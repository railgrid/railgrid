# Kuery provider: fleet-wide object query for MCP, search, and impact analysis

Status: **Implemented**, and rebuilt by provider-contract-remediation §6 — see
"Superseded by provider-contract-remediation §6" below for what changed and why.
Author: 2026-06-11
Related: [kuery](https://github.com/railgrid/kuery) (the query engine this wraps),
`providers/infrastructure/` (the standalone-provider pattern this is modeled on),
`pkg/hub/providers/` (CatalogEntry provisioning), `providers/edges/internal/tunnel/grammar.go`
(the edge data-plane grammar this provider consumes), `docs/providers.md`,
`docs/code-provider-architecture.md`.

## Summary

The **`kuery`** provider gives tenants — and, most importantly, **AI agents via MCP** — a
single query surface over every object across their connected edge clusters. One structured
query against a local SQL-backed index replaces N kubectl round-trips through N reverse
tunnels: "which of my 50 edges run image X / still have the old ConfigMap / are missing CRD
Y" becomes one cheap call instead of a fan-out over slow edge links. On top of that index it
offers relationship traversal for impact analysis ("who consumes this Secret — safe to
rotate?") and a portal UI.

Realistic value ranking, which drives the phasing below:

1. **Fleet-wide search/inventory** — the differentiated piece; nothing in railgrid answers
   cross-edge questions today without per-edge fan-out.
2. **MCP query tools** — a far better agent surface than the per-edge `kubernetes_*` tools:
   one query instead of dozens of tunneled kubectl calls (latency *and* token cost).
3. **Config-rotation impact** — reliable for declared coupling (ownerRefs, spec references,
   selectors); NOT a full dependency map (network calls, DNS-name references, and dynamic
   operator reads are invisible to it). Cross-edge relations additionally require
   `kuery.io/relates-to` / `kuery.io/group` instrumentation, which manifests won't have by
   default.
4. **Graph visualization** — demo layer on top; single-cluster object graphs are commodity
   (Lens, Headlamp, ArgoCD tree).

It wraps [kuery](https://github.com/railgrid/kuery): a read-only, multi-cluster Kubernetes
query engine that syncs objects from N clusters into SQLite/Postgres via dynamic informers
and exposes a single POST-only API (`kuery.io/v1alpha1 Query`) supporting relationship
traversal that plain list/watch can't do:

- `owners` / `owners+` and `descendants` / `descendants+` (ownerReference chains, transitive
  via recursive CTE)
- `references` — spec-field extraction (Pod→Secret/ConfigMap/PVC/SA, Ingress→Service, …),
  extensible per-CRD via the `kuery.io/refs` annotation
- `selects` / `selected-by` — label-selector containment, both directions
- `events` — involvedObject matches
- `linked` / `linked+` and `grouped` — **cross-cluster** relations via the
  `kuery.io/relates-to` annotation and the `kuery.io/group` label

Kuery is already kcp-aware (APIExport identity disambiguation in `internal/sync/kcp.go`) and
has an Engage/Disengage cluster lifecycle. It has **no UI and no authz** — both are this
provider's job:

- railgrid supplies the **clusters** (connected edges, reached through the edges provider's
  `k8s` data-plane verb) and the **tenant boundary**;
- the provider supplies **tenant-scoped query access** and the **visualization** (inventory,
  object graph, impact view).

## Architecture

```
Browser / MCP client
   │  bearer
   ▼
hub /clusters/{id}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run
   │  (kcp: RBAC on savedviews/run, then reverse-proxy to the provider,
   │   caller stamped in X-Remote-*)          hub /services/providers/kuery/{mcp, mcp/sse}
   ▼                                                                 ▼
kuery provider pod
   │
   ├── query verb ── POST /clusters/{id}/apis/…/savedviews/{name}/run (custom subresource)
   │     gate:   SubjectAccessReview get savedviews/{name} on the caller's behalf,
   │             then read the view as the provider (export VW)
   │     then:   scope to the caller's Engagements → kuery engine
   │
   ├── engagement controller (leader only) ── per-workspace edge watch
   │     on connect:    claim the edge's Lease → Engage("{clusterID}/{edge}")
   │     on disconnect: Disengage, Engagement → Stale
   │
   ├── Engagement reconciler (leader only, provider workspace)
   │     Lease expired  → Engagement Stale, index rows marked stale
   │     grace elapsed  → purge the cluster's rows, delete the record
   │
   └── embedded kuery ── informers stream edge objects through reverse tunnels
         into one local store (SQLite PVC; Postgres for production)
```

### Repository layout

```
providers/kuery/                      module github.com/railgrid/provider-kuery
├── main.go                           init | serve (same pattern as infrastructure provider)
├── engagement/                       controller: watch Edge CRs → Engage/Disengage kuery clusters
├── queryapi/                         the query verb: gates, scoping, validation
├── mcpserver/                        kuery_query, kuery_impact MCP tools
├── portal/                           Vue 3 + cytoscape.js graph UI
├── apis/v1alpha1/                    SavedView (exported) + Engagement (private)
├── controller/savedview/             validates spec.query, stamps Ready
├── install/                          the provider-private Engagement CRD
├── index/                            the store's naming + tenant-label contract
├── manifest.yaml                     CatalogEntry
├── deploy/chart/
└── Dockerfile
```

### Data path: the edge's own published coordinate

For every connected `KubernetesCluster` in a tenant workspace, the engagement controller
builds a `rest.Config` whose `Host` is the edge's Kubernetes API **through kuery's own
export virtual workspace** (`engagement/controller.go` `edgeConfig`):

```
Host = <kuery VW endpoint>/clusters/{tenant}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{name}/k8s
```

`kubernetesclusters/k8s` is a custom subresource the edges provider declares; kuery's
export claims it (and the parent kind) as a composition, the tenant accepts both, and kcp
serves it on kuery's virtual workspace, authorizes each call against the claim, forwards
it to the edges provider under kuery's identity, and the edges gate trusts that claim.
The edge's `status.url` is still read, but only as the change signal that re-dials an
edge; nothing is composed from it.

The config authenticates as **the provider itself** — its own kubeconfig credential —
wraps it in a controller-runtime `cluster.Cluster`,
and `Engage`s it into kuery's sync controller under the name `{clusterID}/{edgeName}`,
where `clusterID` is the tenant workspace's kcp logical-cluster ID (taken from the kuery
`APIBinding`'s `kcp.io/cluster` annotation, which must match the reconcile request). Kuery's
discovery + dynamic informers then stream the edge's objects through the existing reverse
tunnel into the local store. On Edge disconnect/delete the controller `Disengage`s and stands
the `Engagement` down; the Engagement reconciler marks the index rows stale and purges them once
the grace has passed (see "Engagement" below). Kuery's own five-minute garbage collector is not
run: this provider knows exactly which cluster expired and when, so purging is a `RequeueAfter`
on one record rather than a periodic walk of the whole store.

### Tenant identity

The tenant key is the **kcp logical-cluster ID** of the tenant workspace — everywhere: the
engaged cluster name (`{clusterID}/{edge}`), the `Engagement`'s `spec.cluster`, the `tenant`
cluster label on the index row, and `objects[].cluster` in query results. Workspace paths
(`root:railgrid:tenants:…`) are display names, never identity: they are not stored, not accepted
as identity, and not translated.

Critically, it arrives in the **request path**, not a header: the `/clusters/{id}` segment kcp
routed the verb on. There is no `X-Railgrid-Cluster` on a verb; that header belongs to the hub's
backend proxy, which carries only the MCP routes for this provider.

### Tenant isolation

Kuery has no authorization of its own, so its API is **never exposed directly**. The one route
into the store is the query verb, a kcp custom subresource `savedviews/run` on kuery's APIExport,
and two decisions are made before the engine is touched:

1. **kcp** authorizes the caller for `savedviews/run` on that name with ordinary RBAC — the verb
   grant — and reverse-proxies the request to the provider with the caller's identity stamped
   in requestheader headers; then
2. the provider's gate (`provider-sdk/dataplane.Gate`) runs a `SubjectAccessReview` for `get` on
   `savedviews/{name}` on that caller's behalf — visibility — and reads the view **as the
   provider** through its export virtual workspace. There is no bearer here.

Only then is the query scoped: the caller's **`Engagement` set** decides which edges they may
reach, and the query is refused (`not_engaged`) if it names an edge outside that set or if the
workspace has no engaged edge at all. The `tenant` cluster label is how that answer is expressed
to an engine whose `ClusterFilter` holds one cluster name or one label map; it is derived state,
not the authority, and the caller's own `cluster.labels` are replaced wholesale rather than
merged (on SQLite, kuery interpolates label KEYS into the generated `json_extract` path).

**What this replaced.** `POST /api/query` and `GET /api/edges` took the tenant from
`X-Railgrid-Cluster` and never looked at the bearer; there was no SSAR and no tenant client. That
was safe only for as long as the hub proxy stripped and re-injected the header — a request sent
straight at the pod was believed. `/api/status` echoed per-replica internals, and
`RAILGRID_DEV_ALLOW_TENANT_QUERY` (`?tenant=<clusterID>`) let any caller name a tenant. All four
were deleted outright, with no compatibility route.

### Engagement: what kuery is syncing, as an API object

Three facts used to be columns on kuery's SQL `clusters` table: which tenant a row belonged to,
whether it was active or stale, and (implicitly) which replica owned it. The query path read
them back and a one-minute timer swept them. They are now a provider-private **`Engagement`**
kind in kuery's own workspace — spec: cluster ID and edge name; status: owner replica, last
seen, phase.

It is installed as a plain CRD by `init` (see `install/`) and is deliberately **absent from the
APIExport** and from the chart's schemas directory: no tenant can bind, claim, read or write it.
That separation is the whole point — `init` applies the schemas directory to the export, so
anything placed there becomes tenant-facing.

What the change buys:

- **The SQL index holds only synced objects** and is a cache: every row in it is rebuildable
  from the Engagements plus a resync, which is why a stale engagement's rows can be purged
  outright.
- **Staleness is a consequence, watched.** The `runOrphanSweep` one-minute list-and-sweep is
  gone; an engagement goes `Stale` because its per-edge Lease expired, and the Engagement
  reconciler watches those Leases. The Lease name is the Engagement name under a fixed prefix,
  so a Lease event maps back to its record without an index.
- **Purging is a deadline, not a ticker.** The five-minute garbage-collection pass over the
  whole store is gone too; a stale engagement schedules its own purge with a `RequeueAfter`.
- **The heartbeat is a Lease renewal and nothing else.** The 20-second per-binding requeue is
  gone: the per-workspace edge watch renews the claims of the edges it owns on one ticker.
- **"Who is syncing what" is answerable with kubectl** against the provider workspace.

### What drives what

Nothing in the provider runs on a timer over a list any more:

- The **APIBinding reconciler** notices a workspace enabling or disabling kuery. It mints
  (or refreshes) that workspace's engagement identity through the hub and starts or stops
  its edge watch — and never lists edges.
- The **per-workspace edge watch** is the authority on which edges exist and which are
  connected. A freshly opened watch replays every edge, so it *is* the list. It creates each
  Engagement, claims the edge's Lease, engages it, and renews both on one ticker.
- The **Engagement reconciler** runs on the provider's own workspace and watches the per-edge
  Leases, as described above.
- The **SavedView reconciler** validates `spec.query` against the published QuerySpec JSON
  Schema and stamps `status.conditions[Ready]`. The CRD cannot do this itself: QuerySpec is
  recursive (`ObjectsSpec.relations` values carry another `ObjectsSpec`) and controller-gen
  refuses recursive types, so `spec.query` is an opaque embedded object and this reconciler is
  where a tenant learns their view will not run — at save time, not at run time.

All three controllers are singletons behind one Lease in the provider workspace
(`provider-sdk/leaderelection`), with the multicluster manager rebuilt per term because a
stopped controller-runtime manager cannot be restarted. Non-leaders keep serving queries, MCP
and the portal: the request path reads the shared store and the shared Engagements, neither of
which needs this replica to be the leader.

### Readiness

`/readyz` is `provider-sdk/vwhealth`: can THIS process reach the APIExport virtual workspace it
watches, and — while this replica is leader — is the multicluster provider actually watching
tenant workspaces? The CatalogEntry's `backend.healthPath` points at it and the heartbeat's
`CanSend` is the same answer, re-evaluated before every beat, so a replica whose watches die
after startup stops reporting itself alive. `/healthz` remains as liveness only: a provider that
cannot reach its virtual workspace is still serving queries and still holding its engagements,
and restarting it would take away the work it is still doing.

A missing provider kubeconfig is **fatal at startup**. Everything that makes this a provider
rather than a web server hangs off it: the gate acts through the export virtual workspace it
names, the Engagements live in the workspace it points at, and the controllers watch tenant
workspaces through it. Serving without one used to be allowed "for UI dev" and produced a
process that answered every health check while being incapable of authorizing or answering a
single tenant query.

Kuery's relationship to the **edge providers** also follows the platform
[provider-isolation rule](./providers.md#provider-isolation-the-cross-provider-boundary):
it reads `KubernetesCluster` CRs under a **declared composition** the tenant accepted (never
a permission claim; the composition IS the claim) and reaches the clusters through the
edges provider's **`kubernetesclusters/k8s` custom subresource**, served on kuery's own
virtual workspace under that claim — it never holds a credential into an edge provider's
backend, and mints no identity.

### MCP tools (the primary consumer)

The provider serves `/mcp` + `/mcp/sse` (proxied at `/services/providers/kuery/mcp`) and
registers in the aggregate. Both tools go through the **same gated executor** as the verb —
they do not talk to the engine. An agent passes `savedView` to run a specific view, or omits it
and gets its caller's scratch view, created on first use with the caller's own credential. The
MCP transport is the one route that still carries the caller's bearer, and it has no cluster in
its path, so the workspace comes from the header the aggregate's federation client injects; that
is addressing, not authority, because the executor still acts with the caller's bearer against
that cluster and a forged header buys a client whose GET and whose access review both fail. Tools:

- `kuery_query` — full QuerySpec passthrough (tenant-scoped): fleet-wide filter by
  kind/labels/conditions/jsonpath, with optional relations and sparse projection.
- `kuery_impact` — convenience wrapper: given one object ref, runs
  `[descendants+, references, selected-by, events, linked+]` and returns the related set.

For an agent, this collapses "check all edges for X" from dozens of tunneled kubectl calls
into one query — the main practical justification for syncing the data at all.

### Impact view

Select any object in the UI → the same `kuery_impact` query → render an interactive graph
with the blast radius highlighted. Because `linked`/`grouped` are cross-cluster, this can
show e.g. "this ConfigMap feeds 4 Pods on edge-a and is grouped with a Deployment on
edge-b". Present it as *declared* coupling, not a complete dependency map (see Summary).
Sparse projection (kuery's field projection compiled to SQL JSON functions) keeps payloads
small enough for interactive use.

### SavedView

`SavedView` (`kuery.providers.railgrid.ai/v1alpha1`, cluster-scoped) is the provider's one
exported kind, and it exists because **a verb needs an object to hang off**. Running a query is
`POST /clusters/{id}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run`, which kcp
authorizes as RBAC on `savedviews/run` for this name before the provider sees it. Because there
is no un-named query route, there is no query a tenant can run that their RBAC does not cover.

Cluster-scoped on purpose: a custom subresource is addressed by cluster and name with no
namespace segment in kuery's grammar, so a namespaced kind could not be reached through it.

Ad-hoc queries stay inside that contract rather than beside it. The portal playground and the
MCP tools both run a per-user scratch view named
`playground-<first 12 hex of sha256(lowercased identity)>`, created in the tenant's workspace
with the caller's own credential on first use. It carries no query — every run supplies one as
the request's `input.query` override — so the object is a name and a grant surface rather than a
store. A digest rather than the address itself, because object names are readable by anyone who
can list the workspace, which is a wider audience than the people entitled to know who has an
account.

Same "tenant-authored CRDs" stance as the code provider: no CachedResource, no virtual storage.

## Key decisions

### 1. Embed kuery as a library — don't sidecar it

Kuery's engine/sync/store live under `internal/`, so the provider cannot import them today,
and the kuery binary only accepts `--kubeconfigs` at startup — there is no dynamic cluster
add/remove over the wire. Since both repos are ours, the cleanest path is a small upstream
kuery refactor moving `internal/{engine,sync,store,gc}` to `pkg/`, after which the provider
embeds kuery: single container, programmatic Engage/Disengage, one process, no
credential-passing hop. The alternative (sidecar + a new kuery admin API for cluster
registration) adds a network boundary and an auth surface for no benefit.

During development, `go replace` against the kuery checkout works even with the monorepo
submodule layout.

### 2. Edges data-plane authorization — the claim is the authorization

Kuery needs a **long-lived credential for background watches**, and it has one: its own
provider credential, valid on its own export virtual workspace. Both the edge objects and
the `kubernetesclusters/k8s` verb are permission claims on kuery's APIExport, derived from
the composition it declares and accepted by each tenant that enables it. kcp resolves the
claims per consuming workspace (no `identityHash`, so an org's self-hosted edges copy works
unchanged), serves the claimed kind and the claimed subresource on kuery's virtual
workspace, and forwards the verb to the edges provider under kuery's identity. The edges
gate recognises a foreign provider forwarded by a shard and trusts the claim kcp enforced.

Nothing is minted: kuery writes no ServiceAccount, no ClusterRole, no binding and no token
Secret, asks the hub identity service for nothing, and holds no identity claims — a claim
on `serviceaccounts` / `secrets` / `clusterroles` / `clusterrolebindings` is a contract
violation
([provider-connectivity-contract.md §"Scoped identities"](./provider-connectivity-contract.md)).

The split is RBAC's, not a preference: `resourceNames` do not apply to a collection
request, so a name-scoped `list` authorizes nothing and an unnamed one is the only shape
that works — which is why the composition is a claim served on kuery's own virtual
workspace, bounded to the one workspace whose admin accepted it, not a minted rule.

**What licenses those rules.** The CatalogEntry declares the composition, identically in
`manifest.yaml` and the chart's `catalogentry.yaml`:

```yaml
requires:
  - provider: edges                 # also the Enable-ordering dependency edge
    group: edges.railgrid.ai        # the group edges SERVES, not its export name
    resources:
      - name: kubernetesclusters
        verbs: ["get", "list", "watch"]
```

A workspace or org admin accepts it at Enable, and the hub re-checks **all** of it on
every mint: the declaration bounds the verbs, the dependency must be the provider that
actually exports the group, its export must be bound in that workspace, and the consent
must still be there. `create` on `kubernetesclusters/k8s` is clause C, which the hub will
only mint because the **edges provider itself declares** that verb on that resource
(`providers/edges/manifest.yaml`, under that resource's
`spec.export.resources[].verbs`) — a coordinate the hub can
verify exists rather than one a consumer invented. Only `kubernetesclusters` is composed:
`LinuxServer` and `MacOSServer` carry no Kubernetes API to sync and kuery does not engage
them.

`edges.railgrid.ai` is never claimed, so this survives an org running its own edges
provider: a composition names the dependency by **name** and is resolved per workspace
through whatever export is bound there.

**The edge set is part of the credential.** The named halves list the edges this replica
actually engages, so the rules change as the fleet does. The controller re-derives them
on every `Token()` and rebuilds the token source when their fingerprint changes
(`identity.go`), which means a re-mint **re-states** the whole rule set: an edge that
disappears takes its grant with it. The old `tenantaccess.EnsureIdentity` could not do
that — its rules were create-if-absent, so a grant never shrank and could never be
widened either, which is why the data-plane grant had to be a second, separately named
`railgrid-kuery-edgeproxy` object. Both objects are gone.

**The token is never pinned into a config.** It is TTL'd (one hour by default, refreshed
at 80% of its life), and an engaged edge's informers outlive it, so both the per-workspace
edge watch and the per-edge edgeproxy `rest.Config` resolve the bearer **per request**
through a wrapped transport. A `401` or `403` invalidates the source, so the next request
re-`Ensure`s with the hub — the identity may have been revoked, or the composition
withdrawn, and re-minting is how this loop finds out. Disable calls `Release`
explicitly, so revocation does not wait out a TTL.

That is the whole authorization story. kcp authorizes kuery's identity for the
`kubernetesclusters/k8s` subresource (RBAC on the `{resource}/{verb}` coordinate, spelled
`*`) before forwarding to the edges provider, whose gate then reviews `get` on the edge for
that identity. Kuery passes like any other caller.

**What this replaced.** The wildcard verb **`proxy`** on the edge object — one grant that
stood in for `k8s`, `ssh`, service proxy and MCP alike — is gone from the edges provider,
and so is this consumer's grant for it. So is `CatalogEntry.spec.edgeProxyAccess`: kuery's
manifest no longer sets it, because the Enable-time grant that flag asked for grants the
**provider** SA, which is not the credential on this path at all. (The field still exists
on the CatalogEntry type and the hub still materializes a — now per-verb — grant for any
provider that sets it; no in-tree provider does.)

**Why the identity is workspace-local, not the provider SA.** The provider SA's token is
issued in `root:railgrid:providers:kuery`. The edges data plane TokenReviews a foreign SA
in the SA's home cluster with its own credential, and the hub's kcp proxy
(`pkg/server/proxy/proxy.go` `serveServiceAccount`) pins every SA caller to the caller's
own workspace — so the review lands on a doubled
`/clusters/{edges}/clusters/{kuery}/…/tokenreviews` path, kcp answers 404, and the edges
provider answers 403. Every engage failed with `discovery failed … Forbidden`. A token
minted **in the consumer workspace** — which is what the hub's identity service does —
authenticates natively through the edges APIExport virtual workspace instead, the same
path edge-agent and delegated `railgrid-du-*` tokens take.

**Runtime properties.** The SAR runs once per proxied request and kuery's watches are
long-lived streams, so it is one TokenReview + SAR per watch (re)establishment —
negligible. Revocation self-heals at three levels: Disable deletes the APIBinding → the
controller releases the identity and the hub deletes its ServiceAccount, killing every
outstanding token → the edge watch 401s and the engagements are stood down → the index
rows are purged. Withdrawing the composition alone is enough on its own: the next refresh
is refused and the credential lapses within one TTL.

Rejected alternative: hub-minted provider tokens checked against a registry allowlist of
enabled tenants (piggybacking on the proxy's static-token bypass). Simpler, but a bespoke
authz path with a powerful bearer secret and no kcp-native audit trail — RBAC keeps "what
can this provider touch" answerable with kubectl, and the `ScopedIdentity` record keeps
"who asked for it, and when does it expire" answerable too.

### 3. Sync scale and defaults

Full-object sync of every edge through the tunnels is the cost center.

- Default to a resource **whitelist** (workloads, config, RBAC, networking — not every CR),
  not kuery's blacklist: edge links are often the scarcest resource in the system, and
  continuous full-object sync is the cost center. Make the list a chart value. (Note:
  excluding events disables the `events` relation.)
- Per-tenant object quotas (engagement controller refuses/flags edges past a budget).
- SQLite on a PVC by default; Postgres as the production chart option (kuery supports both,
  with GIN indexes on Postgres).
- Kuery's safety limits (30s query timeout, 10k row cap, depth cap 20) stay as-is.

## Phasing

- **Phase 0 — unblock.** Kuery upstream refactor (`internal/` → `pkg/`) — **done**
  ([kuery#3](https://github.com/railgrid/kuery/pull/3), merged 2026-06-11). The edge data
  path — **done**, but not as first designed: the hub-side Enable-time grant for the
  *provider* SA (`CatalogEntry.spec.edgeProxyAccess`) turned out not to be the credential
  on this path at all. What ships is kuery **itself**, through its own export virtual
  workspace: the declared composition on `kubernetesclusters` plus `create` on the
  `kubernetesclusters/k8s` custom subresource are claims the tenant accepts, and kcp
  forwards the verb to the edges provider under kuery's identity — see key decision 2.
- **Phase 1 — skeleton.** **Done.** Binary with `/healthz` + heartbeat, CatalogEntry with
  the SavedView schema, Helm chart, Makefile targets (`build-kuery-provider`,
  `run-provider-kuery`, `install-provider-kuery`, …). Visible in the portal catalog.
  Readiness later moved to `/readyz` (`provider-sdk/vwhealth`), which is what the
  CatalogEntry's `backend.healthPath` now points at.
- **Phase 2 — data + MCP.** **Implemented** (providers/kuery: `core/`, `engagement/`,
  `queryapi/`, `mcpserver/`). Engagement controller (watch Edges through each workspace's
  own edges binding, under the declared `edges.railgrid.ai/kubernetesclusters`
  composition — the design's permission claim was never viable, see key decision 2) +
  embedded kuery sync +
  the tenant query surface + MCP tools (`kuery_query`, `kuery_impact`) into the
  aggregator. This is the value milestone: agents can query the fleet.
  Implementation notes vs. this design: tenant scoping is the `Engagement` set,
  expressed to the engine through a kuery *cluster label* (`tenant`, a bare
  identifier). Both upstream kuery follow-ups are done:
  label keys are bound SQL parameters ([kuery#4](https://github.com/railgrid/kuery/pull/4)
  — the query API still replaces caller-supplied cluster labels wholesale, defense
  in depth), and sync supports a whitelist ([kuery#5](https://github.com/railgrid/kuery/pull/5))
  which the chart now defaults to the workloads/config/RBAC/networking set.
  End-to-end suite with a real connected edge is a Phase 3 work item.
- **Phase 3 — UI.** **Implemented**: portal micro-frontend with the edge/object
  inventory table (edge selector, kind/namespace/name filters), the impact drill-down
  (declared blast radius grouped by relation) and the cytoscape topology graph. The
  portal reads its edges and SavedViews with the **kube client** — both are ordinary
  objects in the tenant's own workspace, so what the user can see is what their RBAC
  allows — and runs every query through the verb. Cytoscape is bundled by Vite rather
  than injected as a runtime `<script>`, so it is covered by the build's integrity
  pinning. e2e with a real connected agent is the remaining Phase 3 work item.
- **Phase 4 — polish.** Postgres chart option,
  `split-kuery.yaml` workflow + `railgrid/provider-kuery` mirror with deploy key (see
  `docs/provider-publishing.md`).

## Open questions

- Whether the engagement controller watches Edges through the provider's APIExport virtual
  workspace (one watch across all bound tenants) or per-tenant — VW is the natural fit.
- Cross-shard behavior: tenant workspaces on a different shard than the provider workspace
  may hit the known CachedResource/discovery issues; SavedView is plain APIBinding-bound
  CRDs so it should be unaffected, but verify during Phase 2.
- Event sync: opt-in per tenant? Events are high-churn and the relation is per-cluster only.
