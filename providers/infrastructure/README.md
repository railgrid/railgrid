# infrastructure provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-infrastructure`](https://github.com/faroshq/provider-infrastructure)
> repository is **automatically synced** from the kedge monorepo
> [`faroshq/kedge`](https://github.com/faroshq/kedge) (path `providers/infrastructure/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/kedge`](https://github.com/faroshq/kedge) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

A kedge provider that brokers application templates from a central
[kro](https://github.com/faroshq/kro-multicluster) (Kube Resource
Orchestrator) cluster into kedge tenant workspaces. A tenant picks a
template in the kedge portal — or asks an MCP-driven LLM — supplies
inputs, and this provider creates the kro instance CR on their behalf
using cloud credentials pulled from the tenant's own kcp workspace.

Full design + staged plan:
[/Users/mjudeikis/.claude/plans/zippy-baking-jellyfish.md](../../../../../../.claude/plans/zippy-baking-jellyfish.md)

## What's here

| Surface | Where |
|---|---|
| HTTP REST | `server/` — `/api/templates`, `/api/instances` |
| MCP transport | `mcpserver/` — `/mcp`, `/mcp/sse` (6 `kro_*` tools) |
| Central kro client | `kro/` — `ResourceGraphDefinition` discovery + instance lifecycle |
| Tenant kcp client | `tenant/` — per-tenant `cloud-credentials` Secret resolution |
| Portal micro-frontend | `portal/` — Vue 3 catalog + dynamic provision form + instance list |
| Helm chart | `deploy/chart/` — provider Deployment + CatalogEntry |
| Per-cloud credential convention | [docs/credentials.md](docs/credentials.md) |

The CatalogEntry ships with `apiExport.schemas: []` (pure broker, no
CRDs leak into tenant workspaces). The single `permissionClaim` is
`secrets get/list/watch` with `tenantScoped: true` so the provider
can read `cloud-credentials` after a tenant Enables it.

## Architecture

```
Browser / MCP client
   │  bearer
   ▼
hub /services/providers/infrastructure/{api/*, mcp, mcp/sse}
   │  proxy injects X-Kedge-Tenant + X-Kedge-User
   │  (pkg/hub/providers/proxy.go SetTenantResolver +
   │   pkg/hub/provider_tenant_resolver.go)
   ▼
this provider pod
   │
   ├── tenant kcp client ── /var/run/secrets/kedge/kedge-provider-kubeconfig
   │     resolves cloud-credentials Secret in tenant workspace
   │
   └── central kro client ── /var/run/secrets/kro/kubeconfig
         discovers RGDs, creates/lists/deletes instances in
         per-tenant namespace kedge-tenants-<hash>
```

## Run locally (stub mode — no central kro needed)

```sh
# 1. Build the portal bundle.
npm --prefix portal install
npm --prefix portal run build

# 2. Run the provider. With KRO_KUBECONFIG unset, kro/stub.go serves
#    three baked-in templates so the UI is demoable without infra.
go run .
# → listening on :8081 (kro=*kro.stubClient tenant=false mcp=true)

# 3. Smoke tests:
curl -s localhost:8081/healthz
curl -s localhost:8081/api/templates | jq '.items[].name'
curl -s localhost:8081/api/templates/postgres | jq '.template.inputsSchema'

# 4. Provision flow (dev mode lets ?tenant= replace the X-Kedge-Tenant header).
export KEDGE_DEV_ALLOW_TENANT_QUERY=true
curl -s -X POST 'localhost:8081/api/instances?tenant=dev&user=alice' \
  -H 'Content-Type: application/json' \
  -d '{"templateName":"postgres","name":"foo","values":{"name":"foo","size":"medium"}}'
curl -s 'localhost:8081/api/instances?tenant=dev' | jq
curl -s -X DELETE 'localhost:8081/api/instances/foo?tenant=dev' -i

# 5. MCP tools/list (note: SSE response — pipe through `tail`).
curl -s -X POST -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  localhost:8081/mcp | head
```

## Run against a real central kro cluster

Point `KRO_KUBECONFIG` at the central cluster's kubeconfig:

```sh
KRO_KUBECONFIG=/path/to/kro-kubeconfig \
KEDGE_HUB_URL=https://localhost:9443 \
KEDGE_HUB_TOKEN=test \
KEDGE_HUB_INSECURE=true \
go run .
```

For the catalog to show real templates, the central kro cluster must
have RGDs labeled `kedge.faros.sh/expose=true`. See
[docs/credentials.md](docs/credentials.md) for the labeling /
annotation contract.

## Register with the hub

```sh
kubectl --kubeconfig kcp-admin.kubeconfig \
  --context kedge-admin \
  ws use root:kedge:providers
kubectl apply -f manifest.yaml
kubectl get catalogentry infrastructure -o yaml
# status.conditions[Ready].status flips True once heartbeats land.
```

Open the portal at `https://<hub>/ui/providers/infrastructure/`.

## Build the image

```sh
docker build -t kedge-infrastructure-provider:dev .
```

## Deploy with Helm

There are two ways the provider gets its runtime kubeconfig (the
`kedge-provider-kubeconfig` Secret it mounts to reach kcp).

### A. Hub-provisioned (default)

The hub catalog controller mints the runtime kubeconfig when it
reconciles the `CatalogEntry`. The chart just deploys the workload:

```sh
helm install infrastructure deploy/chart \
  -n infrastructure --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-infrastructure-hub-token \
  --set centralKro.kubeconfigSecretRef.name=central-kro-kubeconfig
```

### B. Self-bootstrap with an init container (`bootstrap.enabled=true`)

An init container runs `infrastructure init` before the serve container,
installing the CRDs / CachedResource / APIExport into the provider
workspace. **Both containers share one kubeconfig** — no separately
minted runtime token. Where that kubeconfig comes from is set by
`bootstrap.kubeconfigSource`:

#### `hubMinted` (default) — the platform mints, the provider consumes

This is the recommended split. A **platform admin** applies the
`CatalogEntry`; the hub creates the provider workspace, mints a
kubeconfig that is **cluster-admin within that workspace**, and writes it
as the `kedge-provider-kubeconfig` Secret (the hub must run with
`--kubeconfig` so its `HostSecretWriter` can write into this cluster).
The **provider owner** just deploys the chart:

```sh
helm install infrastructure deploy/chart \
  -n infrastructure --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-infrastructure-hub-token \
  --set bootstrap.enabled=true            # kubeconfigSource=hubMinted is the default
```

The init/serve volume is **not** `optional` — the pod waits in
`ContainerCreating` until the hub delivers `kedge-provider-kubeconfig`,
which is exactly the bootstrap ordering we want. No `kubectl ws create`,
no separate admin kubeconfig: the hub already created the workspace when
it reconciled the CatalogEntry.

#### `supplied` — fully standalone, no hub

Install into any cluster with only a kcp kubeconfig (no hub provisioning):

```sh
helm install infrastructure deploy/chart -n infrastructure --create-namespace \
  --set bootstrap.enabled=true \
  --set bootstrap.kubeconfigSource=supplied \
  --set bootstrap.workspacePath=root:kedge:providers:infrastructure \
  --set-file bootstrap.kcpKubeconfig=./provider-workspace-admin.kubeconfig
```

Here you own the prerequisites: the kubeconfig must be admin of
`bootstrap.workspacePath`, and that workspace must already exist
(`kubectl ws create`). Prefer `bootstrap.kcpKubeconfigSecretRef` to an
inline kubeconfig in production.

Trade-off (both sources): the serve container runs with
cluster-admin-in-workspace rather than a narrow scoped SA. For
least-privilege, use the hub-provisioned model (A) with a manual init.
The init container re-runs on every pod (re)start; every step is
idempotent, so that's safe.

`values.yaml` has the full configuration surface — image, replicas,
hub URL, the Secret references (central kro + runtime
kedge-provider-kubeconfig), the `bootstrap.*` block, and the toggle for
whether the chart should also render the `CatalogEntry`.

## MCP integration

Add the endpoint to a Claude / Cursor / Cline config separately from
the central kedge MCP aggregator:

```jsonc
{
  "mcpServers": {
    "kedge-kro": {
      "url": "https://<your-kedge-hub>/services/providers/infrastructure/mcp",
      "headers": { "Authorization": "Bearer <kedge-bearer>" }
    }
  }
}
```

The MCP server exposes six tools: `kro_list_templates`,
`kro_describe_template`, `kro_provision`, `kro_list_instances`,
`kro_get_instance`, `kro_delete_instance`. Identity (tenant + user) is
taken from the same bearer token the kedge portal uses — the model
never needs to ask the user for a tenant path.

External providers cannot plug into the in-tree aggregator at
[providers/mcp/aggregate/](../mcp/aggregate/) (init()-only registration).
This provider therefore runs a standalone MCP server alongside the
central one.

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8081` | Listen port |
| `KEDGE_HUB_URL` | (unset → heartbeat off) | Hub base URL for heartbeats |
| `KEDGE_HUB_TOKEN` | (unset) | Bearer token for heartbeats |
| `KEDGE_PROVIDER_NAME` | `infrastructure` | CatalogEntry name |
| `KEDGE_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `KEDGE_PROVIDER_KUBECONFIG` | `/var/run/secrets/kedge/kedge-provider-kubeconfig` | Mounted kcp kubeconfig |
| `KEDGE_TENANT_CREDENTIALS_SECRET` | `cloud-credentials` | Secret name in tenant workspace |
| `KEDGE_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace in tenant workspace |
| `KEDGE_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=` replace `X-Kedge-Tenant` (dev only) |
| `KRO_KUBECONFIG` | (unset → stub mode) | Central kro cluster kubeconfig |
| `KRO_NAMESPACE_PREFIX` | `kedge-tenants-` | Per-tenant namespace prefix |

### `init` subcommand (bootstrap) env vars

| Var | Default | Purpose |
|---|---|---|
| `INFRASTRUCTURE_ADMIN_KUBECONFIG` | (falls back to `KUBECONFIG`, then in-cluster) | kcp **admin** kubeconfig for the bootstrap |
| `INFRASTRUCTURE_WORKSPACE_PATH` | (unset) | Retarget the admin kubeconfig at `/clusters/<path>` (the provider workspace) |
| `INFRASTRUCTURE_KUBECONFIG` | `./infrastructure.kubeconfig` | Path the minted runtime kubeconfig is written to (file) |
| `INFRASTRUCTURE_RUNTIME_KUBECONFIG_SECRET` | (unset) | When set, also write the runtime kubeconfig into this host-cluster Secret |
| `INFRASTRUCTURE_RUNTIME_KUBECONFIG_NAMESPACE` | (`POD_NAMESPACE`, then `default`) | Namespace for the runtime Secret |
| `POD_NAMESPACE` | (unset) | Downward-API pod namespace; used when the namespace var above is unset |
| `HOST_KUBECONFIG` | (unset → in-cluster) | Out-of-cluster override for the host client that writes the runtime Secret |
