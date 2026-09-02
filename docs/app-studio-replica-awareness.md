# App Studio replica awareness — design

Status: **single-replica deployment boundary enforced**. Phases A–C are
implemented (run claims; project affinity + peer forwarding; git re-hydration
on adoption, emptyDir workspaces, owner-gated commit convergence), but the
workspace's shared Playwright Browser adds a newer fleet-wide serialization
requirement that these mechanisms do not satisfy. ·
Date: 2026-08-17 · Author: design note
Related: [`provider-horizontal-scaling.md`](./provider-horizontal-scaling.md)
(the cross-provider plan this details), the kuery per-edge claims
(`providers/kuery/engagement/claims.go`) and the edges replica routing
(`providers/edges/internal/tunnel/{registry,remote}.go`) — the two primitives
this design reuses.

## Where app-studio actually stands

The audit (2026-08-17) split the provider's state cleanly in two:

**Already multi-replica-clean.** Everything conversational lives in Postgres
with real concurrency control: revision-CAS on run snapshots, sequence-CAS on
thread events, `SELECT … FOR UPDATE`, advisory-locked schema bootstrap
(`store/store.go:203,215`, `store/postgres.go:38,996`). The client-facing SSE
stream is a pure store poll (`api/assistant_threads.go:823-856`) — it would
serve correctly from any replica today. Project/Session/Studio state is kcp
CRs; committed source of truth is git via the code provider.

**Pod-local.** Two clusters of state, and they are different problems:

1. **Run ownership** — the supervisor's `runs`/`reservations` maps, the Busy
   gate, steering channels, thread mirrors (`api/assistant_supervisor.go:55-56`,
   `api/assistant_busy.go:22-44`). Worst consequence: the orphan-run
   reconciler (`api/assistant_supervisor_http.go:1171-1220`) force-interrupts
   any `running` run **not found in the local supervisor** — with two
   replicas, the first poll that lands on the wrong one kills a live run.
2. **The workspace tree** — the assistant's working files plus the
   dirty/revision/settlement ledgers on a pod-local RWO volume
   (`workspace/store.go`, `source_state.go`). A second replica sees an empty
   tree, which silently reads as "project clean" and can push an **empty
   file list** to the dev sandbox (audit F3).

There is also one cross-project resource: each workspace has one shared,
single-session Playwright Browser. The browser session manager serializes that
resource only within one App Studio process. Project pinning can place projects
from the same workspace on different replicas, so two processes could invalidate
or concurrently drive the same browser. The chart therefore rejects
`replicaCount > 1` and uses a Recreate strategy to avoid transient overlap
during upgrades (`deploy/chart/templates/deployment.yaml`).

## Design verdict

No hub changes, no shared filesystem. Two implemented provider-side mechanisms:

- **Durable run claims in Postgres** make run lifecycle correct fleet-wide.
- **Project pinning + peer forwarding** keep all workspace-touching work on
  one replica per project, with git re-hydration as the failover story.

These mechanisms make project and run state replica-aware, but they do not make
the workspace-wide Browser replica-safe. App Studio remains one replica until
browser ownership and serialization become durable or distributed.

The hub keeps its "no request pinned to a pod" model: the Service still
round-robins, and the *provider* forwards internally exactly like edges does
for tunnels — one intra-cluster hop, invisible to the hub and the browser.

## Mechanism 1 — run claims (Postgres)

A `run_claims` table (or owner columns on `assistant_runs`): `run_key`,
`owner_replica`, `owner_addr`, `heartbeat_at`. Semantics copied from kuery's
per-edge claims, SQL-backed because the store is already there:

- `supervisor.Attach` inserts the claim (`ON CONFLICT` guarded by a staleness
  check); the worker goroutine renews it (~15s); completion clears it.
- **Busy / reservations become cluster-wide**: `Busy(scope)` is "local map OR
  live claim in Postgres"; the external-operation lock (hydrate, template
  switch, delete) claims through the same table, so two replicas can no
  longer run a turn and a hydrate concurrently (audit F5).
- **The orphan-interrupt reconciler fires only on claim-EXPIRED runs**
  (heartbeat older than ~60s), never on "not in my local map". This deletes
  failure mode F1 outright, and is a strict improvement even at one replica
  (today a provider restart mid-run relies on the same local-map heuristic).
- Thread mirrors and any other per-run singletons gate on holding the claim
  (F8).
- Interrupt/steer/approve on a non-owner replica: resolve `owner_addr` from
  the claim and forward (mechanism 2's listener), instead of today's 409
  `"assistant turn is not active on this provider"`.

## Mechanism 2 — project pinning + peer forwarding

The workspace tree cannot be shared cheaply, so it isn't: each project is
**owned by one replica at a time**, and workspace-touching requests execute
on the owner.

- **Claim**: `project_claims` row (same table shape) keyed by project UID,
  holding `owner_addr` (`podIP:internalPort` — the edges pattern; POD_IP via
  downward API). Acquired lazily by the first workspace-touching request for
  an unclaimed project; renewed while runs/dev-sync are active; expires after
  ~10 minutes idle so projects rebalance across the fleet over time.
- **Forwarding**: an internal HTTP listener (plain `httputil.ReverseProxy`,
  shared-bearer auth, loop-guard header — no raw relay needed since
  app-studio is HTTP-only). A middleware on project-scoped routes checks the
  claim: owner → serve; foreign owner → forward; no owner → claim and serve.
- **Route split** (from the audit's flow map):
  - *Forwarded (workspace/run-touching)*: turn start, steer, interrupt,
    approvals/resume, hydrate-workspace, template switch, scaffold reseed,
    dev-sync, file browser, promote gate, preview-bridge, preview
    interaction (browser lock).
  - *Served anywhere (store/CR-backed)*: SSE event streams, thread/message
    listing, project listing, health, portal assets, MCP surfaces that only
    read the store.
- **Workspace lifecycle becomes claim-driven**: on claiming a project whose
  tree is absent locally, re-hydrate from git (the existing
  `hydrate-workspace` flow) before serving. This turns the volume into a
  **cache**: the chart's RWO PVC can become `emptyDir`, pods become cattle,
  and the migration-marker/monotonic-growth issues go with it. The
  `source-revision.json` fence must be seeded from the durable side (a
  revision column on the project claim) rather than restarting at 1, so the
  dev-sandbox staleness check survives a move (audit F3).

**Failover semantics (the deliberate trade)**: a replica crash loses the
uncommitted workspace edits of the projects it owned; the next request
re-claims elsewhere and re-hydrates from the last commit. For a dev sandbox
whose durable truth is git, that matches user expectations — and commit
cadence is assistant-driven and frequent. Hardening later, if wanted:
periodic WIP snapshot pushes to a scratch branch shrink the loss window
without changing the design.

## Mechanism 3 — reconciler sharding (no leader election)

The multicluster manager stays on every replica (pod readiness requires it —
`controller_manager.go:266-292` — and it is not serving-entangled). Instead
of a global lease, the **Project reconciler's commit-convergence path runs
only on the project's owner** (claim check at the top; foreign projects
requeue). The CR-only parts (instance ensure, status mirroring) are
idempotent and may stay active-active initially; folding them under the same
claim check is a later cleanup. This is claims-sharding, same shape as
kuery's engagement — leader election is the wrong tool here because the
work is inherently partitioned by project, not singleton.

## What this deletes

- The orphan-interrupt cross-kill (F1) and the non-mutual Busy gate (F5).
- The chart's `replicaCount != 1` hard fail and the RWO PVC.
- The empty-workspace dev-sync wipe (F3) — non-owners never touch workspaces.
- Preview-bridge session breakage (F6) — bridge routes ride the pin.

These mechanisms do **not** delete the shared-browser race: projects in one
workspace can have different owners while still targeting the same Browser.
The chart's single-replica guard remains the safety boundary.

## Phasing

| Phase | Work | Safe at 1 replica? |
|---|---|---|
| A | Run claims table + Busy/reservation/orphan-interrupt on claims | Yes — strictly better restart semantics |
| B | Internal listener + project claims + forwarding middleware; revision fence to durable side | Yes — forwarding is a no-op single-replica |
| C | Claim-driven hydration, `emptyDir` default, reconciler owner-gating | Makes workspace/run state replica-aware, but does not unlock N replicas by itself |
| D | Distributed or durable ownership for the workspace-wide Playwright Browser | Required before allowing `replicaCount > 1` |
| E (optional) | WIP snapshot-to-git; preview-bridge to shared store if forwarding proves noisy | Hardening |

## Open decisions

1. **`emptyDir` + re-hydration vs keeping per-pod PVCs** (StatefulSet +
   volumeClaimTemplates). Recommendation: `emptyDir` — a PVC that survives
   pod moves but not claim moves buys little once hydration is claim-driven.
2. **Crash-loss tolerance**: is losing uncommitted edits on replica crash
   acceptable v1 behavior, or is Phase D's WIP snapshotting a launch
   requirement?
3. **Forward-all vs selective**: start with the selective route split above,
   or forward every project-scoped route and carve out reads later? The
   selective split is more work to get right but keeps SSE latency flat.
4. **Shared Browser ownership**: put the Browser session lease in durable
   shared state, or route every workspace's browser calls to one designated
   replica? Either solution must cover all projects in the workspace and
   preserve the no-replay-on-unknown-outcome contract.
