# Provider horizontal scaling — beyond leader election

Status: **proposed** · Date: 2026-08-17 · Author: design note
Related: [`provider-connectivity-contract.md`](./provider-connectivity-contract.md),
[`platform-internal-networking.md`](./platform-internal-networking.md) (tunnel-HA
prior art), [`cross-provider-simplification.md`](./cross-provider-simplification.md)
(kuery's dead sync path), [`helm.md`](./helm.md) §HA (the hub's own scaling
model), `provider-sdk/leaderelection` (the first HA primitive).

## The problem

Leader election (`provider-sdk/leaderelection`) made **code** and
**infrastructure** safe to scale: their multicluster managers host only
write loops, so gating the manager on a Lease leaves non-leaders serving
REST/MCP/portal untouched.

Four providers could not take that fix, each for a different structural
reason, and today they pin `replicaCount: 1`:

| Provider | Why the manager can't just be gated |
|---|---|
| **databricks** | The HTTP action path resolves tenant clusters *through the running manager* (`managerAuthority`, `providers/databricks/controller_manager.go:209-219`); readiness and heartbeat require it too (`main.go:171-181`). |
| **kuery** | Engagement is per-replica serving state: each replica syncs edges into its own store and answers `/api/edges` from an in-memory map (`providers/kuery/engagement/controller.go:103-104,197-208`). |
| **edges** | revdial registers tunnel dialers in a **process-global map closed over the accepted socket** (`provider-sdk/revdial/revdial.go:89-91`); pickup dials and every consumer request must reach the process holding the fd (`providers/edges/internal/tunnel/connman.go:24-26`). |
| **app-studio** | Assistant runs are owned by in-process supervisors, the workspace source tree is a pod-local RWO volume, and an orphan-run reconciler on any *other* replica force-interrupts a live run (`providers/app-studio/api/assistant_supervisor_http.go:1171-1220`); the chart hard-fails `replicaCount != 1` (`deploy/chart/templates/deployment.yaml:3-5`). |

This doc answers: what does each provider actually need to scale out, whether
the hub should grow a session/affinity layer, and in what order to do the work.

## Evidence summary (what the audit found)

Full per-provider audits were done against the tree on 2026-08-17; the
load-bearing findings:

**The hub deliberately has no affinity, and nothing can address a specific
provider pod.** Every request to a provider goes hub backend proxy →
`CatalogEntry.spec.backend.url` → a plain ClusterIP Service
(`pkg/hub/providers/proxy.go:293-326`, `docs/helm.md:190-192`: "no request is
pinned to a pod"). There is no headless Service, no `sessionAffinity`, no
pod-DNS use anywhere in the platform charts. Any per-pod routing scheme is
greenfield.

**databricks' coupling is shallow.** The authority consumer takes exactly one
thing from the manager: `cluster.GetClient()` (`tenant/client.go:191-213`). The
client it gets is just the provider SA config with
`Host = {VW-shard-URL}/clusters/{cluster}` plus an informer cache
(`multicluster-provider/pkg/cache/cluster.go:41-61`). The **agents provider
already builds exactly these clients with no manager at all** — reading
`APIExportEndpointSlice.status.endpoints[].url` directly, with per-shard
probing and a learned cluster→shard cache
(`providers/agents/api/background.go:163-241,303-337`). Databricks even reads
the same slice today and throws the URL away
(`controller_manager.go:656-672`).

**kuery's engagement path is dead code, and its store is a derived cache.**
The reconciler watches the retired `faros.sh/v1alpha1 Edge` kind and dials the
removed `/services/edges-proxy` hub mount (`engagement/controller.go:66,371-374`;
tracked in `cross-provider-simplification.md:110-118`). All synced data lives
in one SQL store (SQLite file on an RWO PVC by default, Postgres supported and
used in dev), keyed by cluster name, fully re-buildable from live discovery +
informer relist — deterministic row IDs, idempotent upserts
(`kuery/pkg/sync/handler.go:20,82-84`). Queries are answered **from SQL, never
from the engagement runtime** (`kuery/pkg/engine/engine.go:35-91`) — so with a
shared Postgres, *any* replica can answer *any* query; only engagement (the
sync work) needs partitioning. The current schema has no ownership column and
disengage/GC are global, so naive 2-replica operation wipes tenant labels and
purges live data (audit items #5-#7).

**edges has exactly two structural blockers, and the industry has exactly two
answers.** (1) Connection affinity: revdial's dialer *is* the accepted socket;
(2) the manager doubles as the serving path's tenant-config resolver — but
that dependency is shallow too: consumers only need
`rest.Config{Host: {VW-URL}/clusters/{cluster}}`, constructible from the slice
the provider already ensures (`controller_manager.go:89`). On tunnel HA the
platform doc already surveyed the field
(`platform-internal-networking.md:456-480`): either **the agent opens one
tunnel per replica** (Konnectivity `--server-count`, kcp per-shard tunneler)
or **the ingress does connection-aware routing** to the owning replica
(Gardener SNI). There is no third option; revdial cannot escape this.

**app-studio is two problems wearing one trench coat.** Everything
conversational is already multi-process-safe in Postgres: revision/sequence
CAS on every mutation, advisory-locked schema bootstrap, and an SSE stream
that is a pure store poll usable from any replica
(`store/store.go:203,215`, `api/assistant_threads.go:823-856`). What is
pod-local is (a) **run ownership** — supervisor maps, Busy/reservation gates,
steering channels, and the orphan-interrupt logic that trusts them
(`api/assistant_supervisor.go:55-56`, `api/assistant_busy.go:22-44`), and (b)
**the workspace file tree** plus its dirty/revision/settlement ledgers on a
pod-local volume (`workspace/store.go:159-180`, `source_state.go:32-36`).
Git (the code provider) is already the durable source of truth for committed
work; the workspace is uncommitted working state.

**The hub's sharedstore is portable but not reusable in place.** It is ~250
lines of Secrets-as-KV with TTL and single-use `Take`
(`pkg/hub/sharedstore/sharedstore.go`), but it lives in the monorepo module and
writes to a hub-internal workspace providers can't reach. The port target is
`provider-sdk`, storing in the provider's **own** workspace — the same move
already made for `leaderelection`.

## The verdict on hub-level session management

**Don't build it.** The audits show three of the four providers scale without
any hub routing change, and the fourth (app-studio) is better served by
ownership state + peer forwarding *inside the provider*:

- The hub's own HA design principle — every replica serves the full surface,
  no request pinned to a pod (`docs/helm.md:190-192`) — is worth preserving.
  A hub-side affinity table keyed by provider-internal concepts (run IDs,
  edge names, project UIDs) would leak every provider's domain model into the
  hub and make the hub a party to each provider's failover story.
- Per-pod addressing from the hub is greenfield (no headless Services, no
  replica identity in CatalogEntry/heartbeats) and was already rejected once
  as a cross-provider pattern (`platform-internal-networking.md:137-140`).
- Affinity routing solves *request placement* but not *state placement*: even
  with perfect stickiness, a replica crash still loses the workspace tree or
  the tunnel unless the state problem is solved anyway. Solve state, and
  stickiness becomes an optimization, not a correctness requirement.

What the platform *should* grow instead are three small, provider-side
primitives in `provider-sdk` (Phase 0 below).

## Shared primitives (provider-sdk)

**P1 — `sharedstore` port.** The hub's Secrets-backed KV (TTL, GC, single-use
`Take`), parameterized on the provider's own workspace + a namespace. Backing
for: app-studio preview-console sessions, small cross-replica memos. (~250
lines + tests, mechanical port.)

**P2 — `ownership` claims registry.** A tiny claim/renew/release API for
*sharded* singletons — "replica R owns item X until TTL" — with the item set
defined by the provider (edge names, cluster names, run keys). Two backends:
kcp Leases in the provider workspace (no new deps; same RBAC the
leader-election work already granted) for small cardinality, and a SQL table
for providers that already run one (kuery, app-studio). This is
leader-election generalized from one lease to N.

**P3 — peer addressing + forwarding.** A headless Service per provider chart
(`clusterIP: None`, pod DNS) plus a small HTTP helper: "not my item → 307/
proxy to the owner replica recorded in P2". Contained entirely inside each
provider; the hub keeps dialing the one ClusterIP Service. Charts gain a
`replicaId` (pod name) env via the downward API.

## Per-provider plans

### databricks — small; no new primitives needed

1. **Slice-backed authority.** Implement `ClusterAuthority` reading
   `APIExportEndpointSlice.status.endpoints[].url` directly (copy the agents
   pattern: multi-shard URL extraction, shard probe + learned cache,
   `background.go:217-241,303-337`). Keep the credential-preserving provider
   config for it (today `SetBaseConfig` deliberately drops credentials —
   `tenant/client.go:114-148` — so add a second path). Reads become live GETs
   instead of informer-cached; acceptable for the action-path volume, and it
   removes the engagement warm-up wait.
2. **Decouple readiness/heartbeat from the manager** — ready when the slice
   yields ≥1 endpoint URL, not when the manager engages.
3. **Leader-elect the controller manager** exactly like code
   (fresh per term, `SkipNameValidation`), now that serving no longer needs it.
4. Chart: `replicaCount` becomes freely scalable.

Behavioral deltas to accept: a tenant without a Ready APIBinding gets a kcp
403/404 instead of "cluster unavailable"; wrong-shard reads must fail loudly
(probe list, don't guess).

### kuery — medium; blocked on fixing the dead sync path first

1. **Revive the data path** (already-tracked defect): watch
   `edges.faros.sh/{KubernetesCluster,LinuxServer}`, dial the edges provider's
   `edgeproxy` URL from `status.URL` instead of the removed hub mount.
2. **Postgres required for >1 replica** (chart gate: refuse `replicaCount > 1`
   with the SQLite/RWO-PVC store). The store is a derived cache — migration is
   "point at Postgres, let it re-sync", no data migration.
3. **Partition engagement with P2 (SQL backend).** Add an `owner_replica` +
   `owner_heartbeat` to the `clusters` table (or a sibling claims table).
   Reconcile becomes: claim edge → engage locally; lost claim → disengage
   *without* touching shared rows. Fix the two global-write hazards the audit
   found: disengage/pod-shutdown must not `markStale`/wipe labels on rows it
   doesn't own, and GC must run only on claim-expired clusters.
4. **Serve `/api/edges` and `/api/status` from SQL**, not the in-memory map —
   then any replica answers any query and any listing; no affinity needed
   anywhere.

### edges — large; adopt tunnel-per-replica (industry option 1)

Recommendation: **the agent dials every replica** (Konnectivity
`--server-count` model), not connection-aware routing — it eliminates the
forwarding hop on the hot data path, keeps the hub untouched, and matches
what kcp itself does per-shard.

1. **Replica discovery for agents.** The provider advertises its replica set
   (count + per-replica pickup identity) on the connect handshake; the agent
   maintains one control WS per replica (re-list on change). Pickup dials go
   to the *replica-specific* path — P3's headless Service gives each replica
   an addressable identity to embed in the pickup URL, so the revdial global
   map is only ever consulted by the process that owns the dialer.
2. **Serving path drops the manager dependency**: construct tenant configs
   from the slice URL directly (same shallow dependency as databricks;
   the two caveats in the audit apply — fail-closed semantics move to kcp).
   With tunnels on every replica, `ConnManager.Load` succeeds locally and the
   502 class disappears.
3. **Event store → shared backend** (the in-code plan: Redis `LPUSH/LTRIM`
   per key, `internal/events/store.go:59-60`), and event *subscribers* gated
   by P2 claims (one UniFi WS per service across the fleet, not per replica).
4. **Controllers**: with every replica able to dial every edge, the
   tunnel-liveness cross-check is no longer replica-local — controllers can
   be leader-elected with the standard pattern. Status writes stop flapping.
5. Interim guardrail (cheap, do first): a runtime assertion that refuses to
   start with >1 endpoints in its own Service while the above is unbuilt —
   today nothing but a values comment prevents the flap storm.

Cost note: N replicas ⇒ N control connections per agent. That is the accepted
trade everywhere in the industry survey; agents are few (per site), replicas
are 2-3.

### app-studio — large; durable run ownership + a workspace decision

The conversational plane is already multi-replica-clean (Postgres CAS + SSE
store-polling). Two work streams:

1. **Run ownership in Postgres (P2, SQL backend).** A `run_owner` claim
   (replica ID + heartbeat) written at `supervisor.Attach`, renewed by the
   worker, cleared on completion:
   - `Busy`/`reserved` consult claims, not local maps — the commit-convergence
     gate and external-operation lock become cluster-wide correct.
   - The orphan-interrupt reconciler fires only on *claim-expired* runs —
     fixing the F1 cross-kill without any routing change.
   - Interrupt/steer/approve on a non-owner replica forward to the owner via
     P3 (single internal hop) instead of returning 409.
2. **The workspace tree.** Two viable endpoints, decide by ops preference:
   - **(a) Shared RWX volume** (NFS/Filestore/EFS): smallest code delta —
     the `mutationMu` process mutex must become a claim-based per-project
     lock (P2), and `fsGroup`/locking semantics need validation on the chosen
     filesystem. Ops burden: an RWX storage class everywhere faros deploys.
   - **(b) Replica-pinned projects** (P2 claim per project + P3 forwarding
     for all workspace-touching routes): no storage dependency; a replica
     crash loses uncommitted workspace state for its projects (recovered by
     re-hydrating from git — the flow that already exists for
     `hydrate-workspace`), which is arguably acceptable for *uncommitted*
     assistant work. Leans into git-as-source-of-truth.
   Recommendation: **(b)** first — it needs no new infra, reuses hydrate, and
   its failure mode (lose uncommitted edits on pod crash) matches user
   expectations of a dev sandbox; (a) remains open as a later hardening.
3. Preview-console sessions move to P1 (sharedstore) or ride the project pin.
4. Only then: leader-elect/claim-gate the Project reconciler per project
   (owner replica reconciles its own projects — reconcile-side sharding via
   the same claims), and lift the chart's `replicaCount != 1` fail.

## Phasing and order

| Phase | Work | Size | Unlocks |
|---|---|---|---|
| 0 | provider-sdk: P1 sharedstore port, P2 ownership claims (Lease + SQL backends), P3 headless-Service + forwarding helper; per-replica heartbeat field (optional, additive) | S-M | everything below |
| 1 | databricks slice-backed authority + leader-elected controllers | S | databricks → N replicas |
| 2 | kuery: revive sync path → Postgres gate → claims-partitioned engagement → SQL-backed listings | M | kuery → N replicas |
| 3 | edges: replica-set handshake + per-replica pickups, manager-free serving configs, shared event store, leader-elected controllers | L | edges → 2-3 replicas |
| 4 | app-studio: run-ownership claims → peer forwarding → project pinning (workspace option b) → per-project reconcile sharding | L | app-studio → N replicas |

Phases 1-4 are independent of each other (order chosen by value/effort);
each ends with the provider's chart guard removed and a failover test
(kill the owner replica mid-operation, assert convergence).

## Open questions

1. **P2 backend split** — is one interface over two backends (Lease vs SQL)
   worth it, or should kuery/app-studio just use their DBs directly and only
   edges use Leases? (Leases in kcp have write-rate limits worth respecting
   at high claim cardinality.)
2. **Edges replica-set handshake compatibility** — old agents that dial once
   must keep working against a scaled provider (they'd reach one replica;
   data-plane requests to other replicas would need P3 forwarding as a
   compatibility shim, or agents upgrade first).
3. **app-studio option (b) crash semantics** — is losing uncommitted
   workspace edits on replica crash acceptable product behavior, or does that
   force option (a)/periodic snapshot-to-git?
4. **Per-replica heartbeats** — the registry is name-keyed and CatalogEntry
   status is single-writer-averse; per-replica liveness may be better served
   by each provider's own claims table than by widening the hub heartbeat
   protocol. Revisit only if a concrete consumer appears.
