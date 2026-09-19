# railgrid-quickstart-provider

Reference railgrid provider demonstrating the platform's extension surface end-to-end. Ships the provider Deployment, ClusterIP Service, and the CatalogEntry that registers the provider (UI + backend + the `greetings` APIExport) with the railgrid hub. A complete, copy-from template for a new provider's chart.

Helm chart for the railgrid **quickstart** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into, and this
chart mounts it into **both** containers as `RAILGRID_PROVIDER_KUBECONFIG`:

- the `init` container applies the two objects the provider ships — the
  APIResourceSchemas and the generated APIExport, both baked into the image at
  `/etc/railgrid/kcp` (override with `RAILGRID_KCP_DIR`) and applied verbatim —
  then creates the APIExportEndpointSlice the controller watches and the bind
  grant;
- the `provider` container watches tenant workspaces through the APIExport
  virtual workspace, and lends the config's host and CA — never its bearer — to
  the per-request clients the `greet` data-plane verb authorizes through.

Without the Secret the pod still serves the portal, but no `Greeting` is ever
reconciled and the verb is disabled. The readiness probe is `/readyz` (watches
are live), separate from the `/healthz` liveness probe (the process is up).

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, railgrid creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace railgrid-provider-quickstart

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace railgrid-provider-quickstart create secret generic railgrid-provider-kubeconfig \
  --from-file=kubeconfig=./quickstart.kubeconfig

helm upgrade --install quickstart oci://ghcr.io/railgrid/charts/railgrid-quickstart-provider \
  --namespace railgrid-provider-quickstart \
  --set hub.url=https://railgrid.example.com \
  --set providerKubeconfig.secretName=railgrid-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/quickstart/ |
| `image.repository` | `ghcr.io/railgrid/railgrid-quickstart-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `2` | Number of Deployment replicas. Safe above 1: the HTTP surface is stateless and the reconciler is single-writer by leader election on a Lease in the provider's own kcp workspace (no RBAC on this cluster is involved). |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). Empty url → heartbeats disabled, which is fine for a UI-only demo install. |
| `hub.url` | `https://railgrid-hub.railgrid.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. Leave name empty to send unauthenticated heartbeats (dev only). |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `providerKubeconfig` |  | **Required.** Secret holding the workspace-admin kubeconfig minted by the platform admin via /bonkers (admin onboarding). Key must be `kubeconfig`. Mounted by both containers at `/var/run/secrets/railgrid` and passed as `RAILGRID_PROVIDER_KUBECONFIG`: `init` bootstraps the workspace with it, `serve` watches tenant workspaces and builds the data-plane caller clients from it. |
| `providerKubeconfig.secretName` | `railgrid-provider-kubeconfig` |  |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  |  |
| `resources.limits.cpu` | `200m` |  |
| `resources.limits.memory` | `128Mi` |  |
| `resources.requests.cpu` | `50m` |  |
| `resources.requests.memory` | `32Mi` |  |
| `podLabels` | `{}` | Optional pod-level overrides. |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |

