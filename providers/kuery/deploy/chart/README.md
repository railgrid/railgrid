# faros-kuery-provider

Faros provider for fleet-wide object search, relationship traversal, and impact analysis across connected edge clusters (built on faroshq/kuery). Ships the provider Deployment, ClusterIP Service, and the CatalogEntry that registers the provider (UI + backend + the SavedView APIExport + edgeProxyAccess) with the faros hub. Phase 1 skeleton.

Helm chart for the faros **kuery** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-kuery

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-kuery create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./kuery.kubeconfig

helm upgrade --install kuery oci://ghcr.io/faroshq/charts/faros-kuery-provider \
  --namespace faros-provider-kuery \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/kuery/ |
| `image.repository` | `ghcr.io/faroshq/faros-kuery-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `1` | Number of Deployment replicas. Phase 1 is stateless; Phase 2 introduces the local store, which pins this to 1. Safe to scale WITH store.driver=postgres: engagement is sharded across replicas by per-edge Leases in the provider workspace, and queries/edge listings are answered from the shared store… |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). Empty url → heartbeats disabled, which is fine for a UI-only demo install. |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. Leave name empty to send unauthenticated heartbeats (dev only). |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `store` |  | Embedded kuery store. SQLite-on-PVC is the default; for production scale switch driver to postgres, set dsn to the connection string, and disable persistence. |
| `store.driver` | `sqlite` |  |
| `store.dsn` | `/data/kuery.db` |  |
| `store.persistence.enabled` | `true` |  |
| `store.persistence.size` | `5Gi` |  |
| `store.persistence.storageClassName` | `""` |  |
| `sync` |  | Edge sync resource selection, comma-separated "resource.group" entries (empty group = core). whitelist: ONLY these resources sync; everything else stays discoverable (kind resolution works) but ships no objects. Edge links are often bandwidth-constrained, so the default is the workloads/config/RB… |
| `sync.whitelist` | `>-` |  |
| `sync.blacklist` | `""` |  |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted via /bonkers (admin onboarding). Consumed by both the init container and serve. Key "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  | The sync engine holds informer caches for every engaged edge — size for the fleet, not for an idle web server. |
| `resources.limits.cpu` | `"1"` |  |
| `resources.limits.memory` | `1Gi` |  |
| `resources.requests.cpu` | `100m` |  |
| `resources.requests.memory` | `256Mi` |  |
| `podLabels` | `{}` | Optional pod-level overrides. |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |

