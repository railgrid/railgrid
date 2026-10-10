# Infrastructure provider reference

Group `infrastructure.railgrid.ai/v1alpha1`.

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

Every runtime verb on an instance (dev sandbox control, the `proxy` into a
browser or searxng instance) is a kcp **custom subresource** `instances/<verb>`
on the hub's kcp front door, authorized by your workspace RBAC:

```
/clusters/{clusterID}/apis/infrastructure.railgrid.ai/v1alpha1/instances/{name}/{verb}[/{tail}][?component={c}]
```

There is no provider REST surface; the provider's own origin serves only
`/mcp` (section 9).

## 2. Instance

```yaml
apiVersion: infrastructure.railgrid.ai/v1alpha1
kind: Instance
metadata:
  name: demo
  labels: { railgrid.ai/template: application }     # attribution label; consumers stamp it
spec:
  template: application        # required, immutable (CEL self == oldSelf)
  values: { … }                # template-shaped; NOT validated at admission
  imagePullSecretRef: { name: demo-registry }        # optional; section 6
  oidcBridgeSecretRef: { name: acme-oidc }           # optional; only with values.oidc.mode: byo
status:
  phase: Pending|Ready|Failed
  template, templateVersion, observedGeneration, message, railgridNetworkPhase
  conditions: Valid (InvalidValues | TemplateNotFound), SecretsBridged (Bridged | SecretRefNotFound | SecretRefInvalid | BridgeSecretRefMissing), OIDCConfigured, Ready, ResourcesReady
  url, host, ready, runtimeNamespace, runtimeRef, components, outputs, controlSecretRef, connectionSecretRef, redirectURL   # projected per template
  apiServiceRef, webServiceRef, webReady, apiReady, databaseReady, oauthReady   # application: {name, namespace} of the in-namespace api/web Services
  appServiceRef                                      # simple-webapp: its Service
  schedule                                           # cron-job
```

The `*ServiceRef` names are how other instances in the workspace reach an app
without the access gate (section 5, "Machine callers"). A `cron-job` status
has `phase`, `runtimeRef` and `schedule`: no last-run time, exit code or logs.

Printer columns: Template, Phase, Ready, Age (URL only with `-o wide`).
Finalizer `instances.infrastructure.railgrid.ai/runtime`. Invalid values are
admitted and reported as `Valid=False/InvalidValues`; the last good runtime
keeps running.

Platform-reserved `spec.values` keys you must not set: `railgridMode` (except
explicitly `development`), `railgridActions*`, `railgridNetworkPhase`,
`expose.fqdn`, `railgridCluster`, `credentialsSecretName`, `railgridRedeployRevision`.

## 3. Template

Fields a consumer cares about: `displayName`, `description`, `category`,
`version`, `exposure internal|optional|public` (default `internal`),
`schema` (JSON Schema for `values`), `sampleValues`, `agent {usage, prerequisites[], outputs[]}`
(read `usage` before writing code), `view`, `dataPlane` (verbs available on
instances, per component, with `exec {maxTimeoutSeconds, maxOutputBytes}`),
`development` (dev-mode contract: `components{<name>: {workspacePath,
imageInput, devImage, workingDir, startCommand, port, reload{strategy,
rules[{paths, command}]}}}`, `scaffold {repository, ref}`,
`build.workflowPath`, `maxLifetimeSeconds`, `idleTimeoutSeconds`, each
≤ 604800 = 7 days). The annotation `railgrid.ai/immutable-inputs` lists
value paths `update_instance` refuses to change.

```bash
kubectl get templates
kubectl get template application -o jsonpath='{.spec.agent.usage}'
kubectl get template application -o jsonpath='{.spec.schema}' | jq .
kubectl get template application -o jsonpath='{.spec.sampleValues}'
```

## 4. Shipped templates

| Template | Category | Exposure | Dev-capable | What it is |
|---|---|---|---|---|
| `simple-webapp` v0.3.0 | Workloads | public | yes (Node.js, component `app`, `workspacePath .`) | One container, one port, public URL. Values: `name`*, `image` (required in production, ignored in dev), `port` (8080), `replicas` 1..10, `env` map, `connections {database, cache}`, `expose.hostnamePrefix`, `access public\|private` (default public). Status: `url`, `host`, `ready`, `appServiceRef`. Dev: `npm run dev` with a vite shim, else `npx vite`; a lockfile/`package.json` change triggers `npm install`. |
| `application` v0.1.0 | Workloads | public | yes (Node.js, components `web` → `web/`, `api` → `api/`) | `web` + `api` + Postgres on one host; `/api/*` routed to the api container with the path preserved. Values: `name`*, `webImage`/`apiImage` (default to the scaffold hello-world images; ignored in dev), `webPort`/`apiPort` (8080), `webEnv`/`apiEnv` maps, `webReplicas`/`apiReplicas` 1..10, `database {version "15"\|"16" (default 16), size small(1Gi)\|medium(5Gi)\|large(20Gi)}` (both immutable), `oidc {mode none\|byo, issuerURL, clientID, scopes}` (dev preview auth only), `access`, `expose.hostnamePrefix`. **No `connections`**: the api gets only its own `DATABASE_URL` (Secret `<name>-db-credentials`), so it can't use a `redis-cache`. Contract: bind `0.0.0.0`, honor `$PORT`, api reads `DATABASE_URL` (`postgres://appuser:…@host:5432/appdb`, `sslmode=disable`), DB starts empty, retry first connect, frontend calls `/api/*` same-origin. Dev: `api/` needs a `package.json` with a `dev` or `start` script (`npm run dev \|\| npm start`); `web/` a vite `package.json`. Status: `url`, `host`, `webReady`, `apiReady`, `databaseReady`, `oauthReady`, `redirectURL` (BYO), `webServiceRef`, `apiServiceRef`. |
| `worker` v0.2.0 | Workloads | internal | yes (Node.js, component `worker`, `workspacePath .`) | Deployment only, no Service. `name`*, `image` (production), `replicas` 1..5, `env`, `connections {database, cache}`. Dev process `npm run dev \|\| npm start`. The `worker` component declares sync/log/restart/env/process but **no `exec`**: `exec` answers 404 `exec is not declared for component worker`. Status: `ready`. |
| `cron-job` v0.3.0 | Workloads | internal | no | `name`*, `image`*, `schedule` (`"0 * * * *"` UTC, 5-field), `command[]`, `args[]` (0.3.0+; override the image entrypoint/CMD, so a stock image runs a one-liner), `env`, `connections {database, cache}`. `concurrencyPolicy: Forbid`, `backoffLimit: 3`, history 3 successful / 1 failed. **On a 0.2.0 catalog there is no `command`/`args`**: the entrypoint does the work; with a public image drive it via `env` (e.g. `node:20-alpine` + `NODE_OPTIONS=--import=data:text/javascript;base64,…`: the default `node` command runs the preload, then exits on the empty stdin; use top-level `await` and `process.exit(code)`). **Runs are not observable**: status is only `schedule`, no logs, run times or exit codes anywhere, so test locally and have each run write evidence you can read. |
| `database` v0.1.0 | Databases | internal | no | Standalone Postgres. `name`* (≤ 50, DNS label), `version "15"\|"16"` (default 16, immutable). Status: `host`, `port`, `ready`, `connectionSecretRef` → Secret `<name>-db-credentials` with `host/port/user/dbname/password/uri`. DB `appdb`, user `appuser`. Consumed via a workload's `connections.database`. |
| `redis-cache` v0.2.0 | Databases | internal | no | Ephemeral Redis (no persistence). `name`*, `size small(64Mi)\|medium(256Mi)\|large(1Gi)`, `version "6"\|"7"` (default 7). Secret `<name>-credentials`, keys `host/port/password/uri` (`redis://:<pw>@<name>:6379`, no TLS). Consumed via `connections.cache`. |
| `browser` v0.1.0 | Agent tools | optional | no | Headless Chromium + Playwright MCP. `name`*, `size small(1Gi)\|medium(2Gi)\|large(4Gi)`, `expose.enabled` (default false; needs `oidc.issuerURL` + `clientID`, mode `byo` only). The `proxy` verb root **is** the MCP streamable-HTTP endpoint (pinned to `/mcp` upstream; GET/POST/DELETE; no suffix). One Chromium per instance, stateful, single replica. |
| `searxng` v0.1.0 | Agent tools | optional | no | Private SearXNG with JSON API. `name`*, `size small(256Mi)\|medium(512Mi)\|large(1Gi)`, `expose`/`oidc` as browser. `GET …/instances/<name>/proxy/search?q=<query>&format=json` (GET only, caller supplies the tail). Backs agents' `web_search`. |
| `universal-coding-sandbox` v0.1.0 | Development | internal | yes (component `workspace`, image `${railgrid.devImage.universal}`, `sleep infinity`) | Platform-owned; disabled unless the operator sets `RAILGRID_CODING_SANDBOX_ENABLED=true` with digest-pinned universal + dev-agent images (hosted installs keep it off). `name`* only. `maxLifetimeSeconds`/`idleTimeoutSeconds` 43200 (12 h). Default-deny egress; `runtime` phase allows DNS + TCP 443 to public IPv4 only. Verbs `workspace`, `sync`, `exec`, `restart`, `log`, `process`. |

No bucket, secret, domain, or route templates exist. `env` maps are stored
world-readable; never put secrets there.

### Your own secrets

No workload template takes a reference to a Secret you create: there is no
`secretRef`/`envFrom`/`secretEnv` input, and `env`/`webEnv`/`apiEnv` are
world-readable ConfigMaps. The only Secrets that reach a container are the
platform's own: the `connections` slots below (`<name>-db-credentials`,
`<name>-credentials`) and an `application` api's `<name>-db-credentials`.
So for an app secret (an API key, a shared ingest token):

- Keep it in the app's database and set it through an authenticated route
  (for a private app, from your shell with an app token).
- For a secret two workloads must share, derive it from a credential both
  already receive: e.g. a `cron-job` with `connections.database: <app>-prod`
  gets the same `DATABASE_URL` as the app's api, so both can compute
  `HMAC-SHA256(db password, "<purpose>")`. It changes when that credential
  does.

Never put the value in `env`, a prompt or a commit.

### Connections

`simple-webapp` (0.3.0), `worker` (0.2.0) and `cron-job` (0.2.0) take
`connections` — fixed slots, not a list, default `{}` / `""`. Read
`spec.version` and `spec.schema.properties` of the live Template before
relying on it. `application` does not. `connections.database` also accepts an
`application` instance's name, because that instance's Secret
`<name>-db-credentials` exists too:

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
  (the portal's publish dialog shows it). Custom domains are not supported.
- Exposure is a Gateway API `HTTPRoute` attached to the platform Gateway
  (by default a tunnel gateway at the hub's CDN); TLS and DNS are handled at that edge.
- The dev-mode `access: private` URL redirects anonymous requests to the hub
  sign-in like production; `connections` can be added to a running dev-mode
  instance with `kubectl patch` (pod re-rendered, synced files kept).
- **A brand-new host can fail TLS for several minutes.** Because the
  certificate is issued per hostname at that edge, a freshly created instance
  resolves in DNS but rejects the TLS handshake (curl exit 35, `sslv3 alert
  handshake failure`) until issuance completes; on one dev hub this took
  0–9 minutes. `status.phase` is already `Ready` and the pods are serving;
  only the edge certificate is missing, so there is nothing to fix.
  Distinguish it from a real failure by hitting an existing instance on the
  same base domain — if that serves and the new one does not, it is
  issuance — and confirm with
  `openssl s_client -connect <host>:443 -servername <host> </dev/null | openssl x509 -noout -subject`,
  which prints `Could not find certificate from <stdin>` while issuance is
  pending and a subject CN matching the host once the certificate exists.
  Root cause (`providers/infrastructure/docs/application-template-architecture.md`,
  "TLS for the app base domain"): the CDN's default certificate covers only
  the zone apex and one level below. A base domain below the apex (e.g.
  `apps.example.com`) makes app hosts two levels deep, so each needs its own
  edge certificate. Operator fix: a `*.<baseDomain>` edge certificate or a
  base domain that is itself a zone apex.
- Every publishable template routes through an infrastructure-owned
  `railgrid-access-proxy` gate. `values.access: public` is pure passthrough;
  `private` requires platform sign-in and a SubjectAccessReview for verb
  `get` on `instances/<name>/access` as `railgrid:<email>`. App Studio's
  publishing routes write the grant for you: a ClusterRole
  `railgrid-app-access-…` with two rules (`get` on resource `instances/access`,
  `resourceNames: [<instance>]`, and kcp verb `access` on nonResourceURL `/`,
  which lets an invited outsider into the workspace authorizer at all) plus
  a ClusterRoleBinding per user. Grants are listed and revoked at
  `GET|DELETE /api/orgs/{org}/workspaces/{ws}/app-access[/{binding}]`
  (revoke needs workspace admin).
- App Studio's publishing mode `restricted` (aliases `members`, `private`)
  is `access: private` at the gate plus an invite-only policy recorded on
  the Project; the gate treats them identically. "Unpublish" = private with
  no grants (the URL never goes away).
- Changing `access` is an in-place patch; no redeploy. A raw Instance
  defaults to `access: public`; App Studio's `<project>-dev` sets it from the
  project's preview sharing mode (private unless public).
- **Machine callers.** Only people pass a `private` gate: a browser sign-in,
  or a `fapp_` token (below) minted from a user's own hub JWT and valid for
  ≤ 15 min. ServiceAccount tokens can't mint one, so a `cron-job`, `worker`,
  CI job or hosted agent has no unattended way through the gate. Inside the
  workspace it doesn't need one: all instances share the runtime namespace
  `<clusterID>-default`, so a workload calls the app's Service directly and
  never meets the gate:
  `application` → `http://<name>-api:<apiPort>` / `http://<name>-web:<webPort>`
  (`status.apiServiceRef` / `webServiceRef`), `simple-webapp` →
  `http://<name>:<port>` (`status.appServiceRef`). Such routes are open to
  every workload in the workspace, so authenticate them in the app. The
  optional ingress NetworkPolicy `railgrid-tenant-isolation` (chart
  `tenantNetworkPolicy.enabled`, off by default) blocks *other* workspaces'
  pods, never same-workspace ones. Hosted agents run outside the workspace
  and can reach only public apps.

### App access tokens (programmatic access to private apps)

A browser gets a 302 to `/auth/apps/authorize`; a program acting for a
signed-in user trades that user's hub bearer **at the hub** for an app-bound
token (unattended callers: "Machine callers" above):

```
POST <hub>/auth/apps/token        Authorization: Bearer <hub token>
{"cluster":"<clusterID>","group":"infrastructure.railgrid.ai","resource":"instances","name":"<instance>","ttlSeconds":600}
→ 200 {"token":"fapp_…","expiresAt":"<RFC3339>","host":"<app host>"}
```

- `ttlSeconds` optional: default 600, allowed 60–900; the token never
  outlives the (JWT) hub token it was minted from. Bound to exactly one
  instance; sealed (AES-256-GCM), stateless, any hub replica verifies it.
- The hub runs the same SubjectAccessReview as a browser sign-in. Errors
  (JSON `{"error":…}`): 400 `malformed token request`, 401 `invalid bearer
  token` (not a hub user credential; kcp ServiceAccount tokens, including
  the MCP connect token, are always refused), 403 `access denied`, 404
  `instance has no published host`, 429 `too many failed attempts`
  (`Retry-After: 3`; 20 failures per source address, then one per 3 s),
  503 `access policy unavailable` / `identity unavailable` /
  `instance host unavailable` / `token service unavailable`.
- Call the app with `Authorization: Bearer fapp_…`. The gate accepts
  **only** `fapp_` tokens: anything else (a raw hub token included) gets 401
  from the gate itself and is never relayed. The 401 body is
  `{"error":"invalid_token","message":…,"tokenEndpoint":"<hub>/auth/apps/token","instance":{"cluster","group","resource","name"}}`
  (plus `WWW-Authenticate: Bearer realm="railgrid", error="invalid_token"`),
  so `curl -H 'Authorization: Bearer x' https://<app>/ | jq .instance` gives
  the mint coordinates.
- Any `Authorization: Bearer` request is answered 200/401/403/429/502,
  never 302. 403 `access_denied` = no grant; 429 `rate_limited` relays the
  hub's `Retry-After`; 502 `unavailable` = hub unreachable with no cached
  verdict (also when the gate's hub URL is plain http). The gate verifies via
  `POST <hub>/auth/apps/verify` (re-runs the SAR), caches allows for
  min(15 min, token lifetime) and refusals for 30 s, keyed by SHA-256 of the
  token; the upstream app never sees `Authorization`, and no session cookie
  is minted. So revocation takes effect within ≤ 15 min.

## 6. Private images and secrets

Two typed references on `Instance.spec` name Secrets in namespace `default`
of your workspace. The provider reads them through a label-scoped claim, so
**each Secret must carry the label `railgrid.ai/owner: infrastructure`**; an
unlabelled one is invisible and reports `SecretsBridged=False/SecretRefNotFound`
exactly like a missing one. There is no name convention any more: an unset
reference means "pulls from a public registry" / "no BYO OIDC".

- Pull secret: `spec.imagePullSecretRef: {name: <secret>}` → a
  `kubernetes.io/dockerconfigjson` Secret (key `.dockerconfigjson`, else
  `SecretRefInvalid`). The controller bridges it into the runtime namespace
  as `<instance>-registry` and attaches it to the default ServiceAccount, so
  every pod of every component can pull. App Studio mints
  `<instance>-registry` itself at promote from the code Connection and sets
  the ref on `<project>-prod`.

  ```bash
  gh auth token | kubectl create secret docker-registry gosvc-registry -n default \
    --docker-server=ghcr.io --docker-username=<github user> --docker-password-stdin
  kubectl label secret gosvc-registry -n default railgrid.ai/owner=infrastructure
  # then: spec.imagePullSecretRef: {name: gosvc-registry}
  ```

  A runtime cluster whose nodes already hold a registry credential may pull
  a private image with no ref at all; `ImagePullBackOff` in `status.message`
  means you need one. Two Instances may share one pull Secret.
- BYO OIDC client secret (`values.oidc.mode: byo`): `spec.oidcBridgeSecretRef:
  {name: <secret>}` with key `oidc_client_secret`, bridged as
  `cloud-credentials-<instance>` for the template's oauth2-proxy. `byo`
  without a ref reports `SecretsBridged=False/BridgeSecretRefMissing`. The
  template prose still says "a Secret named `cloud-credentials`"; the
  controller reads only the ref.

## 7. Limits

No ResourceQuota. Every tenant runtime namespace gets a create-only
`LimitRange` `railgrid-defaults`: request 50m/128Mi, limit 500m/512Mi, max
2 CPU / 2Gi per container (chart `tenantLimitRange.enabled`). Template-level
ceilings: `replicas` 1..10 (1..5 for `worker`). A dev sandbox is reaped at
its template's `maxLifetimeSeconds` (from creation) or `idleTimeoutSeconds`
(since the last authorized data-plane call) when the template declares them
(≤ 7 days; only `universal-coding-sandbox` ships them, 12 h each).
Sandboxes run PSS-restricted, non-root UID 1000, seccomp RuntimeDefault, all
capabilities dropped.

## 8. Development mode

`values.railgridMode: "development"` on a dev-capable template gives a live
sandbox: image inputs may be omitted, declared components run a dev image
with hot reload, and files are pushed with the data plane or MCP. Node.js is
the only toolchain in the shipped dev images (the universal sandbox adds Go
and Python). `access: public` is honored in development mode too. The `exec`
verb exists only on components whose template declares it (`application`,
`simple-webapp`, `universal-coding-sandbox`); `worker` answers 404
`exec is not declared for component worker`.

Data-plane verbs (kcp custom subresources on the hub, as you; the component
rides in `?component=`, never in the path; production instances answer 409).
`railgrid sandbox` ([cli.md](cli.md)) wraps them:

```
BASE = /clusters/{clusterID}/apis/infrastructure.railgrid.ai/v1alpha1/instances/{name}
GET  BASE/runtime-status                      → the Instance's status object
GET  BASE/log?component={c}                   current attempt's dev-process output (ring of 500 lines)
GET  BASE/process?component={c}               → {attemptID, configured, running, port, portReachable, sourceRevision, sourceDigest, syncEncodings[], …}
POST BASE/sync?component={c}                  {files[{path,content,encoding?}], deletePaths[], restart ""|auto|always, sourceRevision?, sourceDigest?}
                                              → {phase:"Synced", changed[], deleted[], reloadRuns[], reloadError?, restarted, sourceRevision, sourceDigest}
POST BASE/restart?component={c}               → {restarted:true}
POST BASE/env?component={c}                   {env:{KEY:value,…}} → {phase:"EnvUpdated", applied[], restarted:false}; GET is 405
POST BASE/exec?component={c}                  start: header Idempotency-Key (required) + {action:"start", argv[], workdir?, timeoutSeconds ≤120, sourceRevision?, sourceDigest?} → {sessionID, requestID, state:"queued", sourceRevision, sourceDigest}
                                              run:   Idempotency-Key optional + {action:"run", …same fields…} → poll-shaped result
                                              poll:  {action:"poll", sessionID} → {state queued|running|succeeded|failed|canceled|timed_out, exitCode, stdout, stderr, truncated}
                                              cancel: {action:"cancel", sessionID}
GET|POST BASE/workspace/{op}?component=workspace   universal-coding-sandbox only (below)
*    BASE/proxy[/{tail}]                      browser (MCP endpoint, GET/POST/DELETE) and searxng (GET, e.g. /proxy/search?q=…&format=json)
```

`railgrid sandbox sync <i> <c> [dir]` (authoritative, `--restart auto|always`),
`exec <i> <c> -- <argv…>` (`--timeout` ≤ 120s, `--workdir`), `logs <i> <c> [-f]`,
`restart <i> <c>`, `env <i> <c> KEY=value… [--restart]`, `status <i> [<c>]`
(instance → `runtime-status`; component → `process`, printed as `Running:`,
`Port:`, `Source:`, `Sync:` lines).

Sync:

- Paths are relative to the component's `workspacePath`; `.git`,
  `node_modules` and `.assistant-snapshots` path components are refused.
  `sourceRevision` and `sourceDigest` go together or not at all. With them
  the sync is **authoritative**: it replaces the component's managed file
  set, the revision must not go backwards, and the digest must equal sha256
  over `path` NUL `bytes` NUL for every file sorted by path, over **decoded**
  bytes (hex, an optional `sha256:` prefix is ignored), or the call is a 409
  (`workspace sync revision is older than the applied revision`,
  `workspace sync revision was already applied with a different digest`).
  `railgrid sandbox sync` uses Unix seconds as the revision (bumped past the
  applied one) and `git ls-files -co --exclude-standard` as the file list.
- A plain sync (no revision/digest) also stamps an applied revision: the
  managed set becomes previous manifest + written − deleted, hashed from
  disk after reload hooks, and the revision becomes previous+1 (or 1) only
  if the digest changed. The response carries `sourceRevision`/`sourceDigest`.
  Consequence: a writer mixing plain and authoritative syncs must continue
  numbering from the returned revision (App Studio does; see
  [app-studio.md](app-studio.md)).
- `files[].encoding`: `utf-8` (default; must be UTF-8 without NUL) or
  `base64` (standard, padded, no line breaks, strict). Limits per request:
  500 files, 25 MiB per file and 48 MiB total decoded, 96 MiB JSON body. 413
  `sync request body exceeds 100663296 bytes; send fewer or smaller files per request`,
  or `sync request too large: …` for the decoded limits. Senders send base64
  only when `process` lists `base64` in `syncEncodings`; otherwise
  `railgrid sandbox sync` skips the binaries with a warning, `railgrid app
  sync` reports them `binary-unsupported`, and `dev_sync` refuses the call.

Exec:

- `run` = start + poll inside the provider with backoff (250 ms → 1 s)
  until terminal or min(timeoutSeconds + 10 s, 90 s) (default timeout 120 s,
  so 90 s); on deadline it returns the non-terminal result (`state:"running"`
  with `sessionID`) — continue with `poll`. Leaving the request never cancels
  the command. Without `Idempotency-Key`, a body `requestID` or a random
  128-bit key is used. `start` requires the header.
- Body limits: ≤ 512 KiB, `argv` 1..64 entries of ≤ 4096 bytes, `workdir`
  ≤ 256 bytes and workspace-relative, output truncated at the template's
  `maxOutputBytes` (256 KiB on every shipped template).
- Omitting **both** `sourceRevision` and `sourceDigest` (start or run) runs
  against the component's currently applied revision, read from the dev
  agent's `/status`; one without the other is 400
  (`sourceDigest is required for start when sourceRevision is set (omit both to use the applied revision)`).
  No applied revision → 400
  `sourceRevision is required for start: component "<c>" reports no applied source revision — sync its workspace first (dev_sync, or POST .../instances/<name>/sync?component=<c>), wait for any dependency reload to finish, then retry; or pass sourceRevision and sourceDigest explicitly`;
  status unreadable → 502. Start and run results include the
  `sourceRevision`/`sourceDigest` used. `railgrid sandbox exec` reads the
  revision from `process` first and refuses with
  `<i>/<c> has no source revision; run 'railgrid sandbox sync <i> <c> <dir>' first (exec needs an authoritative sync)`
  (or `run 'railgrid app sync <project>' first` for an App Studio-managed
  instance).
- Bad action: `action must be "start", "run", "poll", or "cancel"`.
- Exec runs argv in a separate stateless executor: no shell (pass
  `sh -c '…'` yourself), none of the app's own environment (`DATABASE_URL`,
  user env, secrets). Env is `HOME=/tmp LANG PATH PWD TMPDIR NPM_CONFIG_CACHE`
  plus `PORT` (the component's dev port, so `localhost:$PORT` reaches the
  dev server) and `RAILGRID_COMPONENT`. Working directory the component
  workspace. Exec is refused (409) until the instance is Ready at its
  current generation (and, for the universal sandbox, network phase
  `runtime`).
- There is no metrics surface.

Env verb: live process only (a kubectl change to `values.env` needs this
plus `restart` to reach a running pod); ≤ 32 keys per call; names must be
`[A-Za-z0-9_]`; names containing `SECRET`, `TOKEN`, `PASSWORD`, `PASSWD`,
`APIKEY`, `API_KEY`, `PRIVATE_KEY`, `CREDENTIAL`, `ACCESS_KEY`, or equal to
`KEY` / ending `_KEY` are refused (`secret-looking environment variable …
cannot be set through /env`).

Workspace verb (`universal-coding-sandbox` only, component `workspace`; dev
agent `/workspace/{seed|list|read|mutate|diff|checkpoint}`, `seed` = the
authoritative `/sync`). Binary behavior: files > 1 MiB or not UTF-8 are
*opaque* (tracked by digest and size, never returned as text). `read`
answers 413
`read "<p>" is N bytes (sha256 …), above the 1048576-byte text limit; …` or
422 `read "<p>" is not UTF-8 text: it is a binary file (…); binary files are opaque to the workspace API`.
Checkpoints record opaque files as `opaqueFiles[{path,digest,bytes}]`;
restore is 409 when such a file is missing or changed
(`… is recorded by digest only and the workspace copy …; sync that file back first`).
Managed-file cap: 512; a mutation > 1 MiB is 413.

## 9. MCP tools (`infrastructure__*`)

| Tool | Input | Notes |
|---|---|---|
| `list_templates` | `category?`, `cloud?` | `{templates:[{name, displayName, description, category, cloud, version, kind, exposure}]}`; `internal` never gets a URL |
| `describe_template` | `name`, `version?` | `{name, version, kind, backend, category, exposure, inputsSchema, sampleValues, agent{usage}, development{components{<c>{port, startCommand, toolchain, workspacePath}}}, immutableInputs}`. The inputs are `inputsSchema.properties` (not `schema`). Check `exposure` before promising a URL. |
| `provision` | `template`, `templateVersion?`, `name`, `values` | Creates the Instance CR as you. Not idempotent: an existing name errors `instance "<n>" already exists — use update_instance …`. |
| `list_instances` | none | `{instances:[{name,template,phase,message}]}` |
| `get_instance` | `name` | Full instance with conditions and child status |
| `update_instance` | `name`, `values` (RFC 7386 merge patch) | Roll a new image, scale, change env or schedule. Rejected for `name`, `railgridMode`, `expose`, `credentialsSecretName` and the template's `railgrid.ai/immutable-inputs` (`value "<p>" cannot be changed on a live instance (immutable: …); recreate the instance to change it`). |
| `delete_instance` | `name` | Destructive, idempotent (`deleted:true` even if gone) |
| `dev_sync` | `instance`, `files[{path,content,encoding?}]`, `restart? auto\|none` | Plain (non-authoritative) sync: adds and overwrites files, never removes any. App Studio's `<project>-dev` normally gets its files from git (`railgrid app sync`); `dev_sync` is the way in when App Studio's hydrate is broken (SKILL.md 4.4), since it touches neither the workspace store nor git. `encoding` `utf-8`\|`base64`; ≤ 500 files, 48 MiB decoded, 25 MiB per binary. Paths are workspace-relative and routed by each component's `workspacePath` (`web/src/App.jsx` → component `web` as `src/App.jsx`); none routable → `none of the N files are under a development component directory (…)`. Base64 files are checked first against every target component's `syncEncodings`; if any lacks `base64`, nothing is synced (`nothing was synced — component "<c>" cannot receive binary files (…): <paths>. …`). Components with no routed files are not called. A fresh `node` component (no applied source, no running process) must receive `package.json` in the call (`component "<c>" runs a node development sandbox but <dir> has no package.json — …`); a partial sync into a working sandbox does not. Returns `{instance, components:{<c>:{files, response}}}`. |
| `dev_exec` | `instance`, `component?` (omit when the template has one), `argv[]`, `workdir?`, `timeoutSeconds?` (≤ 120), `idempotencyKey?` | Data-plane `run` against the applied revision (no revision sent). Returns `{instance, component, sessionID, requestID, state, exitCode, stdout, stderr, truncated, sourceRevision, sourceDigest, hint?}`; non-terminal after ~90 s → `hint` says to repeat the same call with `idempotencyKey` = the returned `requestID`. Env: `PORT`, `RAILGRID_COMPONENT`, not the app's env. |
| `dev_logs` | `instance`, `component`, `maxBytes?` (65536, max 262144) | Tail; `{instance, component, log, truncated}` |
| `dev_restart` | `instance`, `component` | `{instance, component, response}` |

The dev tools refuse a template with no `development` block, an instance not
in `railgridMode: development`, and an unknown component (listing the valid
names). Provider-direct endpoint:
`https://<hub>/services/providers/infrastructure/mcp`. An org-scoped (BYO)
`infrastructure` appears on the aggregate only for human bearers — see
[mcp-and-edges.md](mcp-and-edges.md).

## 10. Relationship to App Studio

App Studio's dev environment is an `Instance` `<project>-dev` with
`railgridMode: development`; promotion writes `<project>-prod` with
`railgridMode: production`, digest-pinned images and
`spec.imagePullSecretRef: {name: <project>-prod-registry}`. Both are
ordinary instances you can inspect with kubectl (label
`app-studio.railgrid.ai/project: <project>`). Everything App Studio deploys
you can deploy yourself with the same YAML; App Studio adds the git
scaffold, CI, digest pinning, naming, sharing, and the sandbox loop.

## 11. Self-hosting

The chart (`oci://ghcr.io/railgrid/charts/railgrid-infrastructure-provider`,
namespace `railgrid-provider-infrastructure`) runs in operator mode:
`operator.enabled=true`, `operator.providerKubeconfigSecret.{name,key}`,
`catalogEntry.enabled=false`, `hub.url`, plus — for app publishing —
`operator.application.baseDomain`, `operator.application.gateway.{name,namespace}`
(an existing Gateway API Gateway in your cluster), `operator.publishing.hubPublicURL`
and `operator.publishing.accessProxyImage`; the coding sandbox needs
`codingSandbox.enabled=true` with digest-pinned `development.images.universal`
and `development.agentImage` (`spec.serving.selfHosting` in the CatalogEntry;
values reference in the chart README). Without the publishing values,
templates that publish an app fail their reconcile and everything else still
provisions. The kcp shard's virtual workspace URL must be reachable from that
cluster or Instances never reconcile. Edge clusters are **not** targets for
this provider; edge placement is the edges provider's `Workload` and
`Placement` kinds.
