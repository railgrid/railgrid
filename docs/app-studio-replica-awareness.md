# App Studio replica awareness — design

Status: **no App Studio state is authoritative on a pod any more.** Phases A–C
are implemented (run claims; project affinity + peer forwarding; git
re-hydration on adoption, emptyDir workspaces), the controllers run behind a
Lease instead of the claims-sharding this note originally proposed
(provider-contract-remediation §9 Cut A), Cut D.2 moved the SSE stream onto
Postgres LISTEN/NOTIFY and conversation retention onto a per-`Session`
deadline, and **Cut D.3 (20 September 2026) moved the working-copy ledger off
the volume and onto `Project.status.workspace`.** What is left on a pod is a
cache of the working tree, which is rebuilt when it is absent or behind, and
one genuinely process-local resource: the workspace's shared Playwright
Browser. That browser is now the only reason `replicaCount` defaults to 1 and
the Deployment keeps `strategy: Recreate`. ·
Date: 2026-08-17, revised for Cut A, Cut D.2 and Cut D.3 · Author: design note
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
stream is a pure store read — it would serve correctly from any replica today,
and since Cut D.2 it is woken by Postgres `LISTEN/NOTIFY` rather than by a
250 ms per-connection poll, so serving it from any replica is also cheap:
an append on replica A wakes the stream replica B is holding open, and each
process pays one listening connection however many streams it serves
(`store/thread_notify.go`). Project/Session/Studio state is kcp
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

   *Half of this is closed by Cut D.3.* The ledgers are on
   `Project.status.workspace`; the tree is a cache. The failure mode above is
   gone at its root: a replica with no tree now reads a revision and a dirty
   set that say so, instead of reading silence as cleanliness.

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
controller-runtime manager cannot be restarted, and neither can the
`tenantwatch.Hub` the Project and Studio reconcilers share, so both are
constructed inside the term and die with it. Losing the lease costs a
controller pause, not a process restart.

The 15 s manager restart loop is gone with it — the election's own campaign is
the retry that covers a provider coming up before `init` has created its
workspace and endpoint slice. So are the 10 minute safety resyncs in all three
reconcilers: the watches and the signal buses are the triggers, and no
`RequeueAfter` is left on the identity path at all. The 5 s wait for a
ServiceAccount token Secret went with the ServiceAccount — the hub mints the
project and Studio identities synchronously
(`controller/project/identity.go`, `controller/studio/identity.go`), so there
is no pending dependency to back off on, and a hub failure is an error the
controller's own backoff retries.

The dependency watches keep a credential of their own, and deliberately. The
Instances, Repositories and RepositoryCommits this provider reconciles belong
to whichever infrastructure and code provider each WORKSPACE bound, so they
cannot ride the manager's wildcard informer: that informer rides this
provider's APIExport virtual workspace, which would have to CLAIM those
first-party kinds, and a first-party claim pins one serving `identityHash` for
every consumer at once. `controller/tenantwatch` therefore keeps one LIST/WATCH
per tenant workspace at `{hub}/clusters/{cluster}`, as the same hub-minted
identity the reconcilers write with, started on the first reconcile that holds
a token and replaced when a 401/403 proves the token in hand is dead. It is
still watch-driven end to end: no resync, no relist timer, only a bounded
backoff after an error.

That the hub is per-term is why it is built in `runControllerManager` next to
the manager — a stopped hub, like a stopped manager, is not restartable.

## What still requires affinity

Leader election makes the *controllers* replica-safe. Cut D.2 (20 September
2026) removed the two places where a *conversation* still depended on a clock
running inside each replica:

- The SSE stream's 250 ms per-connection Postgres poll is gone. Appends issue
  a `NOTIFY` on `app_studio_assistant_thread_events`; one `LISTEN` connection
  per process fans the arrival out to every stream that process is holding
  (`store/thread_notify.go`). Postgres delivers to every listener, so an
  append on one replica wakes a stream on another, and the 15 s keepalive is
  the safety net for a notification lost to a listener reconnect — the reader
  always re-reads from its own sequence cursor, so a missed or duplicated
  signal costs latency, never an event. The memory store implements the same
  interface with the in-process broadcaster alone.
- `runRetention`'s per-replica ticker is gone. `SessionStatus` now carries
  `turnCount` and `lastActivityAt`, and the Session reconciler requeues at
  `lastActivityAt + retention` and deletes the Session; the purge is the
  finalizer that already existed. Expiry is therefore the controller leader's
  work — once per fleet instead of N replicas racing on one cutoff — and a
  conversation with an in-flight turn has no deadline until that turn settles.
  `runAttachmentRetention` stays a cutoff sweep on purpose: a *draft*
  attachment belongs to a project and an actor, carries its own expiry, and
  can predate any thread, so it has no Session to hang a deadline on.

Cut D.2 was the conversational half of Cut D. **Cut D.3 (20 September 2026) is
the file half, and it is done**: no App Studio state is authoritative on a pod
any more. One thing still requires affinity, and one combination is still
refused outright.

### What Cut D.3 moved, and where it went

The working-copy ledger — the source revision the development data plane
fences on, the set of paths that differ from the last commit, the
RepositoryCommit in flight, and the settlement receipt that clears the dirty
set once it lands — is now `Project.status.workspace`, written through the
status subresource under optimistic concurrency.

It went onto App Studio's own kind rather than onto the Code provider's
`RepositoryCheckout`, which the original plan named. A `RepositoryCheckout` is
a one-shot operation object: a repositoryRef plus a ref in spec, the checkout's
own result in status. "Which revision this project's working copy is at" is not
a property of a checkout that happened once; it is App Studio's state about a
project App Studio owns, and putting it on another provider's CRD would have
meant new fields there, a `repositorycheckouts` entry in
`dependencies[].composes`, and a clause E — for state that provider never
reads. Clause A covers the new fields where they are, and nothing about the
composition or the Code provider changed.

The bound on `uncommittedPaths` is derived rather than picked: a project tree
is capped at 500 files (`workspace.maxWorkspaceTreeFiles`), so the largest
single transition is a whole-tree replacement — up to 500 written paths plus up
to 500 deletions — and `MaxItems: 1024` leaves headroom above it. Each entry is
bounded by the 1024 bytes the file store already accepts for a path, so nothing
the store admits can fail to be recorded. Overflowing the bound is a bug in
some other bound, so the ledger reports it instead of truncating: a dropped
path is a file that never reaches git.

What that buys, concretely:

- **A replica with no tree no longer reads "clean".** It reads the project's
  revision and its dirty set, which is what audit F3 needed and what neither
  the claim's revision floor nor a missing JSON file could say. The
  empty-file-list push to a dev sandbox is closed at the root, not by keeping
  the request away from the wrong replica.
- **The pending commit is one record.** It used to be an annotation on the
  Project pointing at a `RepositoryCommit` plus a file beside the tree holding
  the digest and paths it carried, and those two could disagree the moment a
  volume was replaced — the reconciler carried a branch for exactly that. Both
  halves are members of `status.workspace.pendingCommit` now, the branch is
  gone, and the project identity's named `get` grant reads the same record the
  convergence loop follows (`controller/project/identity.go`).
- **Settlement can finish on a different replica than the one that committed.**
  The receipt is on the object; the digest check reads the local tree, so a
  replica that does not hold the tree declines to settle rather than clearing a
  dirty set it cannot verify, and the receipt waits for one that can.
- **Cut D.4's leftover trees stop mattering.** A leader finalizing a project it
  never held files for still deletes best-effort per replica, but a leftover
  tree on a non-owner is now inert in a stronger sense: its ProjectUID is gone
  from the ledger too, so nothing can read a revision for it.

### What is still pod-local, and why that is fine

The **working tree** — the bytes. It is a cache, and it is treated as one:

- `RetainsSource` no longer compares two pod-local numbers. It compares a local
  tag (`workspace/tree_revision.go`, "what revision were the bytes in THIS
  directory written at") against the ledger's revision. Absent, behind, or
  untagged all read as stale.
- A stale or absent tree is **rebuilt** from the last commit
  (`api/project_hydrate.go`), in one `ReplaceTree` — one revision, one ledger
  update, rather than a control-plane write per checked-out file. The rebuild
  is marked `Committed`, which says what a rebuild means: these bytes ARE the
  repository's, so the paths come back clean instead of queued for a commit
  that would push git's own content back to git.
- What a lost volume still costs is the **uncommitted bytes themselves**. Git
  cannot return what was never committed, and no amount of control-plane state
  changes that. The ledger's job is to make the loss *legible* — the rebuild
  notice names the dropped revision — instead of silent.

### Why project affinity is still here

`X-Railgrid-Project` routing (`api/replica_affinity.go`) is now a **cache
locality optimization, and it is kept deliberately as one**.

Correctness no longer depends on it: a request that lands on a replica without
the tree rebuilds rather than serving an empty workspace, and the ledger it
rebuilds against is the same one everywhere. The alternative — dropping the
forward and rebuilding on whichever replica the Service happened to pick —
would mean a git checkout per request under round-robin, for a working copy
that is cheap to keep in one place and expensive to move. One intra-cluster hop
is the better trade, and it is now a trade rather than a requirement.

The claim's recorded revision survives for one narrower job: a claim that ran
*ahead* of a status write names a revision the development data plane has
already been handed, so adoption repairs it into the ledger
(`EnsureSourceRevisionFloor`) before anything reads it. It is no longer the
fence.

### The one thing left, and the one thing refused

1. **The workspace's shared Playwright Browser** — one single-session browser
   per WORKSPACE, serialized only inside one App Studio process, while project
   pinning can place two projects of the same workspace on different replicas.
   Unchanged by Cut A, D.1, D.2, D.3 and D.4. It is now the *only* reason
   `replicaCount` defaults to 1 and the Deployment keeps `strategy: Recreate`
   — an upgrade must not transiently run two pods against that browser. Both
   stay, and `deploy/chart/values.yaml` says so in those words. Phase D below
   is the answer.
2. **Coding-sandbox claims with `assistant.runSandbox.mode=force`** — no
   distributed CAS behind them. The chart refuses that one combination with
   `replicaCount > 1`, and that `fail` stays.

The `ReadWriteOnce` claim also stays, for a reason that is now about data
rather than authority: a project with no repository yet has nothing to rebuild
its tree from, and RWO is what every storage class serves for the
single-replica default. `emptyDir: true` became a supportable production choice
for installations where every project has a repository — the reconciler commits
a dirty workspace as soon as the project goes idle, so the exposure is short —
and multiple replicas want `emptyDir` or a ReadWriteMany class, never a shared
RWO claim.

## What this deletes

- The orphan-interrupt cross-kill (F1) and the non-mutual Busy gate (F5).
- The chart's `replicaCount != 1` hard fail.
- The pod-local working-copy ledger: `source-state.json`,
  `source-revision.json`, `commit-settlement.json`, `pending-commit.json` and
  the `initial-repository` receipt are all gone, along with the
  `ai.railgrid.ai/pending-commit` annotation that mirrored one of them and the
  reconciler branch for the two disagreeing (Cut D.3). The RWO PVC stays, but
  for a different reason than it was listed here under: it holds the working
  tree of a project that has no commit to rebuild from, not authority.
- The SSE stream's per-connection 250 ms store poll, and the per-replica
  conversation-retention ticker (Cut D.2).
- The 15 s controller restart loop, the "manager started" readiness contract,
  and the three 10 minute safety resyncs.
- The empty-workspace dev-sync wipe (F3) — non-owners never touch workspaces.
- Preview-bridge session breakage (F6) — bridge routes ride the pin.

These mechanisms do **not** delete the shared-browser race: projects in one
workspace can have different owners while still targeting the same Browser. It
is the last thing holding the `replicaCount: 1` default and the `Recreate`
strategy, and Phase D is what answers it.

## Phasing

| Phase | Work | Safe at 1 replica? |
|---|---|---|
| A | Run claims table + Busy/reservation/orphan-interrupt on claims | Yes — strictly better restart semantics |
| B | Internal listener + project claims + forwarding middleware; revision fence to durable side | Yes — forwarding is a no-op single-replica |
| C | Claim-driven hydration, `emptyDir` default, reconciler owner-gating | Makes workspace/run state replica-aware, but does not unlock N replicas by itself |
| C.1 | Leader-elected controllers, vwhealth readiness, resyncs deleted (remediation §9 Cut A) | Yes — a single replica simply always wins the lease |
| C.2 | Working-copy ledger onto `Project.status.workspace`; tree becomes a rebuildable cache (remediation §9 Cut D.3) | Yes — one replica reads and writes its own project's status |
| D | Distributed or durable ownership for the workspace-wide Playwright Browser | Required before *recommending* `replicaCount > 1` |
| E (optional) | WIP snapshot-to-git; preview-bridge to shared store if forwarding proves noisy | Hardening |

## Open decisions

1. ~~**`emptyDir` + re-hydration vs keeping per-pod PVCs**~~ — **settled by Cut
   D.3, both ways.** The volume is a cache, so `emptyDir` is a supportable
   production choice; the PVC stays the default because a project with no
   repository yet has no commit to be rebuilt from. `deploy/chart/values.yaml`
   states which installation wants which.
2. **Crash-loss tolerance**: is losing uncommitted edits on replica crash
   acceptable v1 behavior, or is Phase D's WIP snapshotting a launch
   requirement? Cut D.3 narrows but does not answer this: the provider now
   knows exactly WHICH paths were lost and says so in the rebuild notice, and
   the reconciler commits a dirty workspace as soon as the project goes idle,
   so the window is short — but the bytes are still gone.
3. **Forward-all vs selective**: start with the selective route split above,
   or forward every project-scoped route and carve out reads later? The
   selective split is more work to get right but keeps SSE latency flat.
4. **Shared Browser ownership**: put the Browser session lease in durable
   shared state, or route every workspace's browser calls to one designated
   replica? Either solution must cover all projects in the workspace and
   preserve the no-replay-on-unknown-outcome contract.
