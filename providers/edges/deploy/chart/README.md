# faros-edges-provider

Standalone faros provider for edges — both Kubernetes clusters and Linux/SSH servers, under one group edges.faros.sh. Terminates the agent reverse tunnel (revdial) in-process, owns the KubernetesCluster + LinuxServer API + its APIExport, and exposes kubectl/SSH/MCP through the hub backend proxy. Ships the provider Deployment (horizontally scalable — each agent dials one replica and a Lease registry + pod-to-pod relay route requests to the tunnel's owner), a ClusterIP Service, and the CatalogEntry that registers the provider with the faros hub.

Helm chart for the faros **edges** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-edges

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-edges create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./edges.kubeconfig

helm upgrade --install edges oci://ghcr.io/faroshq/charts/faros-edges-provider \
  --namespace faros-provider-edges \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build (context = REPO ROOT) with: docker build -f providers/edges/Dockerfile -t IMAGE . |
| `image.repository` | `ghcr.io/faroshq/faros-edges-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `1` | Safe to scale: each agent dials ONE replica; the replica terminating the tunnel claims it in a Lease registry in the provider workspace, and every other replica relays pickups and data-plane requests to the owner over the internal listener (pod-to-pod on internalPort, never exposed on the Service… |
| `internalPort` | `8090` | internalPort carries the replica-to-replica tunnel relay and forwarded revdial pickups. Deliberately not part of the Service. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8088` |  |
| `hub` |  | Hub wiring. |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` | Heartbeat + agent-kubeconfig externalization base. In-cluster Service DNS. |
| `hub.externalURL` | `""` | External URL agents dial (baked into the agent kubeconfig the RBAC reconciler mints). Defaults to hub.url when empty. |
| `hub.internalURL` | `""` | Internal URL the provider uses for hub-side calls when it differs from the external one (split-horizon deployments). Optional. |
| `hub.tokenSecretRef` |  | Bearer token for the heartbeat POST. Secret-backed; empty → unauthenticated heartbeat (dev only). |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verify on heartbeat — dev only. |
| `hub.caData` | `""` | Hub CA bundle (PEM) embedded into per-agent kubeconfigs so agents trust the hub serving cert. Provide EITHER caData (inline PEM) or caSecretRef. |
| `hub.caSecretRef.name` | `""` |  |
| `hub.caSecretRef.key` | `ca.crt` |  |
| `devMode` | `false` | Enables dev-mode shortcuts in the controllers (e.g. relaxed kubeconfig CA). |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted via /bonkers (admin onboarding). Used by BOTH the init container (bootstrap APIExport/schemas) and the serve container (token validation + cross-tenant controllers). Key must be "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `catalogEntry` |  | Render the CatalogEntry into a ConfigMap the init container applies into the provider workspace (it is a kcp resource, not a host-cluster one). |
| `catalogEntry.enabled` | `true` |  |
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

