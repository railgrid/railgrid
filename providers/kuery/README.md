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
  workspace), and syncs each connected kubernetes edge through the hub's
  edges-proxy as the workspace-local `railgrid-kuery` ServiceAccount the
  controller provisions there (the `railgrid-kuery-edgeproxy` grant gives it
  verb `proxy` on kubernetesclusters). Engaged clusters are keyed
  `{clusterID}/{edgeName}` and labelled with their tenant, where the tenant
  key is the tenant workspace's **kcp logical-cluster ID** (read from the
  kuery `APIBinding`'s `kcp.io/cluster` annotation) — never a workspace
  path. Each engaged edge gets an `Engagement` record in kuery's own
  workspace (`apis/v1alpha1`, installed as a provider-private CRD by `init`,
  deliberately NOT exported): spec is the cluster ID and the edge name,
  status is the owning replica, its last heartbeat and a phase. That record
  is the authority for "which edges may this caller query"; the SQL index
  holds only synced objects and is rebuildable from it.
- **The query verb** (`queryapi/`):
  `POST /dataplane/clusters/{clusterID}/savedviews/{name}/run`, the provider's
  ONE tenant route. Two gates run as the caller before the engine is touched
  (`provider-sdk/dataplane`): a real GET of the SavedView in the path's
  cluster, then a SelfSubjectAccessReview for `create` on `savedviews/run`
  scoped to that name. Only then is the query scoped to the caller's
  Engagements. The body may carry `{"input":{"query":…}}` to override the
  view's saved query. Results report `objects[].cluster` as
  `{clusterID}/{edge}`.
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
  `edgeProxyAccess`), Helm chart. The APIExport claims **no** first-party
  (`*.railgrid.ai`) resources — there is no `edges` claim. Such a claim would
  have to pin one serving APIExport identity for every consuming workspace at
  once, which breaks as soon as one org self-hosts `edges`. Edge discovery
  acts as a per-workspace ServiceAccount through each workspace's own `edges`
  binding instead (`init_cmd.go`, `engagement/`,
  `provider-sdk/tenantaccess`).

What lands next (see the design doc): an e2e suite asserting edge-object
sync end to end with a real connected agent, and the Postgres chart option.

## Layout

```
main.go             serve loop: /healthz, /readyz, the query verb, /mcp, portal, heartbeat
apis/v1alpha1/      SavedView (exported) + Engagement (provider-private)
controller/savedview/  validates spec.query, stamps Ready
install/            applies the private Engagement CRD into the provider workspace
index/              store naming + the tenant label both halves agree on
core/               embedded kuery wiring (store, engine, sync)
engagement/         edge watch → Engage/Disengage, Engagements, per-edge Leases
queryapi/           the query verb: gates, engagement scoping, QuerySpec validation
mcpserver/          kuery_query + kuery_impact, through the same gated executor
assets.go           //go:embed of portal/dist
manifest.yaml       CatalogEntry (SavedView schema, edgeProxyAccess; no first-party claims)
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

Nothing to fill in: kuery claims no first-party resources, so there is no
identity hash to resolve. It reads through edges as a per-workspace
ServiceAccount over each workspace's own `edges` binding, so self-hosting
kuery usually means self-hosting `edges` as well — do that first and kuery
will reach your own instance.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
