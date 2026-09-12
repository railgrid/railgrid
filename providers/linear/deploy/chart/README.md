# Linear provider Helm chart

Installs one standalone Linear provider with embedded portal, controller,
webhook receiver, and MCP endpoint. All durable state lives in tenant KRM
resources; no additional database or PVC is required. Use the repository root
as Docker build context: `docker build -f providers/linear/Dockerfile .`.

Two hosting Secrets must be supplied, each with a `kubeconfig` key:

- `providerKubeconfig.secretName`: bootstrap identity for the one-shot init.
- `runtimeKubeconfig.secretName`: provider ServiceAccount identity for export
  virtual-workspace access and authenticated heartbeats.

Init installs the generated schemas, APIExport, endpoint slice, bind grant and
CatalogEntry. Enable the provider in a tenant and accept its `secrets/get` claim.
Keep bootstrap credentials out of the runtime Secret. Existing tenant bindings
do not silently gain future claims; re-enable or migrate when claims change.

| Value | Default | Purpose |
| --- | --- | --- |
| `image.repository` | `ghcr.io/faroshq/faros-linear-provider` | Provider image. |
| `image.tag` | chart appVersion | Explicit image version override. |
| `image.pullPolicy` | `IfNotPresent` | Image pull behavior. |
| `workspacePath` | `root:faros:providers:linear` | Fully qualified provider workspace; override for org-owned hosting. |
| `replicaCount` | `1` | One replica required for bounded event admission. |
| `service.type` / `service.port` | `ClusterIP` / `8092` | HTTP service. |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` | Bootstrap kubeconfig Secret. |
| `runtimeKubeconfig.secretName` | `linear-runtime-kubeconfig` | Runtime identity Secret. |
| `hub.url` | in-cluster Faros hub URL | Heartbeat and caller-scoped MCP API access. |
| `hub.insecure` | `false` | Development-only TLS relaxation. |
| `hub.tokenSecretRef` | empty | Optional heartbeat token override; otherwise runtime kubeconfig token. |
| `catalogEntry.enabled` | `true` | Self-register CatalogEntry from init. |
| `resources` | see values.yaml | Container CPU/memory requests and limits. |
| `serviceAccount`, `nodeSelector`, `tolerations`, `affinity`, `podLabels`, `podAnnotations` | see values.yaml | Hosting settings. |

`/healthz` reports process liveness. `/readyz` requires successful export
reconciliation. Individual Connection readiness is separate and refreshed once
per minute; operations fetch credentials afresh and do not trust cached readiness.

For Linear deliveries, configure an external HTTPS ingress restricted to
`/webhooks/`; supply the exact cluster/namespace/connection callback path in
Linear. This chart does not create a public ingress or change Faros hub auth.
Subscription/signing configuration belongs to the tenant Connection, not Helm
values. API keys and signing secrets must never be committed or printed.
