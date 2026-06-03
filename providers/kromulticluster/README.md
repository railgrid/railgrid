# kro-multicluster provider

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
hub /services/providers/kro-multicluster/{api/*, mcp, mcp/sse}
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
kubectl get catalogentry kro-multicluster -o yaml
# status.conditions[Ready].status flips True once heartbeats land.
```

Open the portal at `https://<hub>/ui/providers/kro-multicluster/`.

## Build the image

```sh
docker build -t kedge-kro-multicluster-provider:dev .
```

## Deploy with Helm

```sh
helm install kro-mc deploy/chart \
  -n kro-multicluster --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-kro-multicluster-hub-token \
  --set centralKro.kubeconfigSecretRef.name=central-kro-kubeconfig
```

`values.yaml` has the full configuration surface — image, replicas,
hub URL, the two Secret references (central kro + hub-minted
kedge-provider-kubeconfig), and the toggle for whether the chart
should also render the `CatalogEntry`.

## MCP integration

Add the endpoint to a Claude / Cursor / Cline config separately from
the central kedge MCP aggregator:

```jsonc
{
  "mcpServers": {
    "kedge-kro": {
      "url": "https://<your-kedge-hub>/services/providers/kro-multicluster/mcp",
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
| `KEDGE_PROVIDER_NAME` | `kro-multicluster` | CatalogEntry name |
| `KEDGE_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `KEDGE_PROVIDER_KUBECONFIG` | `/var/run/secrets/kedge/kedge-provider-kubeconfig` | Mounted kcp kubeconfig |
| `KEDGE_TENANT_CREDENTIALS_SECRET` | `cloud-credentials` | Secret name in tenant workspace |
| `KEDGE_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace in tenant workspace |
| `KEDGE_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=` replace `X-Kedge-Tenant` (dev only) |
| `KRO_KUBECONFIG` | (unset → stub mode) | Central kro cluster kubeconfig |
| `KRO_NAMESPACE_PREFIX` | `kedge-tenants-` | Per-tenant namespace prefix |
