# Infrastructure provider reference

Group `infrastructure.faros.sh/v1alpha1`.

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
| `simple-webapp` v0.3.0 | Workloads | public | yes (Node.js) | One container, one port, public URL. Values: `name`*, `image`* (prod), `port` (8080), `replicas` 1..10, `env` map, `connections {database, cache}`, `expose.hostnamePrefix`, `access public\|private`. Status: `url`, `host`, `ready`. |
| `application` v0.1.0 | Workloads | public | yes (Node.js) | `web` + `api` + Postgres on one host; `/api/*` routed to the api container with the path preserved. Values: `name`*, `webImage`*, `apiImage`* (prod), `webPort`, `apiPort` (8080), `database {version "15"\|"16", size small\|medium\|large}` (immutable), `oidc {mode none\|byo}` (dev preview auth only), `access`, `expose.hostnamePrefix`. Contract: bind `0.0.0.0`, honor `$PORT`, api reads `DATABASE_URL` (`postgres://appuser:…@host:5432/appdb`, `sslmode=disable`), DB starts empty, retry first connect, frontend calls `/api/*` same-origin. |
| `worker` v0.2.0 | Workloads | internal | yes | Deployment only, no Service. `replicas` 1..5, `env`, `connections {database, cache}`. Dev component `worker` has sync/logs/restart but no `exec`. |
| `cron-job` v0.2.0 | Workloads | internal | no | `name`*, `image`*, `schedule` (`"0 * * * *"` UTC), `env`, `connections {database, cache}`. **No `command`/`args`**: the entrypoint does the work; with a public image drive it via `env` (e.g. `node:20-alpine` + `NODE_OPTIONS=--import=data:text/javascript;base64,…`). |
| `database` v0.1.0 | Databases | internal | no | Standalone Postgres. `name`* (≤ 50), `version "15"\|"16"` (immutable). Status: `host`, `port`, `ready`, `connectionSecretRef` → Secret `<name>-db-credentials` with `host/port/user/dbname/password/uri`. DB `appdb`, user `appuser`. Consumed via a workload's `connections.database`. |
| `redis-cache` v0.2.0 | Databases | internal | no | Ephemeral Redis. `name`* (≤ 63), `size small(64Mi)\|medium(256Mi)\|large(1Gi)`, `version "6"\|"7"`. Secret `<name>-credentials`, key `uri` (`redis://:<pw>@<name>:6379`, no TLS). Consumed via `connections.cache`. |
| `browser` v0.1.0 | Agent tools | optional | no | Headless Chromium + Playwright MCP, reached via data-plane `proxy` verb. |
| `searxng` | Search | | no | Backs agents' `web_search`. |
| `universal-coding-sandbox` | Development | internal | yes | Disabled unless the operator enables it. |

No bucket, secret, domain, or route templates exist. `env` maps are stored
world-readable; never put secrets there.

### Connections

`simple-webapp` (0.3.0), `worker` (0.2.0) and `cron-job` (0.2.0) take
`connections` — fixed slots, not a list, default `{}` / `""`:

| Slot | Value | Injected env | From |
|---|---|---|---|
| `connections.database` | `values.name` of a `database` instance (pattern `^$\|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, ≤ 50) | `DATABASE_URL` = `postgres://appuser:<pw>@<name>-db:5432/appdb` (no `sslmode`; in-cluster TLS is off, use `sslmode=disable`) | Secret `<name>-db-credentials`, key `uri` |
| `connections.cache` | `values.name` of a `redis-cache` instance (≤ 63) | `REDIS_URL` = `redis://:<pw>@<name>:6379` | Secret `<name>-credentials`, key `uri` |

Each slot renders a literal `secretKeyRef` env entry. Unset → placeholder
Secret with `optional: true`, the variable stays unset (an `env`-map value of
the same name still applies). Set → `optional: false`: the pod (or each
cron run) waits in `CreateContainerConfigError` until that Secret and key
exist, so it must name a real instance **in the same workspace** (all of a
workspace's instances share one runtime namespace; cross-workspace does not
work). A set connection wins over the `env` map. Slots apply in development
mode too (the dev overlay copies the literal env). Retry the first
connection and run migrations idempotently inside that retry loop. Never
pass a `database`/`redis-cache` credential through the `env` map (a
world-readable ConfigMap); use the slot.

## 5. URLs, TLS, access

- Host: `<hostnamePrefix|instance name>-<12 hex sha256(clusterID)>.<baseDomain>`;
  prefix ≤ 50 chars; URL `https://<host>`; base domain is platform config
  (`.faros.app` in the hosted portal copy). Custom domains are not supported.
- Exposure is a Gateway API `HTTPRoute` attached to the platform Gateway
  (default a Cloudflare Tunnel Gateway); TLS and DNS are handled at that edge.
- **A brand-new host fails TLS for several minutes.** Because the certificate
  is issued per hostname at that edge, a freshly created instance resolves in
  DNS but rejects the TLS handshake (curl exit 35, `sslv3 alert handshake
  failure`) until issuance completes. Measured on a dev hub: 0–9 minutes
  from promote to first 200 (eleven runs: ~0, ~0, 2 m 50 s, ~4, ~5, 5 m 20 s,
  5 m 30 s, ~7, 8 m 51 s). `status.phase` is already `Ready` and the pods are
  serving; only the edge certificate is missing, so there is nothing to fix.
  Distinguish it from a real failure by hitting an existing instance on the same
  base domain — if that serves and the new one does not, it is issuance —
  and confirm with
  `openssl s_client -connect <host>:443 -servername <host> </dev/null | openssl x509 -noout -subject`,
  which prints `Could not find certificate from <stdin>` while issuance is
  pending (that output is the signal) and a subject CN matching the host
  once the certificate exists.
  Root cause (`providers/infrastructure/docs/application-template-architecture.md`,
  "TLS for the app base domain"): Cloudflare Universal SSL covers only the
  zone apex and one level below. A base domain below the apex (e.g.
  `bob.faros.sh`) makes app hosts two levels deep, so each needs its own edge
  certificate. Operator fix: a `*.<baseDomain>` edge certificate (Advanced
  Certificate Manager / Total TLS) or a base domain that is itself a zone
  apex.
- Every publishable template routes through an infrastructure-owned
  `faros-access-proxy` gate. `values.access: public` is pure passthrough;
  `private` requires platform sign-in and a SubjectAccessReview on `get`
  of `instances/<name>/access`. Grants are a ClusterRole with two rules
  (the `instances/access` get, and kcp `access` on nonResourceURL `/`)
  plus a ClusterRoleBinding per user (`faros:<email>`). App Studio's
  publishing routes write these for you.
- Changing `access` is an in-place patch; no redeploy.

### App access tokens (programmatic access to private apps)

A browser gets a 302 to `/auth/apps/authorize`; a program must trade its
hub bearer **at the hub** for an app-bound token:

```
POST <hub>/auth/apps/token        Authorization: Bearer <hub token>
{"cluster":"<clusterName>","group":"infrastructure.faros.sh","resource":"instances","name":"<instance>","ttlSeconds":600}
→ 200 {"token":"fapp_…","expiresAt":"<RFC3339>","host":"<app host>"}
```

- `ttlSeconds` optional: default 600, allowed 60–900; the token never
  outlives the (JWT) hub token it was minted from. Bound to exactly one
  instance; sealed (AES-256-GCM), stateless, any hub replica verifies it.
- The hub runs the same SubjectAccessReview as a browser sign-in. Errors:
  400 `malformed token request`, 401 `invalid bearer token` (not a hub user
  credential; kcp ServiceAccount tokens, including the MCP connect token,
  are always refused), 403 `access denied`, 404
  `instance has no published host`, 429 `too many failed attempts`
  (`Retry-After: 3`; 20 failures per source address, then one per 3 s),
  503 `access policy unavailable` / `identity unavailable` /
  `token service unavailable`.
- Call the app with `Authorization: Bearer fapp_…`. The gate accepts
  **only** `fapp_` tokens: anything else (a raw hub token included) gets 401
  from the gate itself and is never relayed. The 401 body is
  `{"error":"invalid_token","message":…,"tokenEndpoint":"<hub>/auth/apps/token","instance":{"cluster","group","resource","name"}}`,
  so `curl -H 'Authorization: Bearer x' https://<app>/ | jq .instance` gives
  the mint coordinates.
- Any `Authorization: Bearer` request is answered 200/401/403/429/502,
  never 302. 403 `access_denied` = no grant; 502 `unavailable` = hub
  unreachable with no cached verdict (also when the gate's hub URL is plain
  http to a non-loopback host). The gate verifies via
  `POST <hub>/auth/apps/verify` (re-runs the SAR), caches allows for
  min(15 min, token lifetime) and refusals for 30 s, keyed by SHA-256 of the
  token; the upstream app never sees `Authorization`, and no session cookie
  is minted. So revocation takes effect within ≤ 15 min.

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
`access: private`; `access: public` is honored in development mode too.
The `exec` verb exists only on components whose template declares it
(`application`, `simple-webapp`); `worker` answers 404
`exec is not declared for component worker`.

Data-plane verbs (through the hub, as you; production instances answer 409).
`faros sandbox` ([cli.md](cli.md)) wraps them:

```
GET  /services/providers/infrastructure/dataplane/clusters/{cluster}/instances/{name}/status
GET  …/instances/{name}/components/{c}/log        (stream; bound it with timeout)
GET  …/instances/{name}/components/{c}/process    → {running, port, portReachable, sourceRevision, sourceDigest, syncEncodings[], …}
POST …/instances/{name}/components/{c}/sync       {files[{path,content,encoding?}], deletePaths[], restart ""|auto|always, sourceRevision?, sourceDigest?}
                                                   → {phase:"Synced", changed[], deleted[], reloadRuns[], restarted, sourceRevision, sourceDigest}
POST …/instances/{name}/components/{c}/restart
POST …/instances/{name}/components/{c}/env        {env:{KEY:value,…}} → {phase:"EnvUpdated", applied[], restarted:false}; live process env only (a kubectl change to values.env needs this plus restart to reach a running pod); GET is 405
POST …/instances/{name}/components/{c}/exec       start: header Idempotency-Key (required) + {action:"start", argv[], workdir?, timeoutSeconds ≤120, sourceRevision?, sourceDigest?} → {sessionID, requestID, state:"queued", sourceRevision, sourceDigest}
                                                   run:   Idempotency-Key optional + {action:"run", argv[], workdir?, timeoutSeconds?, sourceRevision?, sourceDigest?} → poll-shaped result
                                                   poll:  {action:"poll", sessionID} → {state queued|running|succeeded|failed|canceled|timed_out, exitCode, stdout, stderr, truncated}
                                                   cancel: {action:"cancel", sessionID}
```

Sync:

- Paths are relative to the component's `workspacePath`.
  `sourceRevision` and `sourceDigest` go together or not at all. With them
  the sync is **authoritative**: it replaces the component's managed file
  set, the revision must not go backwards, and the digest must equal sha256
  over `path` NUL `bytes` NUL for every file sorted by path, over **decoded**
  bytes (hex, an optional `sha256:` prefix is ignored), or the call is a 409
  (`workspace sync revision is older than the applied revision`,
  `workspace sync revision was already applied with a different digest`).
- A plain sync (no revision/digest) also stamps an applied revision: the
  managed set becomes previous manifest + written − deleted, hashed from
  disk after reload hooks, and the revision becomes previous+1 (or 1) only
  if the digest changed. The response carries `sourceRevision`/`sourceDigest`.
  Consequence: a writer mixing plain and authoritative syncs must continue
  numbering from the returned revision (App Studio does; see
  [app-studio.md](app-studio.md)).
- `files[].encoding`: `utf-8` (default) or `base64` (standard, padded, no
  line breaks, strict). Limits per request: 500 files, 25 MiB per file and
  48 MiB total decoded, 96 MiB JSON body. 413
  `sync request body exceeds 100663296 bytes; send fewer or smaller files per request`,
  or `sync request too large: …` for the decoded limits. Senders send base64
  only when `process`/`/status` lists `base64` in `syncEncodings`; otherwise
  `faros sandbox sync` and App Studio skip the binaries and `dev_sync`
  refuses the call.

Exec:

- `run` = start + poll inside the provider with backoff (250 ms → 1 s)
  until terminal or min(timeoutSeconds + 10 s, 90 s) (default timeout 120 s,
  so 90 s); on deadline it returns the non-terminal result (`state:"running"`
  with `sessionID`) — continue with `poll`. Leaving the request never cancels
  the command. Without `Idempotency-Key`, a body `requestID` or a random
  128-bit key is used. `start` requires the header.
- Omitting **both** `sourceRevision` and `sourceDigest` (start or run) runs
  against the component's currently applied revision, read from the dev
  agent's `/status`; one without the other is 400
  (`sourceDigest is required for start when sourceRevision is set (omit both to use the applied revision)`).
  No applied revision → 400
  `sourceRevision is required for start: component "<c>" reports no applied source revision — sync its workspace first (dev_sync, or POST .../components/<c>/sync), wait for any dependency reload to finish, then retry; or pass sourceRevision and sourceDigest explicitly`;
  status unreadable → 502. Start and run results include the
  `sourceRevision`/`sourceDigest` used.
- Bad action: `action must be "start", "run", "poll", or "cancel"`.
- Exec runs argv in a separate stateless executor: no shell (pass
  `sh -c '…'` yourself), none of the app's own environment (`DATABASE_URL`,
  user env, secrets). Env is `HOME LANG PATH PWD TMPDIR NPM_CONFIG_CACHE`
  plus `PORT` (the component's dev port, so `localhost:$PORT` reaches the
  dev server) and `FAROS_COMPONENT`.
  Working directory the component workspace. Exec is refused until the
  instance is Ready and its network phase is `runtime`.
- There is no metrics surface.

Workspace verb (template-declared `workspace`, used by App Studio's
per-run coding sandbox on `universal-coding-sandbox`; dev agent
`/workspace/{seed|list|read|mutate|diff|checkpoint}`). Binary behavior:
files > 1 MiB or not UTF-8 are *opaque* (tracked by digest and size,
never returned as text). `read` answers 413
`read "<p>" is N bytes (sha256 …), above the 1048576-byte text limit; …` or
422 `read "<p>" is not UTF-8 text: it is a binary file (…); binary files are opaque to the workspace API`.
Checkpoints record opaque files as `opaqueFiles[{path,digest,bytes}]`;
restore is 409 when such a file is missing or changed
(`… is recorded by digest only and the workspace copy …; sync that file back first`).
Seed/mutate cap: 512 managed files.

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
| `dev_sync` | `instance`, `files[{path,content,encoding?}]`, `restart? auto\|none` | Plain (non-authoritative) sync. `encoding` `utf-8`\|`base64`; ≤ 500 files, 48 MiB decoded, 25 MiB per binary. Base64 files are checked first against every target component's `syncEncodings`; if any lacks `base64`, nothing is synced (`nothing was synced — component "<c>" cannot receive binary files (…): <paths>. …`). Components with no routed files are not called. Paths must fall under a declared component `workspacePath`; toolchain manifest validated (a node component needs `package.json`) |
| `dev_exec` | `instance`, `component?` (omit when the template has one), `argv[]`, `workdir?`, `timeoutSeconds?` (≤ 120), `idempotencyKey?` | Data-plane `run` against the applied revision (no revision sent). Returns `{instance, component, sessionID, requestID, state, exitCode, stdout, stderr, truncated, sourceRevision, sourceDigest, hint?}`; non-terminal after ~90 s → `hint` says to repeat the same call with `idempotencyKey` = the returned `requestID`. Env: `PORT`, `FAROS_COMPONENT`, not the app's env. |
| `dev_logs` | `instance`, `component`, `maxBytes?` (65536, max 262144) | Tail |
| `dev_restart` | `instance`, `component` | |

Provider-direct endpoint: `https://<hub>/services/providers/infrastructure/mcp`.
An org-scoped (BYO) `infrastructure` appears on the aggregate only for human
bearers — see [mcp-and-edges.md](mcp-and-edges.md).

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
