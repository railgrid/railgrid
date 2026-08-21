# faros-app-studio-provider

App Studio provider chart. Ships the provider Deployment, Service, and CatalogEntry. Configure durable App Studio message storage with store.databaseURLSecretRef.

Helm chart for the faros **app-studio** provider. `values.yaml` is the source of
truth and carries the full inline notes; this table summarises it.

## Installing

A provider needs a kcp credential for the workspace it registers into.

- **On the platform**, an admin mints it during provider onboarding.
- **Running it yourself**, faros creates the workspace, mints the credential,
  and generates these exact commands for you under **Providers → Self-Hosting**
  in the portal. See [docs/byo-providers.md](../../../../docs/byo-providers.md).

```bash
kubectl create namespace faros-provider-app-studio

# The data key MUST be `kubeconfig` — the chart mounts that exact key.
kubectl --namespace faros-provider-app-studio create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=./app-studio.kubeconfig

helm upgrade --install app-studio oci://ghcr.io/faroshq/charts/faros-app-studio-provider \
  --namespace faros-provider-app-studio \
  --set hub.url=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

### Product telemetry

Product telemetry is disabled by default. Self-hosted installations make no
telemetry network calls unless `telemetry.enabled=true` is explicitly set and
the provider has `hub.url` plus its provisioned `providerKubeconfig`. The
telemetry client uses the ServiceAccount token in that kubeconfig, not the
legacy heartbeat token. When enabled, App Studio sends only bounded activation
events to the hub; it never sends
project names, prompts, URLs, commits, source content, or credentials.

## Values

| Key | Default | Notes |
|---|---|---|
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `replicaCount` | `1` | App Studio assistant project mutations use a single active writer. The chart rejects other values and uses a Recreate deployment strategy to prevent cross-pod overlap during upgrades. Safe to scale with the default emptyDir workspace: runs and external operations are guarded by durable claims, pr… |
| `internalPort` | `8091` | internalPort carries peer-forwarded project requests between replicas. Deliberately not part of the Service. |
| `image` |  |  |
| `image.repository` | `ghcr.io/faroshq/faros/app-studio-provider` |  |
| `image.tag` | `""` |  |
| `image.pullPolicy` | `IfNotPresent` |  |
| `serviceAccount` |  | Preview inspection no longer runs a browser sidecar here. The assistant drives the workspace's shared headless browser — the infrastructure provider's Playwright MCP "browser" template, provisioned once per workspace by the Studio reconciler — over the infrastructure data plane. Nothing to config… |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `""` |  |
| `service` |  |  |
| `service.type` | `ClusterIP` |  |
| `service.port` | `8081` |  |
| `telemetry` |  | Product telemetry is opt-in; self-hosted deployments remain off and make no telemetry network calls by default. |
| `telemetry.enabled` | `false` | Set `true` only for an explicit product telemetry opt-in. Requires `hub.url` and `providerKubeconfig`. |
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `catalogEntry.renderAsConfigMap` | `true` |  |
| `catalogEntry.uiURL` | `""` |  |
| `catalogEntry.backendURL` | `""` |  |
| `apiExport` |  |  |
| `apiExport.infraIdentityHash` | `""` | REQUIRED for instance lifecycling: identityHash of the infrastructure provider's APIExport (infrastructure.providers.faros.sh), read from its status.identityHash in root:faros:providers:infrastructure (or copy it from /bonkers root-identities). kcp rejects first-party permission claims without it… |
| `apiExport.codeIdentityHash` | `""` | Same, for the code provider's APIExport (code.providers.faros.sh): backs the repositories claim the Project reconciler needs to create the git backing repo. |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted by the platform admin via /bonkers (admin onboarding). Consumed by both the init container and the serve container. Key must be "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `assistant` |  | Assistant chat behavior. |
| `assistant.toolDisclosure` | `""` | How much tool-level detail the chat disclosures show. "" / "summary" (default) — tool names + per-tool sanitized summaries (paths, queries, counts — never raw file contents or secrets). "minimal" — fully opaque generic labels only ("Edited files"), for deployments whose users should not see imple… |
| `assistant.runSandbox.mode` | `off` | Coding sandbox policy: off disables it, byo-only fails closed until a scoped BYO binding resolves, and force uses the platform provider only with explicit development mode. |
| `assistant.runSandbox.developmentMode` | `false` | Explicit development-only authority required by force mode. |
| `assistant.runSandbox.enabled` | `null` | Deprecated boolean. true maps to byo-only with a startup warning. |
| `previewConsole` |  | Browser-console sharing starts automatically while the embedded preview is open. Until both signing fields are configured, App Studio stays available but reports console instrumentation as unavailable. The private key signs short-lived iframe capabilities. Its matching current and previous public… |
| `previewConsole.enabled` | `true` |  |
| `previewConsole.signingKeyID` | `""` |  |
| `previewConsole.signingKeySecretRef.name` | `""` |  |
| `previewConsole.signingKeySecretRef.key` | `private-key.pem` |  |
| `store` |  | App Studio no longer holds a kubeconfig to the runtime cluster. The development data plane (sync, logs, restart, preview readiness) is served by the infrastructure provider as subresources on the project's template instance, reached through the hub as the calling user. See docs/app-studio-runtime… |
| `store.databaseURL` | `""` |  |
| `store.databaseURLSecretRef.name` | `""` |  |
| `store.databaseURLSecretRef.key` | `database-url` |  |
| `store.inMemoryMessageStore` | `false` |  |
| `store.messageRetention` | `""` | Retention window understood by Go's time.ParseDuration, e.g. "720h". |
| `store.messageEncryptionKeysSecretRef.name` | `""` |  |
| `store.messageEncryptionKeysSecretRef.key` | `keys` |  |
| `workspace` |  | App Studio project workspaces. The provider stores checked-out/generated project files here so the agent can list, read, search, and later mutate them. The workspace is a CACHE of git plus in-flight edits: a replica that adopts a project rebuilds the tree from the last commit, so the default is a… |
| `workspace.path` | `/var/lib/faros-app-studio/workspaces` |  |
| `workspace.existingClaim` | `""` |  |
| `workspace.emptyDir` | `true` |  |
| `workspace.persistence.enabled` | `true` |  |
| `workspace.persistence.size` | `1Gi` |  |
| `workspace.persistence.storageClassName` | `""` |  |
| `hub` |  |  |
| `hub.url` | `"http://faros-hub.faros.svc.cluster.local:8080"` |  |
| `hub.actionsExternalURL` | `""` | Public hub origin used by action-enabled development runtimes. Keep this separate from hub.url: the latter is an internal provider-to-hub address. Production action-enabled projects require an absolute HTTPS origin. |
| `hub.actionsCABundleConfigMap` |  | Optional public CA bundle for that origin. The referenced ConfigMap is mounted at a dedicated path so it augments (never masks) image/system trust. Leave empty when the origin chains to the system CA. |
| `hub.actionsCABundleConfigMap.name` | `""` |  |
| `hub.actionsCABundleConfigMap.key` | `ca-bundle.pem` |  |
| `hub.insecure` | `false` |  |
| `hub.tokenSecretRef.name` | `""` |  |
| `hub.tokenSecretRef.key` | `token` |  |
| `podLabels` | `{}` |  |
| `podAnnotations` | `{}` |  |
| `podSecurityContext` |  |  |
| `podSecurityContext.fsGroup` | `65532` |  |
| `resources` | `{}` |  |
| `nodeSelector` | `{}` |  |
| `tolerations` | `[]` |  |
| `affinity` | `{}` |  |
