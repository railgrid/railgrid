# faros-databricks-provider

Faros Databricks provider

Helm chart for the faros **databricks** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-databricks

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-databricks create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./databricks.kubeconfig

helm upgrade --install databricks oci://ghcr.io/faroshq/charts/faros-databricks-provider \
  --namespace faros-provider-databricks \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  |  |
| `image.repository` | `ghcr.io/faroshq/faros-databricks-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `1` | Safe to scale: serving resolves tenant clusters through the APIExport endpoint slice on every replica, and the controllers are leader-elected (one active set per Lease in the provider workspace). |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `hub` |  |  |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef.name` | `faros-databricks-hub-token` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` |  |
| `controller` |  | The multicluster controller is required for production: it validates the Connection -> Warehouse -> Table dependency chain and serves provider authority for actions. Set mode=rest-only only for an intentional local UI/API process; /readyz and heartbeat then describe that explicit mode. |
| `controller.mode` | `required` |  |
| `controller.retryInterval` | `15s` |  |
| `bootstrap` |  | Bootstrap is explicit because the provider cannot become ready without a provider-workspace kubeconfig. `init` runs the image's `init` subcommand in an init container and may apply the rendered CatalogEntry. `external` skips that init container and requires an operator/GitOps process to have alre… |
| `bootstrap.enabled` | `true` |  |
| `bootstrap.mode` | `init` |  |
| `providerKubeconfig` |  |  |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `catalogEntry` |  |  |
| `catalogEntry.enabled` | `true` |  |
| `mcp` |  | MCP remains a compatibility/presentation surface; Provider Actions do not depend on it. The localhost protection bypass is a narrowly scoped local-dev setting and must remain false in production. |
| `mcp.enabled` | `true` |  |
| `mcp.disableLocalhostProtection` | `false` |  |
| `allowedHostSuffixes` | `[]` | Empty uses the backend's standard Databricks cloud suffix allowlist. Add private workspace suffixes deliberately; values are passed as a comma- separated DATABRICKS_ALLOWED_HOST_SUFFIXES environment variable. |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  |  |
| `resources.limits.cpu` | `200m` |  |
| `resources.limits.memory` | `256Mi` |  |
| `resources.requests.cpu` | `50m` |  |
| `resources.requests.memory` | `64Mi` |  |
| `podSecurityContext` |  |  |
| `podSecurityContext.runAsNonRoot` | `true` |  |
| `podSecurityContext.runAsUser` | `65532` | Numeric UID/GID for the distroless "nonroot" user: kubelet cannot verify runAsNonRoot against a non-numeric image USER and refuses to start the container without these. |
| `podSecurityContext.runAsGroup` | `65532` |  |
| `podSecurityContext.seccompProfile.type` | `RuntimeDefault` |  |
| `securityContext` |  |  |
| `securityContext.allowPrivilegeEscalation` | `false` |  |
| `securityContext.readOnlyRootFilesystem` | `true` |  |
| `securityContext.runAsNonRoot` | `true` |  |
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |

