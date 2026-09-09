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
  --set hub.publicURL=https://faros.example.com \
  --set providerKubeconfig.secretName=faros-provider-kubeconfig \
  --set catalogEntry.enabled=true
```

## Values

| Key | Default | Notes |
|---|---|---|
| `nameOverride` | `""` |  |
| `fullnameOverride` | `""` |  |
| `replicaCount` | `1` | Must remain `1`. Each workspace has one shared, single-session Playwright Browser, while browser session ownership is process-local. The chart rejects values greater than `1` and uses Recreate upgrades to prevent transient pod overlap. |
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
| `catalogEntry` |  | When true, the chart renders the CatalogEntry (which registers the provider with the hub) into a ConfigMap that the init container applies into the provider workspace via the provider kubeconfig. The CatalogEntry is a kcp resource, so it is NOT applied to the hosting cluster this chart installs i… |
| `catalogEntry.enabled` | `true` |  |
| `catalogEntry.renderAsConfigMap` | `true` |  |
| `catalogEntry.uiURL` | `""` |  |
| `catalogEntry.backendURL` | `""` |  |
| `providerKubeconfig` |  | Secret holding the workspace-admin kubeconfig minted by the platform admin via /bonkers (admin onboarding). Consumed by both the init container and the serve container. Key must be "kubeconfig". |
| `providerKubeconfig.secretName` | `faros-provider-kubeconfig` |  |
| `assistant` |  | Assistant chat behavior. |
| `assistant.toolDisclosure` | `""` | How much tool-level detail the chat disclosures show. "" / "summary" (default) — tool names + per-tool sanitized summaries (paths, queries, counts — never raw file contents or secrets). "minimal" — fully opaque generic labels only ("Edited files"), for deployments whose users should not see imple… |
| `assistant.runSandbox.mode` | `off` | Coding sandbox policy: off disables it, byo-only fails closed until a scoped BYO binding resolves, and force uses the platform provider only with explicit development mode. |
| `assistant.runSandbox.developmentMode` | `false` | Explicit development-only authority required by force mode. |
| `assistant.runSandbox.enabled` | `null` | Deprecated boolean. true maps to byo-only with a startup warning. |
| `assistant.limits.maxIterations` | `""` | Per-run model-call ceiling (`APP_STUDIO_ASSISTANT_MAX_ITERATIONS`). Empty keeps the provider default of 200. Only an explicit `0` or `unlimited` removes it, which the provider logs; never do this for untrusted tenants. |
| `assistant.limits.rolloutBudgetTokens` | `""` | Per-run weighted-token budget (`APP_STUDIO_ASSISTANT_ROLLOUT_BUDGET_TOKENS`). Empty keeps the provider default of 2,000,000. Only an explicit `0` or `unlimited` disables it (logged). |
| `assistant.limits.orgMonthlyUSDCap` | `""` | Per-organization monthly model spend cap in USD (`APP_STUDIO_ORG_MONTHLY_USD_CAP`), checked before every model call across all of the organization's projects, runs, and replicas; usage is priced from provider token counts and unknown models are charged a frontier-tier rate. Empty keeps the provider default of 100. Only an explicit `0` or `unlimited` disables it (logged). The cap bounds spend already incurred rather than reserving it up front: the check reads the shared ledger before a model call and the cost is only known after it, so a month can overshoot by at most one model call per call in flight when the cap is crossed. The overshoot does not compound — every later call fails closed. |
| `previewBridge` |  | Signed DOM annotation sharing starts automatically while the embedded preview is open. Until both signing fields are configured, App Studio stays available but reports the optional preview bridge as unavailable. The private key signs short-lived iframe capabilities. Its matching current and previous public… |
| `previewBridge.enabled` | `true` |  |
| `previewBridge.signingKeyID` | `""` |  |
| `previewBridge.signingKeySecretRef.name` | `""` |  |
| `previewBridge.signingKeySecretRef.key` | `private-key.pem` |  |
| `store` |  | App Studio no longer holds a kubeconfig to the runtime cluster. The development data plane (sync, logs, restart, preview readiness) is served by the infrastructure provider as subresources on the project's template instance, reached through the hub as the calling user. See docs/app-studio-runtime… |
| `store.databaseURL` | `""` |  |
| `store.databaseURLSecretRef.name` | `""` |  |
| `store.databaseURLSecretRef.key` | `database-url` |  |
| `store.inMemoryMessageStore` | `false` |  |
| `store.messageRetention` | `""` | Retention window understood by Go's time.ParseDuration, e.g. "720h". |
| `store.attachmentDraftRetention` | `"24h"` | Explicit draft attachment uploads expire after this duration; turn admission promotes bound receipts to retained storage. |
| `store.attachmentWorkspaceQuotaBytes` | `1073741824` | Maximum attachment bytes across all Projects in a workspace. Bound attachments remain counted while their owning conversation exists; `0` disables this workspace limit. Draft-only per-project abuse limits remain provider defaults. |
| `store.messageEncryptionKeysSecretRef.name` | `""` |  |
| `store.messageEncryptionKeysSecretRef.key` | `keys` |  |
| `workspace` |  | Persistent project source storage, including projects without Git. Uses a PVC by default. |
| `workspace.path` | `/var/lib/faros-app-studio/workspaces` |  |
| `workspace.existingClaim` | `""` |  |
| `workspace.emptyDir` | `false` | Use ephemeral storage only for disposable projects; pod replacement loses local source. |
| `workspace.persistence.enabled` | `true` |  |
| `workspace.persistence.size` | `1Gi` |  |
| `workspace.persistence.storageClassName` | `""` |  |
| `hub` |  |  |
| `hub.url` | `"http://faros-hub.faros.svc.cluster.local:8080"` |  |
| `hub.publicURL` | `""` | Browser-reachable HTTPS hub origin for private preview authorization redirects and one-use browser-session handoffs. It may differ from `hub.url`, which is the internal provider-to-hub route; private browser inspection fails closed when this is unset or invalid. |
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

## Workspace persistence and upgrades

Git is optional in App Studio. The default workspace PVC retains project files
and source metadata across provider pod replacement. Configure a storage class
or `workspace.existingClaim` when your cluster has no default provisioner. A PVC
is not an external backup; follow your organization's volume backup policy.
Explicit `workspace.emptyDir: true` or `workspace.persistence.enabled: false`
uses ephemeral storage unless an existing claim is supplied.

**Before upgrading an installation that uses ephemeral storage:**

1. Pause App Studio traffic and wait for active assistant runs and writes to stop.
2. While the old pod still exists, copy the complete directory at `workspace.path`
   to an operator-controlled backup. Include hidden files and source metadata;
   copying only repository files loses uncommitted work and settlement state.
3. Provision the destination PVC. Mount it in a temporary maintenance pod and
   restore the backup, preserving file ownership and permissions for the App
   Studio security context. Verify file counts and checksums before proceeding.
4. Upgrade with `workspace.emptyDir=false`, `workspace.persistence.enabled=true`,
   and `workspace.existingClaim` naming that populated PVC. The chart uses a
   Recreate strategy; schedule this interruption with users.
5. Verify representative projects, including projects without Git, can read
   their existing files. Replace the provider pod once and verify again before
   reopening traffic. Retain the backup until these checks pass.

Changing Helm values does not migrate files. Do not remove the old ephemeral
pod until its files have been backed up and verified on the destination volume.
If validation fails, keep traffic paused and restore the verified backup onto
the retained PVC before restarting the previous application version. Do not
roll back to an empty ephemeral workspace.
