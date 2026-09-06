# infrastructure provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-infrastructure`](https://github.com/faroshq/provider-infrastructure)
> repository is **automatically synced** from the faros monorepo
> [`faroshq/faros`](https://github.com/faroshq/faros) (path `providers/infrastructure/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/faros`](https://github.com/faroshq/faros) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

A faros provider that brokers application templates from a central
[kro](https://github.com/kro-run/kro) (Kube Resource
Orchestrator) cluster into faros tenant workspaces. A tenant picks a
template in the faros portal — or asks an MCP-driven LLM — supplies
inputs, and this provider creates the kro instance CR on their behalf
using cloud credentials pulled from the tenant's own kcp workspace.

## Deploy (operator)

The recommended way to run the whole stack is the **CRD-driven operator**. You
give it a provider (kcp) kubeconfig and one `InfrastructureProvider` CR that
declares the kro + provider image versions; the operator does the rest —
continuously:

- bootstraps the provider kcp workspace (CRDs, APIExport, CachedResource,
  EndpointSlice, the `infrastructure` APIExportEndpointSlice, schemas, Templates);
- **lifecycles the kro Helm release** via the helm CLI (upstream kro,
  single-cluster; chart CRDs applied explicitly so version bumps carry them);
- owns the **provider serve Deployment** (image/replicas/port from the CR).

It is the same `infrastructure-provider` binary (`controller` subcommand); the
runtime image bundles the `helm` CLI so the operator pod can drive kro. The
chart binds the operator's ServiceAccount to an enumerated ClusterRole that
covers helm-installing kro (its CRDs, ClusterRole and binding, workload) and
the serve rollout, and ships a second enumerated role the operator binds the
serve ServiceAccount to for the in-cluster runtime. `operator.clusterAdmin`
(default off) additionally binds `cluster-admin` for setups whose kro chart or
Templates need kinds outside that set.

### Prerequisites

- The provider **workspace must already exist** — onboard/register the provider
  so `root:faros:providers:infrastructure` exists.
- A **provider (kcp) kubeconfig** scoped to that workspace (what the admin
  portal issues).

### Install — single cluster (recommended)

When the operator runs in the cluster where you want kro + the provider serve to
live, you only need the provider kubeconfig. Omit the runtime kubeconfig and the
operator uses its **own (in-cluster) cluster** as the runtime.

```sh
helm install infrastructure \
  oci://ghcr.io/faroshq/charts/faros-infrastructure-provider --version <X.Y.Z> \
  -n faros-infra-operator --create-namespace \
  --set operator.enabled=true \
  --set operator.providerWorkspace=root:faros:providers:infrastructure \
  --set-file operator.providerKubeconfig=./provider-infrastructure.kubeconfig \
  --set operator.kro.version=v0.0.1-mc.7 \
  --set hub.url=https://faros-hub.faros.svc.cluster.local:9443
```

### Install — separate runtime cluster

To run kro + serve in a different cluster, also pass its kubeconfig:

```sh
helm install infrastructure \
  oci://ghcr.io/faroshq/charts/faros-infrastructure-provider --version <X.Y.Z> \
  -n faros-infra-operator --create-namespace \
  --set operator.enabled=true \
  --set operator.providerWorkspace=root:faros:providers:infrastructure \
  --set-file operator.providerKubeconfig=./provider-infrastructure.kubeconfig \
  --set-file operator.runtimeKubeconfig=./runtime-cluster.kubeconfig \
  --set operator.kro.version=v0.0.1-mc.7
```

Values:

- `operator.providerKubeconfig` — the kcp provider kubeconfig. Or reference an
  existing Secret via `operator.providerKubeconfigSecret.name` and omit the
  inline value.
- `operator.runtimeKubeconfig` — **optional**; omit for the in-cluster runtime.
- `operator.kro.*` — chart/version/image of the kro release (defaults to
  upstream: `oci://registry.k8s.io/kro/charts/kro`, image from the chart's
  own defaults).
- `operator.provider.image.*` — the provider serve image (defaults to the chart
  image/appVersion).
- `operator.application.*` — the `application` template's exposure layer:
  `baseDomain` (the zone apps are served under; **required to enable app
  exposure**) and `gateway.name` / `gateway.namespace` (the Gateway API parent
  the generated HTTPRoutes attach to; default `cloudflare-tunnel` /
  `cfgate-system`). These render into the CR's `spec.application` and become the
  serve container's `FAROS_APP_BASE_DOMAIN` / `FAROS_GATEWAY_NAME` /
  `FAROS_GATEWAY_NAMESPACE`. See
  [docs/application-template-architecture.md](docs/application-template-architecture.md).

### Verify

```sh
kubectl -n faros-infra-operator get infrastructureprovider infrastructure -o wide
# PHASE → Ready; conditions Bootstrapped / KroReleased / ProviderDeployed = True
kubectl -n faros-infra-operator logs deploy/infrastructure-faros-infrastructure-provider-operator
kubectl -n kro-system get deploy kro
kubectl -n faros-infrastructure-provider get deploy,svc
```

### Upgrade

Image versions live in the CR/values — bump and re-reconcile:

```sh
helm upgrade infrastructure oci://ghcr.io/faroshq/charts/faros-infrastructure-provider --version <X.Y.Z> \
  -n faros-infra-operator --reuse-values \
  --set operator.kro.version=<new-kro> \
  --set operator.provider.image.tag=<new-provider>
```

### Image + chart publishing

[`.github/workflows/provider-release.yaml`](../../.github/workflows/provider-release.yaml)
is the sole publisher: an `infrastructure/vX.Y.Z` tag builds + pushes the
provider image (operator binary **and** the helm CLI baked in) and packages +
pushes the chart to `oci://ghcr.io/faroshq/charts/faros-infrastructure-provider`.
(`images.yaml` only build-validates the image on PRs; it does not publish.) Until
a release tag is cut, install from the local chart path
(`providers/infrastructure/deploy/chart`) with a provider image that contains the
helm CLI.

## What's here

| Surface | Where |
|---|---|
| HTTP server | `server/` — `/healthz`, portal SPA, `/mcp` |
| MCP transport | `mcpserver/` — `/mcp`, `/mcp/sse` (6 `kro_*` tools) |
| Central kro client | `kro/` — `ResourceGraphDefinition` discovery + instance lifecycle |
| Tenant kcp client | `tenant/` — per-tenant `cloud-credentials` Secret resolution |
| Portal micro-frontend | `portal/` — Vue 3 catalog + dynamic provision form + instance list |
| Operator | `operator/` + `apis/v1alpha1` — `InfrastructureProvider` CRD + reconciler |
| Helm chart | `deploy/chart/` — operator + provider Deployment + CatalogEntry |
| Per-cloud credential convention | [docs/credentials.md](docs/credentials.md) |
| Template-defined instance rendering | [docs/instance-views.md](docs/instance-views.md) |

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
   │  proxy injects X-Faros-Tenant + X-Faros-User
   │  (pkg/hub/providers/proxy.go SetTenantResolver +
   │   pkg/hub/provider_tenant_resolver.go)
   ▼
this provider pod
   │
   ├── tenant kcp client ── /var/run/secrets/faros/faros-provider-kubeconfig
   │     resolves cloud-credentials Secret in tenant workspace
   │
   └── central kro client ── /var/run/secrets/kro/kubeconfig
         discovers RGDs, creates/lists/deletes instances in
         per-tenant namespace faros-tenants-<hash>
```

kro runs in **`kcp-apiexport`** mode: the provider creates instance CRs in the
tenant's kcp workspace through its APIExport
`infrastructure.providers.faros.sh`; kro reads the `infrastructure`
APIExportEndpointSlice in the provider workspace to find the virtual-workspace
URL, watches instance CRs across every bound tenant workspace, and — with
`controller.deployToLocalRuntime=true` — materializes each instance's child
resources on the cluster kro runs in, while the instance object + status stay in
the tenant workspace.

**This provider is the sole owner of the runtime cluster.** The runtime
kubeconfig (`/var/run/secrets/kro/kubeconfig`), the kro RGDs, and the
workloads' internal Services are its private backend layer — no other
provider holds a credential into them. Consumers (e.g. App Studio) operate
infrastructure-owned workloads only through the instance CRs (control plane)
and their VW subresources (data plane: `sandboxrunners/{name}/{log,proxy,…}`),
as the tenant user. See the platform
[provider-isolation rule](../../docs/providers.md#provider-isolation-the-cross-provider-boundary)
and [`app-studio-runtime-decoupling.md`](../../docs/app-studio-runtime-decoupling.md).

## MCP integration

Add the endpoint to a Claude / Cursor / Cline config separately from
the central faros MCP aggregator:

```jsonc
{
  "mcpServers": {
    "faros-kro": {
      "url": "https://<your-faros-hub>/services/providers/infrastructure/mcp",
      "headers": { "Authorization": "Bearer <faros-bearer>" }
    }
  }
}
```

The MCP server exposes six tools: `kro_list_templates`,
`kro_describe_template`, `kro_provision`, `kro_list_instances`,
`kro_get_instance`, `kro_delete_instance`. Identity (tenant + user) is
taken from the same bearer token the faros portal uses — the model
never needs to ask the user for a tenant path.

External providers cannot plug into the in-tree aggregator at
[providers/mcp/aggregate/](../mcp/aggregate/) (init()-only registration).
This provider therefore runs a standalone MCP server alongside the
central one.

## Universal coding sandbox

The platform-owned `universal-coding-sandbox` Template is disabled by default:
it is neither seeded nor admitted until `FAROS_CODING_SANDBOX_ENABLED=true` is
set by the operator. Enabling it also requires
`FAROS_DEV_IMAGE_UNIVERSAL` to be a complete immutable
`name@sha256:<64 lowercase hex digits>` reference, and
`FAROS_DEV_AGENT_IMAGE` must pin the injected/bootstrap `faros-dev-agent` image
to the same immutable form. The shipped image recipe is
`dev-agent/Dockerfile.universal`; it combines Node with Go and Python plus the
bounded `faros-dev-agent` workspace/exec data plane.

The sandbox is private (no hostname or HTTPRoute), uses a persistent workspace,
and enforces the 12-hour idle and hard lifetime bounds. Hosted installations
keep the feature disabled; BYO chart self-hosting values explicitly opt in and
must provide immutable universal and dev-agent image references.

Every synthesized development pod, the coding sandbox included, runs
PSS-restricted (non-root UID 1000, seccomp `RuntimeDefault`, all capabilities
dropped, no privilege escalation) but still shares the host kernel. Set
`FAROS_SANDBOX_RUNTIME_CLASS_NAME` (chart value `sandbox.runtimeClassName`, CR
field `spec.sandbox.runtimeClassName`) to the name of a hardened RuntimeClass
installed on the runtime cluster, `gvisor` or `kata`, before exposing App
Studio to untrusted users. Empty keeps the cluster default runtime.

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8081` | Listen port |
| `FAROS_HUB_URL` | (unset → heartbeat off) | Hub base URL for heartbeats |
| `FAROS_HUB_TOKEN` | (unset) | Bearer token for heartbeats |
| `FAROS_PROVIDER_NAME` | `infrastructure` | CatalogEntry name |
| `FAROS_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `FAROS_PROVIDER_KUBECONFIG` | `/var/run/secrets/faros/faros-provider-kubeconfig` | Mounted kcp kubeconfig |
| `FAROS_TENANT_CREDENTIALS_SECRET` | `cloud-credentials` | Secret name in tenant workspace |
| `FAROS_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace in tenant workspace |
| `FAROS_CODING_SANDBOX_ENABLED` | `false` | Opts into seeding/admitting the platform-owned universal coding sandbox; enabled deployments require immutable universal and dev-agent images |
| `FAROS_DEV_IMAGE_UNIVERSAL` | `ghcr.io/faroshq/faros-universal-dev:latest` | Platform-selected Node/Go/Python image token; the coding sandbox gate accepts only a digest-pinned override |
| `FAROS_DEV_AGENT_IMAGE` | `ghcr.io/faroshq/faros-dev-agent:latest` | Platform-selected injector and control-token bootstrap image; the coding sandbox gate accepts only a digest-pinned override |
| `FAROS_SANDBOX_RUNTIME_CLASS_NAME` | (unset → cluster default runtime) | RuntimeClass (`gvisor` or `kata`) stamped on every synthesized development pod, including the universal coding sandbox; required before serving untrusted users |
| `FAROS_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=` replace `X-Faros-Tenant` (dev only) |
| `KRO_KUBECONFIG` | (unset → stub mode) | Central kro cluster kubeconfig |
| `KRO_NAMESPACE_PREFIX` | `faros-tenants-` | Per-tenant namespace prefix |

---

# Development

Everything below is for working on the provider locally or wiring it up by hand
(without the operator). For deploying, use the operator section above.

## Run locally (stub mode — no central kro needed)

```sh
# 1. Build the portal bundle.
npm --prefix portal install
npm --prefix portal run build

# 2. Run the provider. With KRO_KUBECONFIG unset, kro/stub.go serves
#    three baked-in templates so the UI is demoable without infra.
go run .
# → listening on :8081 (kro=*kro.stubClient tenant=false mcp=true)

# 3. Smoke test: liveness.
curl -s localhost:8081/healthz

# 4. MCP tools/list (note: SSE response — pipe through `head`). Templates
#    and instances are NOT served as REST — they are kro_* MCP tools and,
#    in a real cluster, CRDs read/written directly against kcp.
curl -s -X POST -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  localhost:8081/mcp | head
```

## Run against a real central kro cluster

Point `KRO_KUBECONFIG` at the central cluster's kubeconfig:

```sh
KRO_KUBECONFIG=/path/to/kro-kubeconfig \
FAROS_HUB_URL=https://localhost:9443 \
FAROS_HUB_TOKEN=test \
FAROS_HUB_INSECURE=true \
go run .
```

For the catalog to show real templates, the central kro cluster must
have RGDs labeled `faros.sh/expose=true`. See
[docs/credentials.md](docs/credentials.md) for the labeling /
annotation contract.

## Register with the hub

```sh
kubectl --kubeconfig kcp-admin.kubeconfig \
  --context faros-admin \
  ws use root:faros:providers
kubectl apply -f manifest.yaml
kubectl get catalogentry infrastructure -o yaml
# status.conditions[Ready].status flips True once heartbeats land.
```

Open the portal at `https://<hub>/ui/providers/infrastructure/`.

## Build the image

```sh
docker build -t faros-infrastructure-provider:dev .
```

## Manual kro install (without the operator)

The operator installs and lifecycles kro for you. To wire it by hand (e.g. for
the init-container bootstrap deploy below), install **upstream kro,
single-cluster**: tenants author the flattened `Instance` kind in kcp and the
provider's instance controller materializes the per-template kro CRs on the
runtime cluster, so kro never talks to kcp — no kcp kubeconfig, no
multicluster values, no ordering dance.

```sh
KRO_VERSION=0.9.3   # upstream release (must contain the SSA-finalizer deletion fix, ≥0.9.x)

# helm only installs crds/-dir CRDs on FIRST install; apply them explicitly
# so version bumps carry CRD schema changes too.
helm show crds oci://registry.k8s.io/kro/charts/kro --version "$KRO_VERSION" | kubectl apply -f -

helm install kro oci://registry.k8s.io/kro/charts/kro \
  --version "$KRO_VERSION" \
  -n kro-system --create-namespace
```

Verify:

```sh
kubectl -n kro-system rollout status deploy/kro
```

The provider's `infrastructure init` (or the operator's bootstrap) then seeds
Templates in kcp; the Template controller authors one RGD per template on this
cluster and the instance controller materializes tenant Instances into it.

## Deploy with Helm (init-container bootstrap, non-operator)

A single provider Deployment that self-bootstraps via an init container — the
pre-operator path. The provider needs a runtime kubeconfig to reach kcp, mounted
as the `faros-provider-kubeconfig` Secret. Onboard the provider in the faros
**admin portal**, download the issued kubeconfig, create the Secret, then deploy.

### 1. Create the Secret from the download

The Secret name must be `faros-provider-kubeconfig` and the key must be
`kubeconfig` (the chart defaults — `providerKubeconfig.secretName`):

```sh
kubectl create namespace infrastructure
kubectl -n infrastructure create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=provider-infrastructure.kubeconfig
```

### 2. Deploy the chart

```sh
helm install infrastructure deploy/chart \
  -n infrastructure --create-namespace \
  --set hub.url=https://faros-hub.faros.svc.cluster.local:9443 \
  --set bootstrap.enabled=true
```

With `bootstrap.enabled=true`, an init container runs `infrastructure init`
— installing the CRDs / CachedResource / APIExport (and the `infrastructure`
APIExportEndpointSlice kro watches) into the provider workspace. The serve
container then reuses the same kubeconfig. The init/serve volume is **not**
`optional`, so the pod waits in `ContainerCreating` until the
`faros-provider-kubeconfig` Secret exists.

### Alternative: `supplied` — fully standalone, no hub

```sh
helm install infrastructure deploy/chart -n infrastructure --create-namespace \
  --set bootstrap.enabled=true \
  --set bootstrap.kubeconfigSource=supplied \
  --set bootstrap.workspacePath=root:faros:providers:infrastructure \
  --set-file bootstrap.kcpKubeconfig=./provider-workspace-admin.kubeconfig
```

The kubeconfig must be admin of `bootstrap.workspacePath`, and that workspace
must already exist. Prefer `bootstrap.kcpKubeconfigSecretRef` to an inline
kubeconfig in production.

`values.yaml` has the full configuration surface — image, replicas, hub URL, the
Secret references, the `bootstrap.*` block, the `operator.*` block, and the
toggle for whether the chart renders the `CatalogEntry`.

## `init` subcommand (bootstrap) env vars

| Var | Default | Purpose |
|---|---|---|
| `INFRASTRUCTURE_ADMIN_KUBECONFIG` | (falls back to `KUBECONFIG`, then in-cluster) | kcp **admin** kubeconfig for the bootstrap |
| `INFRASTRUCTURE_WORKSPACE_PATH` | (unset) | Retarget the admin kubeconfig at `/clusters/<path>` (the provider workspace) |
| `INFRASTRUCTURE_KUBECONFIG` | `./infrastructure.kubeconfig` | Path the minted runtime kubeconfig is written to (file) |
| `INFRASTRUCTURE_RUNTIME_KUBECONFIG_SECRET` | (unset) | When set, also write the runtime kubeconfig into this host-cluster Secret |
| `INFRASTRUCTURE_RUNTIME_KUBECONFIG_NAMESPACE` | (`POD_NAMESPACE`, then `default`) | Namespace for the runtime Secret |
| `POD_NAMESPACE` | (unset) | Downward-API pod namespace; used when the namespace var above is unset |
| `HOST_KUBECONFIG` | (unset → in-cluster) | Out-of-cluster override for the host client that writes the runtime Secret |

## Running it yourself

This provider can run in your own cluster instead of on the platform. faros
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in: the generated command turns on **operator mode** — the same
mode the production install runs in — and substitutes the kubeconfig Secret
reference. The operator bootstraps the workspace, installs kro, and owns the
serve Deployment plus the runtime-cluster RBAC it needs, so a self-hosted copy
stands up unattended in a cluster that has nothing but the credential.

The `bootstrap.*` init-container flow documented above is superseded and is
*not* what self-hosting uses: it installs neither kro nor the serve pod's RBAC.

One platform-side prerequisite applies and is worth checking first: the shard's
`virtualWorkspaceURL` must be reachable from your cluster. If it is not, this
provider still installs, reports healthy, and reconciles Templates — but never
acts on an Instance, because Instances are watched through the APIExport virtual
workspace. See
[Platform prerequisite](../../docs/byo-providers.md#platform-prerequisite-a-publicly-dialable-virtual-workspace-url).

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
