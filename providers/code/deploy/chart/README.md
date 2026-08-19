# faros-code-provider

faros provider that manages source-code repositories and access (deploy keys, collaborators) across git hosting providers (GitHub today). Ships the provider Deployment, ClusterIP Service, and the CatalogEntry that registers the provider — with its four tenant-authored APIResourceSchemas — with the faros hub.

Helm chart for the faros **code** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-code

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-code create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./code.kubeconfig

helm upgrade --install code oci://ghcr.io/faroshq/charts/faros-code-provider \
  --namespace faros-provider-code \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/code/ |
| `image.repository` | `ghcr.io/faroshq/faros-code-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `1` | Number of Deployment replicas. Keep this at 1 unless bundleStore.existingClaim points at shared storage: commit_files writes provider-owned bundles to disk and the RepositoryCommit controller must read the same bytes. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8083` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). |
| `hub.url` | `https://faros-hub.faros.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. |
| `hub.tokenSecretRef.name` | `""` | Empty omits the Authorization header — the heartbeat endpoint does not require it. Set this ONLY when the Secret already exists in the release namespace; the reference is not optional, so a missing Secret wedges the pod in `CreateContainerConfigError`. |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `providerKubeconfig` |  | Secret the provider mounts read-only at /var/run/secrets/faros/faros-provider-kubeconfig to reach kcp (CODE_KUBECONFIG). The hub catalog controller mints it when it reconciles the CatalogEntry; the volume is `optional`, so the pod stays schedulable and serves portal/MCP reads until the Secret app… |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `githubOAuth` |  | GitHub "Connect with GitHub" OAuth App (optional). When enabled, the portal shows a "Connect with GitHub" button and mints OAuth tokens (with read:packages so the repository Packages panel works). Leave disabled to use pasted Personal Access Tokens only — repositories, deploy keys, collaborators… |
| `githubOAuth.enabled` | `false` |  |
| `githubOAuth.clientId` | `""` | GitHub OAuth App client ID. |
| `githubOAuth.clientSecretRef` |  | The client secret is read from a Secret you create — never put it in values.yaml in plaintext for prod. |
| `githubOAuth.clientSecretRef.name` | `""` |  |
| `githubOAuth.clientSecretRef.key` | `clientSecret` |  |
| `githubOAuth.redirectURL` | `""` | Absolute callback URL registered on the GitHub OAuth App. It is a top-level redirect from GitHub (no faros auth), so it must be publicly reachable and forward to the provider's HTTP backend. Two options — both end in /callback: 1. Reuse the hub ingress via its /services proxy (no extra ingress ne… |
| `githubOAuth.portalOrigin` | `""` | postMessage target origin the popup returns the token to (the hub/portal origin). Empty broadcasts to "*" — set this in production. |
| `githubOAuth.scopes` | `""` | Comma-separated scopes. Empty → provider default (repo,delete_repo,read:org,admin:public_key,read:packages). delete_repo is required for the provider to remove repositories it created. |
| `tenant` |  | Tenant credential resolution. Empty → the provider default ("default"), the namespace the portal writes Connection token Secrets into. Override only if an admin pushes credential Secrets into a namespace tenants cannot write to. |
| `tenant.credentialsNamespace` | `""` |  |
| `bundleStore` |  | RepositoryCommit source bundles. The CR stores only a bundle ref/digest; the generated file contents live here until the controller commits them. |
| `bundleStore.path` | `/var/lib/faros-code/commit-bundles` |  |
| `bundleStore.existingClaim` | `""` | Use an existing PVC instead of rendering one. |
| `bundleStore.emptyDir` | `false` | Explicit non-durable fallback for throwaway/dev deployments. |
| `bundleStore.persistence.enabled` | `true` |  |
| `bundleStore.persistence.size` | `1Gi` |  |
| `bundleStore.persistence.storageClassName` | `""` |  |
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
| `envFromSecret` | `""` | Optional name overrides + pod-level scheduling controls. Optional Secret whose keys are injected wholesale as environment variables (GitHub OAuth app credentials and other dev secrets) — the containerized equivalent of sourcing providers/code/.env. |
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |

