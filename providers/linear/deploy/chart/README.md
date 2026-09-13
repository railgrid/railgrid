# Linear provider Helm chart

Installs Linear's portal, resource reconciliation, Provider Actions and MCP.
Public tenant resources are Connection and Team. Private write receipts live in
Linear's provider workspace; no additional database or PVC is required.

Use the Faros repository root as the Docker build context.
Supply two hosting Secrets, each with a `kubeconfig` key:

- `providerKubeconfig.secretName`: bootstrap workspace administrator for init.
- `runtimeKubeconfig.secretName`: the provider's runtime ServiceAccount identity.

Init installs the exported Connection/Team schemas, APIExport, endpoint slice,
bind grant and CatalogEntry. It also installs the non-exported private receipt
CRD and a narrow provider-local RBAC binding for the `default/provider`
ServiceAccount. Bootstrap must be authorized to grant this permission. Runtime
does not mount the bootstrap credential. Enable Linear in the tenant and accept
its `secrets/get` permission claim.

| Value | Default | Purpose |
| --- | --- | --- |
| `image.repository` | `ghcr.io/faroshq/faros-linear-provider` | Provider image. |
| `image.tag` | chart appVersion | Explicit image version override. |
| `image.pullPolicy` | `IfNotPresent` | Image pull behavior. |
| `workspacePath` | `root:faros:providers:linear` | Fully qualified provider workspace; override for org-owned hosting. |
| `replicaCount` | `1` | One replica required for write admission and recovery. |
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
reconciliation. Connection readiness is refreshed once per minute; actions resolve
credentials and Team bindings again before dispatch.

The Deployment requires one replica and `strategy.type: Recreate`; upgrades
briefly interrupt service so write admission and recovery processes cannot
overlap. New writes require stable request keys (the `Idempotency-Key` header
or body `requestId`). Confirmed receipts with timestamped keys are eligible for
cleanup after 30 days, while uncertain records and opaque-key receipts are
retained within the tenant quota.
The per-tenant receipt cap is 2,000. Reads do not create retained records.

No webhook ingress, subscription or signing secret is required. API-key Secrets
remain tenant-scoped and namespaced; their default namespace is `default`.
See the provider README for action routes and `examples/consumer-rbac.yaml` for
Team/action permissions. Team registration requires administrative `create` on
Teams; ordinary consumers need only resource reads and selected action verbs.
