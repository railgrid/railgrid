# Kuery provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`railgrid/provider-kuery`](https://github.com/railgrid/provider-kuery)
> repository is **automatically synced** from the railgrid monorepo
> [`railgrid/railgrid`](https://github.com/railgrid/railgrid) (path `providers/kuery/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`railgrid/railgrid`](https://github.com/railgrid/railgrid) instead.

Fleet-wide object search, relationship traversal, and impact analysis across
the edge clusters connected to a railgrid workspace — built on
[kuery](https://github.com/railgrid/kuery), a multi-cluster query engine that
syncs objects into a local SQL store and answers relationship queries
(owners, descendants, spec references, selector matches, cross-cluster
links) that plain list/watch can't.

The full design — architecture, tenant isolation, the Enable-time
edges-proxy grant, value ranking, and phasing — lives in the railgrid repo at
[`docs/kuery-provider-architecture.md`](../../docs/kuery-provider-architecture.md).

## Status: Phase 2 — fleet query engine

What works today:

- **Embedded kuery engine** (`core/`): SQLite (default, on the chart's
  PVC) or Postgres store, the query engine, the multi-cluster sync
  controller, and the stale-cluster GC. Sync is whitelist-driven — the
  chart defaults to the workloads/config/RBAC/networking set
  (`sync.whitelist`); non-whitelisted types stay discoverable but ship no
  objects.
- **Edge engagement** (`engagement/`): watches `Edge` objects across every
  tenant workspace that Enabled the provider (APIExport virtual
  workspace), and syncs each connected kubernetes edge through **kuery's own
  export virtual workspace**, as the provider: the declared composition on
  `edges.railgrid.ai/kubernetesclusters` plus the `kubernetesclusters/k8s`
  custom subresource are claims the tenant accepts, kcp forwards the verb to
  the edges provider under kuery's identity, and no per-workspace identity is
  minted. This provider mints no ServiceAccount and holds no RBAC-authoring
  claims. Engaged clusters are keyed
  `{clusterID}/{edgeName}` and labelled with their tenant, where the tenant
  key is the tenant workspace's **kcp logical-cluster ID** (read from the
  kuery `APIBinding`'s `kcp.io/cluster` annotation) — never a workspace
  path. Each engaged edge gets an `Engagement` record in kuery's own
  workspace (`apis/v1alpha1`, installed as a provider-private CRD by `init`,
  deliberately NOT exported): spec is the cluster ID and the edge name,
  status is the owning replica, its last heartbeat and a phase. That record
  is the authority for "which edges may this caller query"; the SQL index
  holds only synced objects and is rebuildable from it. Where to reach an edge
  comes from the edge's own published coordinate (`status.URL`, as
  `edges.railgrid.ai/KubernetesCluster` spells it) — never a path this provider
  builds. An edge that has none yet (it was just created; the coordinate is
  stamped by a later reconcile) holds its claim and sits `Pending` with
  `waiting for the edge to publish status.url`, not an error: the same watch
  delivers the update that adds the coordinate, and that update is what engages
  it. Nothing here retries on a timer.
- **Edge sharding** (`provider-sdk/sharding`): which replica syncs which edge
  is one `coordination.k8s.io` Lease per edge in kuery's own workspace, keyed
  by the store name, held by the replica doing the work and renewed on its own
  clock. A replica only engages edges it wins; losing one to a peer and
  picking up a departed peer's edges both arrive as events on the shard's own
  Lease watch, so nothing re-lists and nothing polls. See
  [Running more than one replica](#running-more-than-one-replica).
- **The query verb** (`queryapi/`): the kcp custom subresource
  `savedviews/run` on kuery's APIExport, the provider's ONE tenant route,
  reached on the hub's kcp front door like any other kube path:
  `POST /clusters/{clusterID}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run`.
  kcp authenticates the caller and authorizes the verb with ordinary RBAC
  (a grant on the coordinate is `*`; the HTTP method is the RBAC verb), then
  forwards the request to the provider with the caller's identity stamped in
  requestheader headers — no bearer travels. The shared gate
  (`provider-sdk/dataplane`) settles visibility with a SubjectAccessReview
  for `get` on the SavedView run on the caller's behalf, reads the view AS
  THE PROVIDER through kuery's export virtual workspace, and only then is the
  query scoped to the caller's Engagements. There is no hub-proxied
  `/services/providers/kuery/dataplane/…` spelling. The body may carry
  `{"input":{"query":…}}` to override the view's saved query. Results report
  `objects[].cluster` as `{clusterID}/{edge}`. The MCP tools reach the same
  executor with the caller's own bearer — the one class the hub still
  proxies with a credential — and gate as the caller: a GET of the view and
  a SelfSubjectAccessReview for `create` on `savedviews/run`.
- **SavedView reconciler** (`controller/savedview/`): validates `spec.query`
  against the published QuerySpec JSON Schema and stamps
  `status.conditions[Ready]`, because the CRD cannot — QuerySpec is recursive
  and controller-gen refuses recursive types.
- **MCP tools** (`mcpserver/`): `kuery_query` (fleet-wide spec
  passthrough) and `kuery_impact` (declared blast radius of one object) at
  `/mcp` + `/mcp/sse`, through the same gated executor as the REST verb.
  Pass `savedView` to run a specific view, or omit it for the caller's own
  scratch view.
- **Portal UI** (`portal/`): fleet topology graph, inventory table
  (edge/kind/namespace/name filters, click-through) and the impact view —
  the declared blast radius of one object, grouped by relation. Edges and
  SavedViews are read with the **kube client** from the tenant's own
  workspace, not from this provider.
- **Registration surface**: heartbeats, CatalogEntry (SavedView schema,
  and the `edges` requirement it declares on
  `kubernetesclusters`), Helm chart. Every permission claim on the generated
  APIExport is **derived from `spec.requires`** and **pins no `identityHash`**,
  so kcp resolves each one per consuming workspace against whatever `edges`
  export is bound there; a pinned first-party (`*.railgrid.ai`) claim would fix
  one serving identity for every consumer at once and break as soon as one org
  self-hosts `edges`. And nothing requires
  `serviceaccounts`/`secrets`/`clusterroles`/`clusterrolebindings`, because
  a provider does not mint identities. Edge discovery and the `k8s` verb both
  ride kuery's own export virtual workspace under the accepted requirement
  claims (`engagement/controller.go` `edgeConfig`).

What lands next (see the design doc): an e2e suite asserting edge-object
sync end to end with a real connected agent, and the Postgres chart option.

## Running more than one replica

`replicaCount > 1` is a supported configuration **with
`store.driver=postgres`** (the chart refuses it with the SQLite/RWO-PVC store,
which no second pod can mount). Every replica does real work — there are no
standbys:

- **Queries scale with replicas.** Every replica answers queries, MCP and the
  portal out of the same Postgres and the same `Engagement` records, so no
  request needs to reach a particular pod.
- **Edge sync is divided, not duplicated.** `engagement.Run` starts on every
  replica (`main.go`), and each replica takes a `provider-sdk/sharding` claim
  on `{clusterID}/{edgeName}` before it dials anything, engaging only the
  edges it wins. Two replicas reconciling the same workspace at the same time
  is therefore correct by construction rather than by timing: one wins each
  edge, the other is told to keep off it. Three replicas sync roughly a third
  of the fleet each.
- **Failure costs a share, not the service.** A replica that shuts down
  cleanly releases its claims, so its edges move to peers in one watch event;
  one that dies loses them after `claimTTL` (60s), which is also the earliest
  the `Engagement` reconciler may call those records stale. Either way the
  other replicas keep syncing everything else throughout.
- **Readiness is per replica.** `/readyz` and the hub heartbeat report whether
  *this* replica is really watching tenant workspaces, because no peer is
  covering for it — a replica that cannot reach the APIExport virtual
  workspace syncs none of the edges it claimed and must say so.

What is still a singleton, behind the `kuery-controllers` Lease, is the pair
of loops that want exactly one writer: the **SavedView reconciler** (it stamps
a tenant object's status, and every replica would compute the same verdict) and
the **Engagement reconciler** (the garbage collector — it marks index rows
stale and deletes a purged engagement's rows and record). Neither is on the
sync path, so a gap between leadership terms delays a status stamp or a purge
and nothing else.

## Layout

```
main.go             serve loop: /healthz, /readyz, the query verb, /mcp, portal, heartbeat
apis/v1alpha1/      SavedView (exported) + Engagement (provider-private)
controller/savedview/  validates spec.query, stamps Ready
install/            applies the private Engagement CRD into the provider workspace
index/              store naming + the tenant label both halves agree on
core/               embedded kuery wiring (store, engine, sync)
engagement/         edge watch → Engage/Disengage, Engagements, per-edge sharding claims
queryapi/           the query verb: gates, engagement scoping, QuerySpec validation
mcpserver/          kuery_query + kuery_impact, through the same gated executor
assets.go           //go:embed of portal/dist
manifest.yaml       CatalogEntry (spec.export: savedviews/run on SavedView; spec.requires: edges incl. kubernetesclusters/k8s, the review API)
portal/             Vite + TS micro-frontend (custom element)
deploy/chart/       Helm chart (host cluster only; PVC for the SQLite store)
```

## Deploying to a cluster (Helm)

The provider runs on the **host cluster** and registers itself into the hub.
The chart is published as an OCI artifact at
`oci://ghcr.io/railgrid/charts/railgrid-kuery-provider`.

### Prerequisites

- A reachable railgrid hub (`hub.url`).
- A **provider kubeconfig** — the workspace-admin kubeconfig minted via the
  admin onboarding flow (`/bonkers`). Stored as a Secret whose key **must be
  `kubeconfig`**.
- For the Postgres store: a running PostgreSQL with a Secret exposing a
  connection `uri` (e.g. a CloudNativePG cluster, whose `*-app` Secret carries
  `uri`). Skip this for the default SQLite store.

### 1. Namespace

```bash
kubectl create namespace railgrid-prod-provider-kuery
```

### 2. Provider kubeconfig Secret

The key **must** be `kubeconfig` (this matches the chart default
`providerKubeconfig.secretName=railgrid-provider-kubeconfig`):

```bash
kubectl -n railgrid-prod-provider-kuery create secret generic railgrid-provider-kubeconfig \
  --from-file=kubeconfig="railgrid/provider-kuery.kubeconfig"
```

### 3. (Postgres only) build the DSN

Read the connection URI from the database Secret and require TLS:

```bash
DSN="$(kubectl -n railgrid-prod-provider-kuery get secret kuery-pg-app \
  -o jsonpath='{.data.uri}' | base64 -d)?sslmode=require"
echo "$DSN"
```

### 4. Install / upgrade

```bash
helm upgrade --install kuery oci://ghcr.io/railgrid/charts/railgrid-kuery-provider:0.0.8 \
  -n railgrid-prod-provider-kuery \
  --set image.tag=v0.0.8 \
  --set hub.url=https://railgrid-railgrid-hub.railgrid-prod.svc.cluster.local:9443 \
  --set hub.insecure=true \
  --set hub.tokenSecretRef.name="" \
  --set catalogEntry.enabled=true \
  --set store.driver=postgres \
  --set store.persistence.enabled=false \
  --set-string store.dsn="$DSN"
```

Key flags:

| Flag | Meaning |
| --- | --- |
| `image.tag` | Provider image version (match the chart release). |
| `hub.url` | In-cluster hub address. |
| `hub.insecure` | Skip hub TLS verification (in-cluster, self-signed). |
| `hub.tokenSecretRef.name=""` | No static hub token — auth is via the provider kubeconfig. |
| `catalogEntry.enabled=true` | Init container self-registers the CatalogEntry. |
| `store.driver` | `postgres` or `sqlite` (default). |
| `store.persistence.enabled` | PVC for the SQLite store; set `false` with Postgres. |
| `store.dsn` | Postgres DSN (use `--set-string`, it contains `:`/`?`/`&`). |

For the **default SQLite store**, drop the `store.*` Postgres flags and set
`store.driver=sqlite` with `store.persistence.enabled=true` (the chart mounts a
PVC at `/data`).

### 5. Verify

```bash
kubectl -n railgrid-prod-provider-kuery rollout status deploy/kuery-railgrid-kuery-provider
kubectl -n railgrid-prod-provider-kuery logs deploy/kuery-railgrid-kuery-provider -c provider --tail=50
```

A healthy provider logs `updated endpointslice object` and serves
`GET /healthz`. Connect an edge in a tenant workspace that Enabled the
provider, then open the Kuery portal tab.

## Local development

From the railgrid repo root:

```bash
make build-kuery-provider        # portal build + go build
make run-provider-kuery          # run against a local hub
make install-provider-kuery      # apply manifest.yaml to embedded kcp
```

## Tilt

Both Tiltfiles (embedded-kcp `Tiltfile` and in-cluster `Tiltfile.cluster`)
include a `providers-kuery` group:

1. `kuery` — builds + serves on :8084 (auto-restarts on source change).
2. ▶ `kuery-register` — applies the CatalogEntry. The hub provisions nothing
   from it: the CatalogEntry only *references* the APIExport by name. The
   APIExport, its APIResourceSchemas, the APIExportEndpointSlice and the bind
   grant are all created by the provider's own `init` against the
   workspace-admin kubeconfig from admin onboarding.
3. ▶ `kuery-init` — mints `.kcp/kuery-runtime.kubeconfig` from the
   provider SA token and ensures the APIExportEndpointSlice; Tilt then
   restarts `kuery` with edge engagement enabled.
4. Connect an edge and open the portal — the Kuery tab shows the fleet
   inventory. (▶ `kuery-unregister` tears the CatalogEntry down.)

## Running it yourself

This provider can run in your own cluster instead of on the platform. railgrid
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in: kuery pins no identity hash, so there is none to resolve.
It reaches edges through its own export virtual workspace under the claims each
workspace accepted, so self-hosting kuery usually means self-hosting `edges` as
well — do that first and kuery will reach your own instance. The requirement
kuery declares on `edges.railgrid.ai/kubernetesclusters` is resolved per
workspace against whichever copy is bound there, so nothing about this changes
when the copy is yours.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
