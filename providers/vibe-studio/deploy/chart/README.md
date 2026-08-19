# faros-vibe-studio-provider

vibe-studio: faros's wizard-first app builder provider. Guided intake recommends an infrastructure Template, provisions a dev sandbox from its scaffold, then drops into conversational building with preview and promotion (docs/vibe-studio-design.md).

Helm chart for the faros **vibe-studio** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-vibe-studio

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-vibe-studio create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./vibe-studio.kubeconfig

helm upgrade --install vibe-studio oci://ghcr.io/faroshq/charts/faros-vibe-studio-provider \
  --namespace faros-provider-vibe-studio \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/vibe-studio/ |
| `image.repository` | `ghcr.io/faroshq/faros-vibe-studio-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `2` | Multi-replica safe: session appends are ordinal-CAS'd in the store, so racing replicas conflict instead of interleaving. With the in-memory store (no database.dsnSecretRef) run a single replica — state is per-process. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `database` |  | Postgres DSN for the durable event store. Empty name → in-memory store (dev only; state does not survive restarts). |
| `database.dsnSecretRef.name` | `""` |  |
| `database.dsnSecretRef.key` | `dsn` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Empty url → heartbeats disabled. |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` |  |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted by the platform admin. The init container uses it to apply schemas/APIExport/slice/bind grant. |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `catalogEntry` |  | Render the CatalogEntry into a ConfigMap the init container self-registers into the provider workspace. Set false to manage it via GitOps separately. |
| `catalogEntry.enabled` | `true` |  |
| `apiExport` |  |  |
| `apiExport.infraIdentityHash` | `""` | REQUIRED for instance lifecycling: identityHash of the infrastructure provider's APIExport (infrastructure.providers.faros.sh), read from its status.identityHash in root:faros:providers:infrastructure. kcp rejects first-party permission claims without it — with an empty value the Project reconcil… |
| `apiExport.codeIdentityHash` | `""` | Same, for the code provider's APIExport (code.providers.faros.sh): backs the repositories claim the git-seeding reconciler needs. |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  |  |
| `resources.limits.cpu` | `500m` |  |
| `resources.limits.memory` | `256Mi` |  |
| `resources.requests.cpu` | `100m` |  |
| `resources.requests.memory` | `64Mi` |  |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |

