# faros-quickstart-provider

Reference faros provider demonstrating the platform's extension surface end-to-end. Ships the provider Deployment, ClusterIP Service, and the CatalogEntry that registers the provider (UI + backend + a sample greetings APIExport) with the faros hub. Pure broker — no kcp or kro kubeconfig wiring; useful as a copy-from template for new providers.

Helm chart for the faros **quickstart** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-quickstart

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-quickstart create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./quickstart.kubeconfig

helm upgrade --install quickstart oci://ghcr.io/faroshq/charts/faros-quickstart-provider \
  --namespace faros-provider-quickstart \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/quickstart/ |
| `image.repository` | `ghcr.io/faroshq/faros-quickstart-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `2` | Number of Deployment replicas. The provider is stateless (no kcp client cache, no local storage), so any replica count is safe. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). Empty url → heartbeats disabled, which is fine for a UI-only demo install. |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. Leave name empty to send unauthenticated heartbeats (dev only). |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted by the platform admin via /bonkers (admin onboarding). The init container uses it to apply the provider's schemas/APIExport/slice/bind grant. Key must be "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
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

