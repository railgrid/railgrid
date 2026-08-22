# faros-agents-provider

Agents provider chart. Ships the provider Deployment, Service, and CatalogEntry for long-running personal AI agents. Configure durable storage with store.databaseURLSecretRef (Postgres is the only hard dependency beyond the hub).

Product activation telemetry is disabled by default. The self-hosted chart makes
no telemetry network calls unless `telemetry.enabled=true` is explicitly set;
when enabled, the mounted `providerKubeconfig` must contain the provider
ServiceAccount bearer token. Its configured CA is also used to trust a private
hub certificate. No extra telemetry secret is required.

Helm chart for the faros **agents** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-agents

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-agents create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./agents.kubeconfig

helm upgrade --install agents oci://ghcr.io/faroshq/charts/faros-agents-provider \
  --namespace faros-provider-agents \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `replicaCount` | `1` |  |
| `image` |  |  |
| `image.repository` | `ghcr.io/faroshq/faros-agents-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `serviceAccount` |  |  |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8087` |  |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `catalogEntry.renderAsConfigMap` | `true` |  |
| `catalogEntry.uiURL` | `""` |  |
| `catalogEntry.backendURL` | `""` |  |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted by the platform admin via /bonkers (admin onboarding). Consumed by both the init container and the serve container. Key must be "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `store` |  | Durable store. Postgres is the agents provider's only hard dependency beyond the hub; inMemoryStore is an explicit non-durable fallback for dev only. |
| `store.databaseURL` | `""` |  |
| `store.databaseURLSecretRef.name` | `""` |  |
| `store.databaseURLSecretRef.key` | `database-url` |  |
| `store.inMemoryStore` | `false` |  |
| `store.messageEncryptionKeysSecretRef` |  | Optional at-rest encryption for message/transcript content. |
| `store.messageEncryptionKeysSecretRef.name` | `""` |  |
| `store.messageEncryptionKeysSecretRef.key` | `keys` |  |
| `hub` |  |  |
| `hub.url` | `"http://faros-hub.faros.svc.cluster.local:8080"` |  |
| `hub.insecure` | `false` | Skip TLS verification and allow HTTP telemetry transport for explicit local/development use only. |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `telemetry` |  | Opt-in product activation telemetry; disabled by default and no-network when false. |
| `telemetry.enabled` | `false` | Set `true` only for an explicit telemetry opt-in; uses the mounted provider kubeconfig token. |
| `envFromSecret` | `""` | Optional Secret whose keys are injected wholesale as environment variables (LLM/channel credentials) — the containerized equivalent of sourcing .env. |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `podSecurityContext` |  |  |
| `podSecurityContext.fsGroup` | `65532` |  |
| `resources` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |
