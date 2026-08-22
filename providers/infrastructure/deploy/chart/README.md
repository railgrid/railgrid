# faros-infrastructure-provider

faros provider that brokers application templates from a central kro (Kube Resource Orchestrator) cluster into faros tenant workspaces. Ships the provider Deployment, ClusterIP Service, the central-kro-cluster kubeconfig Secret (optional, dev convenience), and the CatalogEntry that registers the provider with the faros hub.

Helm chart for the faros **infrastructure** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-infrastructure

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-infrastructure create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./infrastructure.kubeconfig

helm upgrade --install infrastructure oci://ghcr.io/faroshq/charts/faros-infrastructure-provider \
  --namespace faros-provider-infrastructure \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/infrastructure/ |
| `image.repository` | `ghcr.io/faroshq/faros-infrastructure-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `2` | Number of Deployment replicas. The provider is stateless apart from the in-process per-tenant client cache (sync.Map, ~256 entries cap), so >1 replica is safe. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. |
| `hub.tokenSecretRef.name` | `""` | Empty omits the Authorization header — the heartbeat endpoint does not require it. Set this ONLY when the Secret already exists in the release namespace; the reference is not optional, so a missing Secret wedges the pod in `CreateContainerConfigError`. |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `centralKro` |  | Central kro cluster kubeconfig. Two ways to provide it: 1. Inline `centralKro.kubeconfig` (rendered into a Secret by the chart; convenient for dev, NOT recommended for prod). 2. `centralKro.kubeconfigSecretRef` pointing at an existing Secret this chart did NOT create. Use this in prod. |
| `centralKro.kubeconfig` | `""` |  |
| `centralKro.kubeconfigSecretRef.name` | `""` |  |
| `centralKro.kubeconfigSecretRef.key` | `kubeconfig` |  |
| `application` |  | The "application" template (3-tier app exposed on an OIDC-guarded URL). The Application instance controller is OFF unless baseDomain is set AND a central kro kubeconfig is configured (the controller bridges secrets onto that runtime cluster). See docs/application-template-architecture.md. |
| `application.baseDomain` | `""` | Zone apps are served under, e.g. "apps.example.com". Each app gets <prefix\|name>-<tenantHash>.<baseDomain>. Empty → feature disabled. |
| `application.gateway` |  | Gateway API parent the generated Application HTTPRoutes attach to (substituted into Application RGDs as ${faros.gatewayName} / ${faros.gatewayNamespace}). Defaults to the cfgate Cloudflare Tunnel Gateway in-binary; override to point apps at a different Gateway without touching the template. |
| `application.gateway.name` | `"cloudflare-tunnel"` |  |
| `application.gateway.namespace` | `"cfgate-system"` |  |
| `publishing` |  | Platform-owned access-gate configuration shared by simple-webapp, application, and future publishable templates. Templates render the gate (faros-access-proxy) as a component of their own graph via the ${faros.accessProxyImage}/${faros.hubUrl}/${faros.hubPublicUrl} tokens; all app traffic enters… |
| `publishing.baseDomain` | `""` |  |
| `publishing.accessProxyImage` | `ghcr.io/faroshq/faros-access-proxy:latest` |  |
| `publishing.hubURL` | `""` | Internal hub address used for the app-access protocol. Empty falls back to hub.url. In production, use a trusted cluster CA and keep insecure=false. |
| `publishing.hubPublicURL` | `""` | Browser-reachable hub origin used for authorization redirects. Empty defaults to hubURL; set it when the provider uses an in-cluster hub URL. |
| `publishing.insecure` | `false` |  |
| `publishing.publicScheme` | `https` |  |
| `publishing.publicPort` | `0` | Optional externally visible port, e.g. 10443 for a local Gateway. |
| `publishing.gateway.name` | `"cloudflare-tunnel"` |  |
| `publishing.gateway.namespace` | `"cfgate-system"` |  |
| `tenantLimitRange` |  | Default container resource policy stamped into every tenant namespace as a LimitRange named "faros-defaults" (defaultRequest 50m/128Mi, default limit 500m/512Mi, max 2cpu/2Gi per container). Create-only — operators may hand-tune a tenant's copy without it being overwritten. Disable when the runti… |
| `tenantLimitRange.enabled` | `true` |  |
| `development` |  | Development-mode images (docs/app-studio-template-sandboxes.md). These run TENANT code, so production deployments should pin them by digest. Empty values fall back to the in-binary defaults (node → docker.io/library/node:22-bookworm, agent → ghcr.io/faroshq/faros-dev-agent:latest). |
| `development.agentImage` | `""` | The injector image carrying the static faros-dev-agent binary and universal token-bootstrap mode (FAROS_DEV_AGENT_IMAGE). Required as an immutable digest when codingSandbox.enabled is true. |
| `development.previewConsole` |  | Public ES256 JSON Web Key Set used by the injected preview-console bridge to verify short-lived capabilities issued by App Studio. Configure the current key and, during rotation, the previous key. Never put a private signing key here. Empty disables the optional bridge without preventing developm… |
| `development.previewConsole.verificationJWKS` | `""` |  |
| `development.images` |  | Toolchain image per ${faros.devImage.<toolchain>} token — each key K maps to FAROS_DEV_IMAGE_<K>. A template referencing an unconfigured toolchain (other than node) fails setup with a pointer to the missing env var. |
| `development.images.node` | `""` |  |
| `development.images.universal` | `""` | Universal coding sandbox image (`${faros.devImage.universal}`). Pin this tenant-code image by digest in production. |
| `codingSandbox.enabled` | `false` | Platform-owned universal coding sandbox gate. Hosted deployments leave this disabled; BYO/self-hosted installs may set it `true`, but must also provide both `development.images.universal` and `development.agentImage` as immutable `name@sha256:<64 lowercase hex digits>` references. |
| `providerKubeconfig` |  | python: "docker.io/library/python:3.12-slim" go: "docker.io/library/golang:1.26" Container images are NOT configured here. Templates declare them as schema fields with sane defaults (e.g. the database template's spec.version defaults to "16") — the same convention every template follows. See prov… |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `bootstrap` |  | Self-bootstrap via an init container. When enabled, an init container runs `infrastructure init` BEFORE the serve container: it installs the CRDs, CachedResource, and APIExport into the provider workspace. Both containers share ONE kubeconfig (no separately-minted runtime token). |
| `bootstrap.enabled` | `false` |  |
| `bootstrap.kubeconfigSource` | `hubMinted` | Where the shared kubeconfig comes from: hubMinted  - (default) the hub-delivered faros-provider-kubeconfig (providerKubeconfig.secretName). A platform admin applies the CatalogEntry; the hub creates the provider workspace, mints a cluster-admin-in-workspace kubeconfig, and writes it as that Secre… |
| `bootstrap.workspacePath` | `"root:faros:providers:infrastructure"` | Only used when kubeconfigSource=supplied. kcp workspace the provider is installed into (init retargets the supplied kubeconfig at this path; the workspace must already exist). |
| `bootstrap.kcpKubeconfig` | `""` | Provide exactly ONE of: kcpKubeconfig          - inline content, rendered into a Secret by the chart (convenient for dev; avoid for prod). kcpKubeconfigSecretRef - reference to an existing Secret you manage. |
| `bootstrap.kcpKubeconfigSecretRef.name` | `""` |  |
| `bootstrap.kcpKubeconfigSecretRef.key` | `kubeconfig` |  |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  |  |
| `resources.limits.cpu` | `200m` |  |
| `resources.limits.memory` | `256Mi` |  |
| `resources.requests.cpu` | `50m` |  |
| `resources.requests.memory` | `64Mi` |  |
| `podLabels` | `{}` | Optional pod-level overrides. |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |
| `operator` |  | CRD-driven operator mode. When enabled, the chart installs the operator (a controller-manager running `infrastructure-provider controller`), the InfrastructureProvider CRD, RBAC, the two kubeconfig Secrets, and one CR rendered from the values below. The operator then reconciles that CR: bootstrap… |
| `operator.enabled` | `false` |  |
| `operator.clusterAdmin` | `true` | Bind the operator ServiceAccount to cluster-admin. Required for the operator to helm-install the kro chart (which creates ClusterRoles/CRDs) and to manage runtime workloads in its own cluster (in-cluster runtime). Set false to wire a narrower role yourself. |
| `operator.image` |  | Operator (controller) image. Defaults to the provider image above. |
| `operator.image.repository` | `""` |  |
| `operator.image.tag` | `""` |  |
| `operator.providerWorkspace` | `""` | kcp workspace the provider is bootstrapped into. Leave empty when the provider kubeconfig is already scoped to the provider workspace (the operator discovers the path from the workspace's kcp.io/path annotation). Set it only for a root-scoped (admin) kubeconfig that must be retargeted at a worksp… |
| `operator.providerKubeconfig` | `""` | Provider (kcp) kubeconfig. Either set it inline via --set-file operator.providerKubeconfig=./provider-infrastructure.kubeconfig (rendered into a Secret), OR reference an existing Secret by name and leave the inline value empty. |
| `operator.providerKubeconfigSecret.name` | `""` |  |
| `operator.providerKubeconfigSecret.key` | `kubeconfig` |  |
| `operator.runtimeKubeconfig` | `""` | Runtime-cluster kubeconfig (where kro + the provider serve Deployment run). |
| `operator.runtimeKubeconfigSecret.name` | `""` |  |
| `operator.runtimeKubeconfigSecret.key` | `kubeconfig` |  |
| `operator.kro` |  | kro Helm release the operator lifecycles on the runtime cluster. Upstream kro, single-cluster: the provider's instance controller bridges kcp → runtime, so kro never talks to kcp and the retired faroshq/kro-multicluster fork is no longer used. |
| `operator.kro.chart` | `oci://registry.k8s.io/kro/charts/kro` |  |
| `operator.kro.version` | `0.9.3` |  |
| `operator.kro.namespace` | `kro-system` |  |
| `operator.kro.releaseName` | `kro` |  |
| `operator.kro.extraValues` | `{}` |  |
| `operator.provider` |  | Provider serve Deployment the operator owns on the runtime cluster. |
| `operator.provider.replicas` | `2` |  |
| `operator.provider.port` | `8081` |  |
| `operator.application` |  | Application-template exposure layer (the `application` template's public URL + Gateway API parent). This is the operator-mode equivalent of the top-level `application.*` values: the operator owns the serve Deployment, so these land on the InfrastructureProvider CR (FAROS_APP_BASE_DOMAIN / FAROS_G… |
| `operator.application.baseDomain` | `""` | DNS zone apps are served under, e.g. "apps.example.com". REQUIRED to enable app exposure — the Application instance controller stays disabled until this is set. Empty → feature off. |
| `operator.application.gateway` |  | Gateway API parent the generated HTTPRoutes attach to. Empty fields → "cloudflare-tunnel" / "cfgate-system" (the in-binary defaults). |
| `operator.publishing.baseDomain` | `""` |  |
| `operator.publishing.accessProxyImage` | `ghcr.io/faroshq/faros-access-proxy:latest` |  |
| `operator.publishing.hubURL` | `""` |  |
| `operator.publishing.hubPublicURL` | `""` |  |
| `operator.publishing.insecure` | `false` |  |
| `operator.publishing.publicScheme` | `https` |  |
| `operator.publishing.publicPort` | `0` |  |
