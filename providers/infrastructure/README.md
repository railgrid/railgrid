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

## kro runtime (prerequisite)

> If you deploy via the **operator** (recommended, below), it installs and
> lifecycles kro for you — skip this section. The manual steps here are for the
> non-operator paths.

The provider brokers templates from a kro runtime running in
**`kcp-apiexport`** mode:

- The provider creates instance CRs **in the tenant's kcp workspace**, through
  its APIExport `infrastructure.providers.kedge.faros.sh`.
- kro reads the `infrastructure` **APIExportEndpointSlice** in the provider
  workspace (`root:kedge:providers:infrastructure`) to find the APIExport's
  virtual-workspace URL, watches the instance CRs across every bound tenant
  workspace through it, and — with `controller.deployToLocalRuntime=true` —
  materializes each instance's child resources on the cluster kro runs in, while
  the instance object + status stay in the tenant workspace.

With no kro wired up the provider runs in stub mode (three baked-in templates).

### How it's wired — and the ordering

kro and the provider are mutually dependent, so bring-up is a two-step dance:

1. **kro chart installs first, with a _placeholder_ kubeconfig.** The chart
   mounts the `kcp-kubeconfig` Secret (key `kubeconfig`) at
   `/etc/kro/kcp/kubeconfig`; the mount is non-optional, so the Secret must exist
   or the pod never schedules. You seed a stub value — kro starts but stays
   not-Ready until the real credentials arrive.
2. **The provider's `infrastructure init`** (the chart's init container) is what
   makes kro functional. It:
   - creates the APIExport + the `infrastructure` APIExportEndpointSlice in the
     provider workspace ([`install.PlatformAPIExportEndpointSlice`](install/endpointslice.go)) — what kro watches;
   - **overwrites** the `kcp-kubeconfig` Secret in `kro-system` with a real
     kubeconfig pointing at the provider workspace, carrying the runtime SA
     bearer token scoped by the provider's ClusterRole
     ([`install.SeedKroCluster`](install/kroseed.go) — needs `KRO_KUBECONFIG`
     set so init can reach the kro cluster);
   - bounces the kro Deployment so it reloads the new kubeconfig.

> [!IMPORTANT]
> So it is **neither** "provider then kro" **nor** "kro then provider": the kro
> chart goes in first with a placeholder Secret, and the provider's **init** then
> seeds it (creates the endpoint slice, writes the real `kcp-kubeconfig`,
> restarts kro). Until init runs, kro has no VW URL to watch and tenant instances
> go unreconciled. There is no separate "mint a kubeconfig for kro" step — init
> mints and delivers it from the provider's runtime identity.

### Install kro (kcp-apiexport mode)

kro ships its CRDs in the chart. The
[`faroshq/kro-multicluster`](https://github.com/faroshq/kro-multicluster) fork
publishes its image and chart to GHCR. The chart defaults to the upstream image,
so you **must** point `image.repository`/`tag` at the fork or the multicluster
features are missing:

```sh
KRO_VERSION=v0.0.1-mc.7   # latest faroshq/kro-multicluster release tag

# Placeholder kcp credentials so the kro pod can schedule; the provider's
# `infrastructure init` overwrites this Secret with the real kubeconfig and
# restarts kro (see above).
kubectl create namespace kro-system
kubectl -n kro-system create secret generic kcp-kubeconfig \
  --from-literal=kubeconfig=pending-init

helm install kro oci://ghcr.io/faroshq/kro-multicluster/charts/kro/kro \
  --version "$KRO_VERSION" \
  -n kro-system \
  --set image.repository=ghcr.io/faroshq/kro-multicluster/kro \
  --set image.tag="$KRO_VERSION" \
  --set multicluster.enabled=true \
  --set multicluster.provider=kcp-apiexport \
  --set multicluster.kcp.kubeconfigSecret=kcp-kubeconfig \
  --set multicluster.kcp.apiExportEndpointSlice=infrastructure \
  --set controller.deployToLocalRuntime=true
```

Now deploy the provider with bootstrap enabled (see "Deploy with Helm"); its init
container seeds kro as described above. Make sure the provider's init can reach
this kro cluster (`KRO_KUBECONFIG`) so `SeedKroCluster` can write the Secret and
restart kro. Then verify:

```sh
kubectl -n kro-system rollout status deploy/kro
kubectl -n kro-system logs deploy/kro | grep -i apiexport   # should log the discovered VW URL
```

Apply the RGD templates you want to expose, labeled `kedge.faros.sh/expose=true`
(see [docs/credentials.md](docs/credentials.md)).

### Alternative: standalone `kubeconfig` mode (dev only)

For a quick setup with no kcp/APIExport wiring, run kro in `kubeconfig` mode
(`--set multicluster.provider=kubeconfig`, or `--set multicluster.enabled=false`)
against its own cluster. Here kro is installed **first** and the provider talks
to it directly via `centralKro.kubeconfigSecretRef`, using a kubeconfig you mint
from that cluster — create a ServiceAccount, grant it the broker's access
(namespaces, secrets, RGD discovery, and the RGD-defined instance CRs) and a
long-lived token, then assemble a kubeconfig from it:

```sh
KRO_CTX=<your-kro-cluster-context>   # kubectl context for the central kro cluster
SA=kedge-infrastructure-broker
NS=kro-system

# ServiceAccount
kubectl --context "$KRO_CTX" -n "$NS" create serviceaccount "$SA"

# RBAC. namespaces + secrets are core; resourcegraphdefinitions is discovery.
# RGD-generated instance CRDs live in whatever API group each RGD declares, so
# the broker needs to act on those kinds too — scope this down to your RGDs'
# groups in production instead of the broad rule below.
cat <<EOF | kubectl --context "$KRO_CTX" apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kedge-infrastructure-broker
rules:
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["get", "list", "create"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "create", "update", "delete"]
  - apiGroups: ["kro.run"]
    resources: ["resourcegraphdefinitions"]
    verbs: ["get", "list", "watch"]
  # Instance CRs created from RGDs. Replace "*" with your RGDs' actual API
  # groups/resources for least privilege.
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["get", "list", "watch", "create", "update", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kedge-infrastructure-broker
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kedge-infrastructure-broker
subjects:
  - kind: ServiceAccount
    name: $SA
    namespace: $NS
EOF

# Long-lived token Secret (k8s >=1.24 no longer auto-creates SA token secrets).
cat <<EOF | kubectl --context "$KRO_CTX" apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: ${SA}-token
  namespace: $NS
  annotations:
    kubernetes.io/service-account.name: $SA
type: kubernetes.io/service-account-token
EOF

# Assemble the kubeconfig from the SA token + cluster CA + API endpoint.
SERVER="$(kubectl --context "$KRO_CTX" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
CA="$(kubectl --context "$KRO_CTX" -n "$NS" get secret ${SA}-token -o jsonpath='{.data.ca\.crt}')"
TOKEN="$(kubectl --context "$KRO_CTX" -n "$NS" get secret ${SA}-token -o jsonpath='{.data.token}' | base64 -d)"

cat > central-kro.kubeconfig <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: central-kro
    cluster:
      server: ${SERVER}
      certificate-authority-data: ${CA}
contexts:
  - name: central-kro
    context:
      cluster: central-kro
      user: ${SA}
      namespace: ${NS}
current-context: central-kro
users:
  - name: ${SA}
    user:
      token: ${TOKEN}
EOF

# Sanity check the minted kubeconfig.
kubectl --kubeconfig central-kro.kubeconfig get resourcegraphdefinitions
```

> [!NOTE]
> If the central kro API endpoint in the source context is a loopback/proxy
> address (e.g. kind's `https://127.0.0.1:<port>`), rewrite `server:` to an
> address reachable from the provider pod before using the kubeconfig
> in-cluster.

`central-kro.kubeconfig` is the file you feed to the provider — create the
Secret it references (see "Deploy with Helm" → `centralKro.kubeconfigSecretRef`):

```sh
kubectl -n kedge-prod-provider-infrastructure create secret generic central-kro-kubeconfig \
  --from-file=kubeconfig=central-kro.kubeconfig
```

## Deploy as an operator (recommended)

The cleanest way to run the whole stack is the **CRD-driven operator**. You give
it two kubeconfigs (as Secrets) and one `InfrastructureProvider` CR that declares
the kro + provider image versions; the operator does the rest — continuously:

- bootstraps the provider kcp workspace (CRDs, APIExport, CachedResource,
  EndpointSlice, the `infrastructure` APIExportEndpointSlice, schemas, Templates);
- **lifecycles the kro Helm release** via the helm CLI (our multicluster fork
  chart + image, `kcp-apiexport` mode), and seeds kro's `kcp-kubeconfig`;
- owns the **provider serve Deployment** (image/replicas/port from the CR).

It is the same `infrastructure-provider` binary (`controller` subcommand); the
runtime image bundles the `helm` CLI so the operator pod can drive kro.

### Install

The chart installs the operator + the `InfrastructureProvider` CRD + RBAC + the
two kubeconfig Secrets + one CR, all from values, when `operator.enabled=true`:

```sh
helm install infrastructure \
  oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider --version <X.Y.Z> \
  -n kedge-infra-operator --create-namespace \
  --set operator.enabled=true \
  --set operator.providerWorkspace=root:kedge:providers:infrastructure \
  --set-file operator.providerKubeconfig=./provider-infrastructure.kubeconfig \
  --set-file operator.runtimeKubeconfig=./runtime-cluster.kubeconfig \
  --set operator.kro.version=v0.0.1-mc.7
```

- `operator.providerKubeconfig` — the kcp provider kubeconfig (admin-portal
  issued / workspace-scoped). Or reference an existing Secret via
  `operator.providerKubeconfigSecret.name` and omit the inline value.
- `operator.runtimeKubeconfig` — the cluster where kro + the provider serve run.
- `operator.kro.*` — chart/version/image of the kro release (defaults to the
  multicluster fork: `oci://ghcr.io/faroshq/kro-multicluster/charts/kro/kro` +
  `ghcr.io/faroshq/kro-multicluster/kro`). Bump `operator.kro.version` (or edit
  the CR) to upgrade kro.
- `operator.provider.image.*` — the provider serve image (defaults to the chart
  image/appVersion).

Upgrades are a values/CR edit: change `operator.kro.version` or
`operator.provider.image.tag` and the operator reconciles the new versions.

### Image + chart publishing

[`.github/workflows/provider-release.yaml`](../../.github/workflows/provider-release.yaml)
is the sole publisher: an `infrastructure/vX.Y.Z` tag builds + pushes the
provider image (operator binary **and** the helm CLI baked in) and packages +
pushes the chart to `oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider`.
(`images.yaml` only build-validates the image on PRs; it does not publish.)

## Deploy with Helm (init-container bootstrap)

This is the non-operator path: a single provider Deployment that self-bootstraps
via an init container. The provider needs a runtime kubeconfig to reach kcp,
mounted as the `kedge-provider-kubeconfig` Secret. You get that kubeconfig by
onboarding the provider in the kedge **admin portal**, then create the Secret
from it and deploy.

### 1. Onboard the provider in the admin portal

Onboard the infrastructure provider in the kedge admin portal. It provisions the
provider workspace and issues a **provider kubeconfig** scoped to that workspace.
Download it (e.g. `provider-infrastructure.kubeconfig`).

### 2. Create the Secret from the download

The Secret name must be `kedge-provider-kubeconfig` and the key must be
`kubeconfig` (the chart defaults — `providerKubeconfig.secretName`):

```sh
kubectl create namespace infrastructure

kubectl -n infrastructure create secret generic kedge-provider-kubeconfig \
  --from-file=kubeconfig=provider-infrastructure.kubeconfig
```

(If the portal hands you a ready-made Secret manifest instead of a raw
kubeconfig, `kubectl apply` it into the `infrastructure` namespace — just make
sure the name/key match the above.)

### 3. Deploy the chart

```sh
helm install infrastructure deploy/chart \
  -n infrastructure --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-infrastructure-hub-token \
  --set bootstrap.enabled=true
```

With `bootstrap.enabled=true`, an init container runs `infrastructure init`
— installing the CRDs / CachedResource / APIExport (and the `infrastructure`
APIExportEndpointSlice kro watches) into the provider workspace. The serve
container then reuses the same kubeconfig. The init/serve volume is **not**
`optional`, so the pod waits in `ContainerCreating` until the
`kedge-provider-kubeconfig` Secret exists — create it (step 2) before or shortly
after deploying.

### Alternative: `supplied` — fully standalone, no hub

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

Trade-off: with `bootstrap.enabled=true` the serve container runs with
cluster-admin-in-workspace (the bootstrap kubeconfig) rather than a narrow scoped
SA. The init container re-runs on every pod (re)start; every step is idempotent,
so that's safe.

`values.yaml` has the full configuration surface — image, replicas,
hub URL, the Secret references (central kro + runtime
kedge-provider-kubeconfig), the `bootstrap.*` block, the `operator.*` block
(operator mode above), and the toggle for whether the chart should also render
the `CatalogEntry`.

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
