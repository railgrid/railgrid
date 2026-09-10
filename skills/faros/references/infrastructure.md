# Infrastructure provider reference

Read from `providers/infrastructure/` on 2026-09-09. Group
`infrastructure.faros.sh/v1alpha1`.

## 1. Model in one paragraph

Operators publish `Template` CRs into the catalog. Tenants create one kind,
`Instance`, naming a template in `spec.template` and passing template-shaped
input in `spec.values`. The controller validates values against the
template's JSON schema, stamps platform fields, and materializes a kro
resource graph on a provider-owned **runtime cluster** in namespace
`<clusterID>-default`. Status, including `status.url`, is mirrored back to
your workspace. kro, the runtime kubeconfig, Services, and HTTPRoutes are
private backend state. No tenant and no other provider holds a credential
into the runtime cluster.

Tenant-visible kinds: `templates` (read-only, projected) and `instances`
(shortName `inst`), both cluster-scoped. Names like `Application` or
`PostgresDatabase` appear inside templates as `instanceCRD` but are
runtime-internal; do not `kubectl get applications`.

## 2. Instance

```yaml
apiVersion: infrastructure.faros.sh/v1alpha1
kind: Instance
metadata:
  name: demo
  labels: { faros.sh/template: application }     # convention used by the portal
spec:
  template: application        # required, immutable (CEL self == oldSelf)
  values: { … }                # template-shaped; NOT validated at admission
status:
  phase: Pending|Ready|Failed
  template, templateVersion, observedGeneration, message
  conditions: Valid (InvalidValues | TemplateNotFound), Ready, ResourcesReady, OIDCConfigured
  url, host, ready, runtimeNamespace, components, outputs, controlSecretRef, dbConnectionSecretRef, redirectURL   # projected per template
```

Printer columns: Template, Phase, Ready, URL, Age. Finalizer
`instances.infrastructure.faros.sh/runtime`. Invalid values are admitted and
reported as `Valid=False`; the last good runtime keeps running.

Platform-reserved `spec.values` keys you must not set: `farosMode` (except
explicitly `development`), `farosActions*`, `farosNetworkPhase`,
`expose.fqdn`, `farosCluster`, `credentialsSecretName`, `farosRedeployRevision`.

## 3. Template

Fields a consumer cares about: `displayName`, `description`, `category`,
`version`, `exposure internal|optional|public` (default `internal`),
`schema` (JSON Schema for `values`), `sampleValues`, `agent {usage, prerequisites[], outputs[]}`
(read `usage` before writing code), `view`, `dataPlane` (verbs available on
instances), `development` (dev-mode contract: `components`, `scaffold`,
`build.workflowPath`, `maxLifetimeSeconds`, `idleTimeoutSeconds`).

```bash
kubectl get templates
kubectl get template application -o jsonpath='{.spec.agent.usage}'
kubectl get template application -o jsonpath='{.spec.schema}' | jq .
kubectl get template application -o jsonpath='{.spec.sampleValues}'
```

## 4. Shipped templates

| Template | Category | Exposure | Dev-capable | What it is |
|---|---|---|---|---|
| `simple-webapp` v0.2.0 | Workloads | public | yes (Node.js) | One container, one port, public URL. Values: `name`*, `image`* (prod), `port` (8080), `replicas` 1..10, `env` map, `expose.hostnamePrefix`, `access public\|private`. Status: `url`, `host`, `ready`. |
| `application` v0.1.0 | Workloads | public | yes (Node.js) | `web` + `api` + Postgres on one host; `/api/*` routed to the api container with the path preserved. Values: `name`*, `webImage`*, `apiImage`* (prod), `webPort`, `apiPort` (8080), `database {version "15"\|"16", size small\|medium\|large}` (immutable), `oidc {mode none\|byo}` (dev preview auth only), `access`, `expose.hostnamePrefix`. Contract: bind `0.0.0.0`, honor `$PORT`, api reads `DATABASE_URL` (`postgres://appuser:…@host:5432/appdb`, `sslmode=disable`), DB starts empty, retry first connect, frontend calls `/api/*` same-origin. |
| `worker` v0.1.0 | Workloads | internal | yes | Deployment only, no Service. `replicas` 1..5. |
| `cron-job` v0.1.0 | Workloads | internal | no | `name`*, `image`*, `schedule` (`"0 * * * *"` UTC), `env`. |
| `database` v0.1.0 | Databases | internal | no | Standalone Postgres. `name`* (≤ 50), `version "15"\|"16"` (immutable). Status: `host`, `port`, `ready`, `connectionSecretRef` → Secret `<name>-db-credentials` with `host/port/user/dbname/password/uri`. DB `appdb`, user `appuser`. |
| `redis-cache` v0.2.0 | Databases | internal | no | Ephemeral Redis. `size small(64Mi)\|medium(256Mi)\|large(1Gi)`, `version "6"\|"7"`. |
| `browser` v0.1.0 | Agent tools | optional | no | Headless Chromium + Playwright MCP, reached via data-plane `proxy` verb. |
| `searxng` | Search | | no | Backs agents' `web_search`. |
| `universal-coding-sandbox` | Development | internal | yes | Disabled unless the operator enables it. |

No bucket, secret, domain, or route templates exist. `env` maps are stored
world-readable; never put secrets there.

## 5. URLs, TLS, access

- Host: `<hostnamePrefix|instance name>-<12 hex sha256(clusterID)>.<baseDomain>`;
  prefix ≤ 50 chars; URL `https://<host>`; base domain is platform config
  (`.faros.app` in the hosted portal copy). Custom domains are not supported.
- Exposure is a Gateway API `HTTPRoute` attached to the platform Gateway
  (default a Cloudflare Tunnel Gateway); TLS and DNS are handled at that edge.
- **A brand-new host fails TLS for several minutes.** Because the certificate
  is issued per hostname at that edge, a freshly created instance resolves in
  DNS but rejects the TLS handshake (curl exit 35, `sslv3 alert handshake
  failure`) until issuance completes. Measured 2026-09-09: about eight minutes
  from promote to first 200. `status.phase` is already `Ready` and the pods are
  serving; only the edge certificate is missing, so there is nothing to fix.
  Distinguish it from a real failure by hitting an older instance on the same
  base domain — if that serves and the new one does not, it is issuance —
  and confirm with
  `openssl s_client -connect <host>:443 -servername <host>`, which shows a
  subject CN matching the host once the certificate exists.
- Every publishable template routes through an infrastructure-owned
  `faros-access-proxy` gate. `values.access: public` is pure passthrough;
  `private` requires platform sign-in and a SubjectAccessReview on `get`
  of `instances/<name>/access`. Grants are a ClusterRole with two rules
  (the `instances/access` get, and kcp `access` on nonResourceURL `/`)
  plus a ClusterRoleBinding per user (`faros:<email>`). App Studio's
  publishing routes write these for you.
- Changing `access` is an in-place patch; no redeploy.

## 6. Private images and secrets

- Pull secret: a `kubernetes.io/dockerconfigjson` Secret named
  `<instance>-registry` in namespace `default` of your workspace; the
  controller bridges it into the runtime namespace and attaches it to the
  default ServiceAccount. App Studio mints this at promote from the code
  Connection token.
- BYO OIDC client secret: Secret `cloud-credentials` key `oidc_client_secret`,
  bridged as `cloud-credentials-<instance>`.

## 7. Limits

No ResourceQuota. Every tenant runtime namespace gets a create-only
`LimitRange` `faros-defaults`: request 50m/128Mi, limit 500m/512Mi, max
2 CPU / 2Gi per container. Template-level ceilings: `replicas` 1..10 (1..5
for `worker`). Dev sandboxes have `maxLifetimeSeconds` and
`idleTimeoutSeconds` (≤ 7 days). Sandboxes run PSS-restricted, non-root
UID 1000, seccomp RuntimeDefault, all capabilities dropped.

## 8. Development mode

`values.farosMode: "development"` on a dev-capable template gives a live
sandbox: image inputs may be omitted, declared components run a dev image
with hot reload, and files are pushed with the data plane or MCP. Node.js is
the only toolchain in the shipped dev images. Dev instances default to
`access: private`.

Data-plane verbs (through the hub, as you; production instances answer 409).
`scripts/faros-dev.sh` wraps all of them; verified against a live hub
2026-09-10:

```
GET  /services/providers/infrastructure/dataplane/clusters/{cluster}/instances/{name}/status
GET  …/instances/{name}/components/{c}/log        (stream; bound it with timeout)
GET  …/instances/{name}/components/{c}/process    → {running, port, portReachable, sourceRevision, sourceDigest, …}
POST …/instances/{name}/components/{c}/sync       {files[{path,content}], deletePaths[], restart ""|auto|always, sourceRevision?, sourceDigest?}
                                                   → {phase:"Synced", changed[], deleted[], reloadRuns[], restarted, sourceRevision, sourceDigest}
POST …/instances/{name}/components/{c}/restart
POST …/instances/{name}/components/{c}/env
POST …/instances/{name}/components/{c}/exec       start: header Idempotency-Key (required) + {action:"start", argv[], workdir?, timeoutSeconds ≤120, sourceRevision, sourceDigest} → {sessionID, requestID, state:"queued"}
                                                   poll:  {action:"poll", sessionID} → {state queued|running|succeeded|failed…, exitCode, stdout, stderr, truncated}
                                                   cancel: {action:"cancel", sessionID}
```

- Sync paths are relative to the component's `workspacePath`; at most 512
  files. `sourceRevision` and `sourceDigest` go together or not at all. With
  them the sync is **authoritative**: it replaces the component's managed
  file set, the revision must not go backwards, and the digest must equal
  sha256 over `path` NUL `content` NUL for every file sorted by path (hex, an
  optional `sha256:` prefix is ignored) or the call is a 409. Without them
  files are written but the component has no source revision, and exec
  refuses with `sourceRevision is required for start`.
- Exec runs argv in a separate stateless executor: no shell (pass
  `sh -c '…'` yourself), none of the app's environment (`$PORT`,
  `DATABASE_URL` unset; env is `HOME LANG PATH PWD TMPDIR NPM_CONFIG_CACHE`),
  working directory the component workspace. Exec is refused until the
  instance is Ready and its network phase is `runtime`.
- There is no metrics surface.

## 9. MCP tools (`infrastructure__*`)

| Tool | Input | Notes |
|---|---|---|
| `list_templates` | `category?`, `cloud?` | Entries carry `exposure`; `internal` never gets a URL |
| `describe_template` | `name`, `version?` | Schema, `agent` guidance, `development` contract. Check exposure before promising a URL. |
| `provision` | `template`, `templateVersion?`, `name`, `values` | Same object the portal writes. Not idempotent. |
| `list_instances` | none | `{instances:[{name,template,phase,message}]}` |
| `get_instance` | `name` | Full instance with conditions and child status |
| `update_instance` | `name`, `values` (RFC 7386 merge patch) | Roll a new image, scale, change env or schedule. Rejected for immutable fields. |
| `delete_instance` | `name` | Destructive, idempotent |
| `dev_sync` | `instance`, `files[{path,content}]`, `restart? auto\|none` | ≤ 16 MiB; paths must fall under a declared component `workspacePath`; toolchain manifest validated (a node component needs `package.json`) |
| `dev_logs` | `instance`, `component`, `maxBytes?` (65536, max 262144) | Tail |
| `dev_restart` | `instance`, `component` | |

Provider-direct endpoint: `https://<hub>/services/providers/infrastructure/mcp`.

## 10. Relationship to App Studio

App Studio's dev environment is an `Instance` `<project>-dev` with
`farosMode: development`; promotion writes `<project>-prod` with
`farosMode: production` and digest-pinned images. Both are ordinary
instances you can inspect with kubectl. Everything App Studio deploys you can
deploy yourself with the same YAML; App Studio adds the git scaffold, CI,
digest pinning, naming, sharing, and the sandbox loop.

## 11. Self-hosting

The chart supports `operator.enabled=true` to run the operator and runtime in
your own cluster (`selfHosting` block in the CatalogEntry); required value
`operator.application.baseDomain`. The kcp shard's virtual workspace URL must
be reachable from that cluster or Instances never reconcile. Edge clusters
are **not** targets for this provider; edge placement is the edges
provider's `Workload` and `Placement` kinds.
