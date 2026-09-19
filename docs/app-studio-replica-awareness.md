# App Studio replica awareness — design

Status: **controllers leader-elected; data plane still project-affine**. Phases
A–C are implemented (run claims; project affinity + peer forwarding; git
re-hydration on adoption, emptyDir workspaces), and the controllers now run
behind a Lease instead of the claims-sharding this note originally proposed
(provider-contract-remediation §9 Cut A). The chart no longer refuses
`replicaCount > 1`; what is still process-local is listed under "What still
requires affinity" below. ·
Date: 2026-08-17, revised for Cut A · Author: design note
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
or concurrently drive the same browser. The chart keeps a Recreate strategy so
an upgrade does not transiently run two pods against that browser
(`deploy/chart/templates/deployment.yaml`), and `replicaCount` still defaults
to 1 — but it is a default now, not a guard.

## Design verdict

No hub changes, no shared filesystem. Three implemented provider-side
mechanisms:

- **Durable run claims in Postgres** make run lifecycle correct fleet-wide.
- **Project pinning + peer forwarding** keep all workspace-touching work on
  one replica per project, with git re-hydration as the failover story.
- **A controller Lease** keeps CR writes single-writer (mechanism 3 below).

These make project state, run state and CR reconciliation replica-aware. They
do not make the workspace-wide Browser replica-safe, so one replica stays the
recommended deployment until browser ownership and serialization become
durable or distributed — but it is now a recommendation the operator can weigh,
not something the chart refuses to render.

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
- **Owner handover**: a replica relinquishes its project claims on shutdown
  (after its listeners drain) by marking them stale — the row and its
  revision floor stay — so its successor adopts them on the next request
  instead of forwarding to a dead pod IP for the rest of the TTL. A replica
  that dies without shutting down is covered by the forwarder: when the
  owner's address cannot even be dialled (the request was never sent and its
  body is unread), the forwarding replica takes the claim over — only if that
  owner still holds it — and serves the request itself. Any other forwarding
  failure stays a 502, because the owner may already have acted on it.
- **Route split** (from the audit's flow map):
  - *Forwarded (workspace/run-touching)*: turn start, steer, interrupt,
    approvals/resume, hydrate-workspace, template switch, scaffold reseed,
    dev-sync, file browser, promote gate, preview-bridge, preview
    interaction (browser lock).
  - *Served anywhere (store/CR-backed)*: SSE event streams, thread/message
    listing, project listing, health, portal assets, MCP surfaces that only
    read the store.
- **Workspace lifecycle follows retained source**: on claiming a project,
  preserve its ProjectUID-scoped tree if its local revision is at least the
  claim's recorded revision. This includes an intentionally empty tree after
  deletions. Inspect that evidence before raising the local revision floor;
  revision metadata alone does not prove that source exists. Hydrate absent
  or stale source from Git when a repository is connected.

**Persistence**: Git is optional and the chart uses a PVC by default. A pod
replacement can have a new address while retaining the same source volume;
pod identity therefore cannot decide whether to overwrite local files with
Git. Ephemeral deployments still lose local source on pod replacement, and
Git can only recover committed files. Projects without Git require retained
storage or an operator backup. Multiple provider replicas remain unsupported.

## Mechanism 3 — leader-elected controllers

This note originally argued for claims-sharding the reconcilers instead of a
global lease, on the grounds that pod readiness required the manager and that
the work is partitioned by project. Both premises are gone:

- Readiness is no longer "manager started". It is `provider-sdk/vwhealth`:
  reachability of the `ai.railgrid.ai` APIExport virtual workspace, plus the
  multicluster provider's watch state attached for the duration of a
  leadership term. A non-leader has nothing attached and is ready on the probe
  alone, because its API server, assistant supervisor and replica-affinity
  forwarder are all still serving. So a standby never wedges a rollout.
- The reconcilers' *writes* are not partitioned by project. Instance
  convergence, status mirroring, the Studio's shared search/browser instances
  and the Session projection are ordinary single-writer CR work, and the one
  path that does read pod-local state — commit convergence out of the
  workspace FileStore — is already gated by `Owns(scope)`, the project claim.
  A leader that is not the project's owner declines that path and leaves it to
  the owner's next signal.

So the controllers run under `provider-sdk/leaderelection.Run` on a Lease named
`app-studio-controllers` in the provider workspace, rebuilt per term: a
controller-runtime manager cannot be restarted, so it is constructed inside
the term and dies with it. Losing the lease costs a controller pause, not a
process restart.

The 15 s manager restart loop is gone with it — the election's own campaign is
the retry that covers a provider coming up before `init` has created its
workspace and endpoint slice. So are the 10 minute safety resyncs in all three
reconcilers: the watches and the signal buses are the triggers, and no
`RequeueAfter` is left on the identity path at all. The 5 s wait for a
ServiceAccount token Secret went with the ServiceAccount — the hub mints the
project and Studio identities synchronously
(`controller/project/identity.go`), so there is no pending dependency to back
off on, and a hub failure is an error the controller's own backoff retries.

The dependency watches hold no credential of their own at all any more: the
Instances, Repositories and RepositoryCommits this provider reconciles are
claimed by its APIExport, so they arrive on the manager's own wildcard informer
alongside Projects, under the same lease, for every tenant workspace at once.
`controller/tenantwatch` — a second watch hub with a per-workspace token and a
relist loop — is deleted.

## What still requires affinity

Leader election makes the *controllers* replica-safe. It does not make the data
plane replica-safe, and three things still are not:

1. **The workspace tree** (`workspace/store.go`) — pod-local, covered by the
   project claim and peer forwarding. The durable fix is moving the source
   tree and commit ledger onto code-provider `RepositoryCheckout`/
   `RepositoryCommit` objects (remediation §9 Cut D).
2. **The workspace's shared Playwright Browser** — serialized only inside one
   process, and project pinning can place two projects of the same workspace
   on different replicas. Unchanged by Cut A; this is the reason
   `replicaCount` still defaults to 1 and the strategy is still Recreate.
3. **Coding-sandbox claims with `assistant.runSandbox.mode=force`** — no
   distributed CAS behind them yet. The chart used to `fail` on this
   combination; it now renders, and the constraint lives here.

## What this deletes

- The orphan-interrupt cross-kill (F1) and the non-mutual Busy gate (F5).
- The chart's `replicaCount != 1` hard fail and the RWO PVC.
- The 15 s controller restart loop, the "manager started" readiness contract,
  and the three 10 minute safety resyncs.
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
| C.1 | Leader-elected controllers, vwhealth readiness, resyncs deleted (remediation §9 Cut A) | Yes — a single replica simply always wins the lease |
| D | Distributed or durable ownership for the workspace-wide Playwright Browser | Required before *recommending* `replicaCount > 1` |
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
