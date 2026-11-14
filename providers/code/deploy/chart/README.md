# railgrid-code-provider

railgrid provider that manages source-code repositories and access (deploy keys, collaborators) across git hosting providers (GitHub today). Ships the provider Deployment, ClusterIP Service, and the CatalogEntry that registers the provider — with its eight tenant-authored APIResourceSchemas — with the railgrid hub.

Helm chart for the railgrid **code** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, railgrid creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace railgrid-provider-code

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace railgrid-provider-code create secret generic railgrid-provider-kubeconfig \
  --from-file=kubeconfig=./code.kubeconfig

helm upgrade --install code oci://ghcr.io/railgrid/charts/railgrid-code-provider \
  --namespace railgrid-provider-code \
  --set hub.url=https://railgrid.example.com \
  --set providerKubeconfig.secretName=railgrid-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## S3 bucket lifecycle

With `bundleStore.backend: s3` the provider deletes a bundle or snapshot
once it is consumed, but it does not sweep what a crash or an abandoned
request left behind: that is the bucket's job. Expire the two prefixes after
one day (bundles are consumed within minutes and snapshots within an hour):

```json
{"Rules": [
  {"ID": "railgrid-code-bundles",   "Status": "Enabled", "Filter": {"Prefix": "commit-bundles/"}, "Expiration": {"Days": 1}},
  {"ID": "railgrid-code-snapshots", "Status": "Enabled", "Filter": {"Prefix": "git-snapshots/"},  "Expiration": {"Days": 1}}
]}
```

Prepend `bundleStore.s3.prefix` to both prefixes if one is set. On AWS:
`aws s3api put-bucket-lifecycle-configuration --bucket <bucket>
--lifecycle-configuration file://lifecycle.json`; on MinIO: `mc ilm rule add
--expire-days 1 --prefix commit-bundles/ <alias>/<bucket>` and the same for
`git-snapshots/`.

## Values

| Key | Default | Notes |
|---|---|---|
| `image` |  | Container image. Build with: docker build -t IMAGE providers/code/ |
| `image.repository` | `ghcr.io/railgrid/railgrid-code-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `replicaCount` | `1` | Number of Deployment replicas. More than 1 requires `bundleStore.backend=s3` (the chart refuses to render otherwise): the replica that accepts an upload and the leader replica that consumes it must read the same bytes, and a per-pod volume cannot provide that. |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8083` |  |
| `hub` |  | Hub the provider POSTs heartbeats to. Must be reachable from the provider pod (in-cluster Service DNS works). |
| `hub.url` | `https://railgrid-hub.railgrid.svc.cluster.local:9443` |  |
| `hub.tokenSecretRef` |  | Bearer token used in the heartbeat POST. Provided as a Secret because it MUST NOT land in values.yaml in plaintext for prod. |
| `hub.tokenSecretRef.name` | `""` | Empty omits the Authorization header — the heartbeat endpoint does not require it. Set this ONLY when the Secret already exists in the release namespace; the reference is not optional, so a missing Secret wedges the pod in `CreateContainerConfigError`. |
| `hub.tokenSecretRef.key` | `token` |  |
| `hub.insecure` | `false` | Skip TLS verification on heartbeat — dev only, defaults off. |
| `providerKubeconfig` |  | Secret the provider mounts read-only at /var/run/secrets/railgrid/railgrid-provider-kubeconfig to reach kcp (CODE_KUBECONFIG). The hub catalog controller mints it when it reconciles the CatalogEntry; the volume is `optional`, so the pod stays schedulable and serves portal/MCP reads until the Secret app… |
| `providerKubeconfig.secretName` | `railgrid-provider-kubeconfig` |  |
| `githubOAuth` |  | GitHub "Connect with GitHub" OAuth App (optional). When enabled, the portal shows a "Connect with GitHub" button and mints OAuth tokens (with read:packages so the repository Packages panel works). Leave disabled to use pasted Personal Access Tokens only — repositories, deploy keys, collaborators… |
| `githubOAuth.enabled` | `false` |  |
| `githubOAuth.clientId` | `""` | GitHub OAuth App client ID. |
| `githubOAuth.clientSecretRef` |  | The client secret is read from a Secret you create — never put it in values.yaml in plaintext for prod. |
| `githubOAuth.clientSecretRef.name` | `""` |  |
| `githubOAuth.clientSecretRef.key` | `clientSecret` |  |
| `githubOAuth.redirectURL` | `""` | Absolute callback URL registered on the GitHub OAuth App. It is a top-level redirect from GitHub (no railgrid auth), so it must be publicly reachable and forward to the provider's HTTP backend. Two options — both end in /callback: 1. Reuse the hub ingress via its /services proxy (no extra ingress ne… |
| `githubOAuth.portalOrigin` | `""` | postMessage target origin the popup returns the token to (the hub/portal origin). Empty broadcasts to "*" — set this in production. |
| `githubOAuth.scopes` | `""` | Comma-separated scopes. Empty → provider default (repo,delete_repo,read:org,admin:public_key,read:packages). delete_repo is required for the provider to remove repositories it created. |
| `tenant` |  | Tenant credential resolution. Empty → the provider default ("default"), the namespace the portal writes Connection token Secrets into. Override only if an admin pushes credential Secrets into a namespace tenants cannot write to. |
| `tenant.credentialsNamespace` | `""` |  |
| `bundleStore` |  | RepositoryCommit source bundles and staged git snapshots: the only state the provider keeps outside kcp. A CR stores only a reference/digest; the bytes live here until the controller consumes them, and expire on a TTL otherwise. |
| `bundleStore.backend` | `file` | `file`: a volume at `bundleStore.path`, correct for `replicaCount: 1` only. `s3`: any S3-compatible bucket every replica reaches (AWS S3, MinIO, Ceph RGW, …), required for `replicaCount > 1`; no volume is mounted. With `s3` the provider runs no orphan sweep: give the bucket a lifecycle rule (see "S3 bucket lifecycle" below). |
| `bundleStore.path` | `/var/lib/railgrid-code/commit-bundles` | Mount path for `backend: file`. |
| `bundleStore.existingClaim` | `""` | Use an existing PVC instead of rendering one (`backend: file`). |
| `bundleStore.emptyDir` | `false` | Explicit non-durable fallback for throwaway/dev deployments (`backend: file`). |
| `bundleStore.persistence.enabled` | `true` |  |
| `bundleStore.persistence.size` | `1Gi` |  |
| `bundleStore.persistence.storageClassName` | `""` |  |
| `bundleStore.s3.endpoint` | `""` | `host[:port]`, or a URL whose scheme decides TLS. AWS: `s3.<region>.amazonaws.com`. |
| `bundleStore.s3.bucket` | `""` | Bucket name. Must exist unless `createBucket` is set. |
| `bundleStore.s3.region` | `""` | Needed by AWS; most other services ignore it. |
| `bundleStore.s3.prefix` | `""` | Key prefix inside the bucket, so one bucket can be shared. |
| `bundleStore.s3.useSSL` | `true` | Talk TLS to the endpoint. `false` for a plain-HTTP in-cluster MinIO. |
| `bundleStore.s3.createBucket` | `false` | Create the bucket when missing. Dev loops only; provision production buckets outside the chart. |
| `bundleStore.s3.credentialsSecretRef.name` | `""` | Secret holding static credentials. Empty → the AWS credential chain (IRSA / pod identity / `AWS_*` variables, e.g. from `envFromSecret`). |
| `bundleStore.s3.credentialsSecretRef.accessKeyIdKey` | `accessKeyId` |  |
| `bundleStore.s3.credentialsSecretRef.secretAccessKeyKey` | `secretAccessKey` |  |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `resources` |  |  |
| `resources.limits.cpu` | `200m` |  |
| `resources.limits.memory` | `512Mi` |  |
| `resources.requests.cpu` | `50m` |  |
| `resources.requests.memory` | `64Mi` |  |
| `envFromSecret` | `""` | Optional name overrides + pod-level scheduling controls. Optional Secret whose keys are injected wholesale as environment variables (GitHub OAuth app credentials and other dev secrets) — the containerized equivalent of sourcing providers/code/.env. |
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `metrics.enabled` | `true` | Serve Prometheus `/metrics` (controller-runtime reconcile, workqueue and client metrics, Go runtime) on a separate plain-HTTP listener and annotate the pod for scraping (`prometheus.io/*`, `k8s.grafana.com/*`). |
| `metrics.port` | `9090` | Port of the metrics listener (`RAILGRID_METRICS_LISTEN`). |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |


## Repository actions and credential ownership

The chart CatalogEntry includes the Repository Provider Actions contract, matching
`manifest.yaml`. Consumers receive scoped Repository permissions — `read` on the
Repository plus `create` on the `repositories/<action>` subresource;
Code keeps Git credentials and performs Git/PR/comment operations. Configure a
GitHub App Connection with a tenant Secret containing `appID`, `installationID`,
and `privateKey`, or use the existing PAT/OAuth token Connection. Do not mount
Git-host keys into consumer providers.

The runtime image includes Git. Snapshot artifacts reuse the writable commit
bundle storage: uploads are limited to 25 MiB decoded, expire after one hour,
and are bounded by per-tenant quotas. Only small artifact handles enter the
catalog-advertised preparation/publication actions. PR creation and comment
writes are not automatically replayed after an uncertain response. See the
[provider README](../../README.md#repository-provider-actions) for action routing,
identity preconditions, grants, and GitHub App permissions.
