# code provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`railgrid/provider-code`](https://github.com/railgrid/provider-code)
> repository is **automatically synced** from the railgrid monorepo
> [`railgrid/railgrid`](https://github.com/railgrid/railgrid) (path `providers/code/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`railgrid/railgrid`](https://github.com/railgrid/railgrid) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

A railgrid provider that manages source-code repositories and their access —
deploy keys, collaborators, and (read-only) published packages — across git
hosting providers (**GitHub** today) on behalf of railgrid tenants. A tenant adds a
**Connection** (a credential for one git account) in the railgrid portal — or via
an MCP-driven LLM — then declares **Repositories**, **DeployKeys**, and
**Collaborators** as Kubernetes-style resources in their own kcp workspace. The
provider's controllers reconcile those into real GitHub state.

## What's here

| Surface | Where |
|---|---|
| Git host backend | `backend/` — the `GitBackend` seam + `backend/github/` (go-github) |
| Controllers | `controller/{connection,repository,deploykey,collaborator,packages}/` — one multicluster manager across all tenant workspaces |
| API types | `apis/v1alpha1/` — Connection / Repository / DeployKey / Collaborator (tenant-authored) + Package (crawler-authored) CRDs |
| MCP transport | `mcpserver/` — `/mcp`, `/mcp/sse` (list + write tools) |
| GitHub OAuth | `oauthgithub/` — the "Connect with GitHub" popup flow |
| Portal micro-frontend | `portal/` — Vue 3 connections, repositories, repo detail (deploy keys, collaborators, packages) |
| Helm chart | `deploy/chart/` — provider Deployment + Service + CatalogEntry |
| CatalogEntry (raw) | `manifest.yaml` — same content the chart renders, for `kubectl apply` |

The CRDs are **cluster-scoped** and live in the tenant's workspace, projected
there via the provider's APIExport. Connection / Repository / DeployKey /
Collaborator are tenant-authored; **Package** is read-only observed state the
crawler writes (one CR per published artifact, owned by its Repository). The
single `permissionClaim` is `secrets`
(`get,list,watch,create,update,patch,delete`, `tenantScoped: true`) so the
controllers can read the credential Secret a Connection references, and the
portal can store it.

## Architecture

```
Browser / MCP client
   │  bearer
   ▼
hub /services/providers/code/{mcp, mcp/sse, oauth/github/*}
   │  proxy injects X-Railgrid-Tenant + X-Railgrid-Cluster (the workspace's
   │  kcp logical-cluster ID, in both) + X-Railgrid-User
   ▼
this provider pod
   │
   │  controllers (as the provider SA, via the APIExport VW)
   │    Connection  → validate credential against GitHub
   │    Repository  → ensure repo exists on the host
   │    DeployKey   → register/generate keys
   │    Collaborator→ invite/manage access
   │    Package     → crawl host packages on a timer → Package CRs
   │      └ kubeconfig: /var/run/secrets/railgrid/railgrid-provider-kubeconfig
   │
   └  MCP (AS THE CALLER, caller's own bearer token)
```

CRUD does **not** go through this pod's HTTP surface: the portal drives every CR —
Connections, Repositories, DeployKeys, Collaborators, and the crawled Packages —
through the hub's kube REST proxy at
`/clusters/<cluster>/apis/code.railgrid.ai/v1alpha1/<resource>`. Reads are plain
Kubernetes list/get calls; writes are server-side apply for create-or-update
(which also writes the credential Secret under `/api/v1/namespaces/default/secrets`),
merge-patch for targeted updates, and DELETE. The pod's HTTP surface is only for
the MCP tools and the GitHub OAuth callback.

## Run locally

```sh
# 1. Build the portal bundle (embedded into the binary via assets.go //go:embed).
make build-code-provider-portal

# 2. Run against an embedded-kcp hub (see the repo root README for the hub).
make run-hub-embedded-static          # in one terminal
make install-provider-code            # apply the CatalogEntry
make init-provider-code               # write dev kubeconfig + ensure the EndpointSlice
make run-provider-code                # start the provider on :8083

# 3. Smoke test.
curl -s localhost:8083/healthz
```

With the embedded-dev `Tiltfile`, run `code-register` once before starting
`code`. Each Code update (source changes or a manual trigger) builds the
provider, runs `init-provider-code`, then restarts the initialized binary.
An init failure prevents that update from starting a new process. The input
admin kubeconfig is watched so credential changes also rerun initialization;
the generated runtime kubeconfig is not watched, avoiding an update loop.
Readiness uses `/readyz`. The separate `code-init` action remains available
for setup/repair, but no longer restarts Code by itself; trigger `code` when
a process restart is needed. This sequence applies to the embedded-dev
Tiltfile, not the pod-based `Tiltfile.cluster` flow.

`make run-provider-code` auto-sources `providers/code/.env` (gitignored) so
GitHub OAuth + other dev env reach the provider — copy `.env.example` to `.env`
to enable "Connect with GitHub" locally. In dev, `RAILGRID_DEV_ALLOW_TENANT_QUERY=true`
lets `?tenant=` / `?token=` stand in for the hub-injected identity headers.

## Connecting an account

- **Personal Access Token (default):** paste a PAT in the portal's Connections
  view. A classic PAT needs `repo` (+ `delete_repo` to remove provider-created
  repositories, `admin:public_key` for deploy keys, `read:org` for org repos,
  and **`read:packages`** for the repo Packages panel).
- **Connect with GitHub (OAuth):** enable the OAuth App (below) and the portal
  shows a one-click button — no copy-paste. OAuth tokens are requested with
  `read:packages` by default so the Packages panel works out of the box.

The token is stored as a Secret in the tenant workspace, **owned by** its
Connection — deleting the Connection garbage-collects the Secret.

## Repository Provider Actions

Code owns Git-host credentials and transport for consumers such as other Railgrid
providers. Repository-bound actions expose branch reads, PR lookup/create/update,
PR and merge observations, checks/reviews (including inline comments and check
annotations), issue comments, review replies, and canonical Runner snapshot
verification/publication. No engineering scheduling or approval policy lives here.

POST an envelope `{ "input": { ... } }` to
`/services/providers/code/actions/clusters/{cluster}/repositories/{name}/{action}/v1`.
Every input includes `repository` (canonical owner/name), `repositoryUID`, and
`connectionUID`. The caller needs Repository `get` and `invoke` on
`repositories/<action>` for that resource name. Code resolves credentials through
its provider export only after those checks, pins the recorded upstream repository
ID, and rejects replacement or redirection. Tenant callers need no Secret access.
Responses use the shared Provider Action envelope: `requestID`, provider/action
identity, `resourceRef`, and exactly one of `result` or `error`. `X-Request-ID`
supplies the correlation ID. The CatalogEntry advertises the eleven bounded
action schemas and their digests.

Git bundles use a separate bounded upload: `stage_snapshot` at the same route
shape, with its own `invoke` grant, accepts a snapshot containing `baseCommit`,
`commit`, `tree`, and a base64 `bundle` (25 MiB decoded maximum). This supporting
artifact endpoint is not advertised as a small JSON action. It returns a
`bundleRef` scoped to tenant, caller credential, Repository UID, and Connection
UID. Artifacts expire after one hour and are lazily removed during uploads;
quotas bound each tenant to 16 artifacts and 256 MiB. Re-upload after expiration
or credential rotation. The normal `prepare_snapshot` and `publish_snapshot`
actions accept that handle, not inline bundles. The runtime needs Git and writable
bundle storage; the image includes Git and uses the existing bundle volume.

Publication verifies the public Runner single-parent snapshot format and uses
an atomic Git expected-head lease. An empty `expectedHead` requires an absent
branch. Read the exact branch after a lost response. PR creation and comment
writes have no automatic retry or shared receipt store: their catalog idempotency
is `none`. Consumers must retain their own business dispatch fence and reconcile
uncertain outcomes before issuing another mutation. Code never merges a PR.

### GitHub App Connections

Set a Connection's `spec.type` to `github-app` and point `spec.secretRef` at a tenant
Secret with `appID`, `installationID`, and `privateKey` data keys. The private key
must be one PEM RSA key of at least 2048 bits. Code signs a short-lived App JWT and
exchanges it for an installation token; the private key is never sent to consumers.
Grant the installation access only to intended repositories: Contents read/write
for publication, Pull requests read/write for PR collaboration, Issues write for
issue comments, and Checks read for feedback. PAT and OAuth Connections continue
to use their configured token key. The portal's existing PAT/OAuth flows remain.

To register a repository whose lifecycle is managed outside Railgrid, annotate its
Repository with `code.railgrid.ai/existing-only: "true"`. Reconciliation requires an
existing upstream repository; deleting this registration does not delete the
upstream repository. A recorded upstream repository ID is pinned and a missing
or replaced repository is never silently recreated.

## Register with the hub

The CatalogEntry registers the provider with the hub for routing + the portal
Enable flow. It is a kcp resource, so it lives in the provider workspace — not
the hosting cluster. With `catalogEntry.enabled=true` (default) the chart renders
it into a ConfigMap and the init container self-registers it into the workspace
via the provider kubeconfig; alternatively apply the raw manifest yourself:

```sh
kubectl --kubeconfig kcp-admin.kubeconfig ws use root:railgrid:providers
kubectl apply -f manifest.yaml
kubectl get catalogentry code -o yaml   # Ready flips True once heartbeats land
```

Open the portal at `https://<hub>/ui/providers/code/`.

## Build the image

A three-stage build (portal → Go binary → distroless) that bakes the portal
into the binary. Listens on `:8083`.

```sh
docker build -t ghcr.io/railgrid/railgrid-code-provider:dev providers/code/
```

## Deploy with Helm

The chart ships the provider Deployment, a ClusterIP Service, the ServiceAccount,
and (optionally) the CatalogEntry ConfigMap the init container applies to kcp.
The runtime kubeconfig the controllers need
is **minted by the hub** when it reconciles the CatalogEntry and mounted from the
`railgrid-provider-kubeconfig` Secret — the volume is `optional`, so the pod serves
portal/MCP/packages reads immediately and the controller manager engages once
the Secret appears.

### Minimal (PAT-only connections)

```sh
helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://railgrid-hub.railgrid.svc.cluster.local:9443 \
  --set image.tag=0.1.0
```

### With "Connect with GitHub" (OAuth)

Create a GitHub OAuth App, store its client secret in a Secret, then enable the
`githubOAuth.*` block. The portal probes `/services/providers/code/oauth/github/config`
through the hub; once OAuth is enabled and the provider backend is reachable, the
**Connect with GitHub** button appears.

```sh
kubectl -n code create secret generic railgrid-code-github-oauth \
  --from-literal=clientSecret=<oauth-app-client-secret>

helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://railgrid-hub.railgrid.svc.cluster.local:9443 \
  --set githubOAuth.enabled=true \
  --set githubOAuth.clientId=<oauth-app-client-id> \
  --set githubOAuth.clientSecretRef.name=railgrid-code-github-oauth \
  --set githubOAuth.redirectURL=https://<hub-host>/services/providers/code/oauth/github/callback \
  --set githubOAuth.portalOrigin=https://<hub-host>
```

#### Choosing `redirectURL`

GitHub's callback is a **top-level browser redirect with no railgrid auth**, so
`redirectURL` must be publicly reachable and forward to the provider's HTTP
backend (`:8083`). It must end in `/callback`; the matching `/start` URL is
derived automatically by swapping `/callback` → `/start` under the **same host
and path prefix**. Two options:

1. **Reuse the hub ingress (recommended — no extra ingress object):** point at
   the hub's existing `/services/providers/code/*` proxy:
   ```
   https://<hub-host>/services/providers/code/oauth/github/callback
   ```
   The proxy forwards these anonymous requests straight to the provider backend,
   so the whole flow rides the single hub hostname. Set `portalOrigin` to the
   same hub origin.

2. **The provider's own external host:** if you expose the provider directly
   (its own ingress/hostname), use:
   ```
   https://code.example.com/oauth/github/callback
   ```

Whichever you pick, register that **exact** callback URL on the GitHub OAuth App,
and set `portalOrigin` to the hub origin so the popup returns the token only to
your portal.

### Full production deployment (hub-routed OAuth)

Provider running in its own namespace, registered against an already-running hub,
with OAuth routed through the hub ingress (no per-provider ingress). The runtime
kubeconfig the controllers need is supplied as the `railgrid-provider-kubeconfig`
Secret (its key **must** be `kubeconfig`) — mint it via the admin onboarding flow
(`/bonkers`).

```sh
# 1. Namespace.
kubectl create namespace railgrid-prod-provider-code

# 2. Provider kubeconfig Secret (key MUST be "kubeconfig").
kubectl -n railgrid-prod-provider-code create secret generic railgrid-provider-kubeconfig \
  --from-file=kubeconfig=railgrid/provider-code.kubeconfig

# 3. GitHub OAuth App client secret.
kubectl -n railgrid-prod-provider-code create secret generic code-github-oauth \
  --from-literal=clientSecret=<oauth-app-client-secret>

# 4. Install the chart from the published OCI registry.
helm upgrade --install code oci://ghcr.io/railgrid/charts/railgrid-code-provider:0.0.82 \
  -n railgrid-prod-provider-code \
  --set hub.url=https://railgrid-railgrid-hub.railgrid-prod.svc.cluster.local:9443 \
  --set hub.insecure=true \
  --set hub.tokenSecretRef.name="" \
  --set image.tag=v0.0.82 \
  --set catalogEntry.enabled=false \
  --set githubOAuth.enabled=true \
  --set githubOAuth.clientId=<oauth-app-client-id> \
  --set githubOAuth.clientSecretRef.name=code-github-oauth \
  --set githubOAuth.clientSecretRef.key=clientSecret \
  --set githubOAuth.redirectURL=https://railgrid.example.com/services/providers/code/oauth/github/callback \
  --set githubOAuth.portalOrigin=https://railgrid.example.com
```

Notes:
- `hub.insecure=true` + `hub.tokenSecretRef.name=""` suit an in-cluster hub with
  a self-signed cert and no static heartbeat token. For a real heartbeat token,
  create a Secret and set `hub.tokenSecretRef.name`/`.key` instead.
- `catalogEntry.enabled=false` means the chart does **not** manage the
  CatalogEntry — the hub uses whatever `backend.url` the existing CatalogEntry
  declares. **Make sure that `backend.url` points at this deployment's Service**
  (`http://code-railgrid-code-provider.<namespace>.svc.cluster.local:8083`); a stale
  namespace there makes the hub→provider proxy return **502** (and the OAuth
  button stays hidden). Leaving `catalogEntry.enabled=true` lets the init
  container keep `backend.url` in sync with the release namespace automatically.
- After install, verify the OAuth probe returns `{"enabled":true}`:
  ```sh
  curl -s https://railgrid.example.com/services/providers/code/oauth/github/config
  ```

`values.yaml` documents the full surface — image, replicas, hub URL + token
Secret, the runtime kubeconfig Secret name, the `githubOAuth.*` block, the
tenant credential namespace, and the CatalogEntry toggle.

## MCP integration

```jsonc
{
  "mcpServers": {
    "railgrid-code": {
      "url": "https://<your-railgrid-hub>/services/providers/code/mcp",
      "headers": { "Authorization": "Bearer <railgrid-bearer>" }
    }
  }
}
```

Identity (tenant + user) is taken from the same bearer token the portal uses —
the model never asks for a tenant path. Read tools list connections/repositories;
write tools create/delete repositories, deploy keys, and collaborators (all
CRD-native, so the controllers do the host work).

## Packages (read-only)

The repository detail page lists the GitHub Packages published under a repo
(container/npm/maven/…). This is **observed state** — packages appear when
artifacts are pushed (`docker push`, `npm publish`), so there is no create here.

Rather than hitting GitHub on every page view (GitHub has no per-repo packages
API and rate-limits the per-ecosystem listing hard), the **packages controller**
crawls each Repository on a timer (`CODE_PACKAGE_CRAWL_INTERVAL`, default 2m) and
reconciles one **Package CR** per artifact, owned by the Repository (so they're
garbage-collected with it) and labelled `code.railgrid.ai/repository=<repo>`.
The portal then reads those CRs through the hub's kube REST proxy
(`GET /clusters/<cluster>/apis/code.railgrid.ai/v1alpha1/packages?labelSelector=…`)
like any other CRD — no provider round-trip, no throttling. Crawling still needs
the connection token's `read:packages` scope.

Complete GitHub discovery listings (all six ecosystems) and image-version listings are shared
for two minutes across repositories using the same Connection and credential.
Owner classification is cached for one hour, avoiding repeated organization
probes for personal accounts. Cache identity includes the Connection UID/tenant,
API base URL, owner, ecosystem and package identity, and a SHA-256 token fingerprint;
tokens are not stored in cache keys or logged. Rotating the token immediately
uses a separate cache and request budget. A shorter controller interval does
not bypass the two-minute cache. With the default interval and positive jitter,
a new artifact can take roughly 4.5 minutes to reach a particular Repository's
Package CRs if another repository refreshed the shared listing just before publish.
The exception is the ten minutes after a RepositoryCommit for the repository
succeeds (the commit's success also triggers a crawl at once): the repository is
crawled every 30 seconds and its container listing and image versions are
refetched instead of served from the cache, so a freshly built image appears
within about 30 seconds of publish. Other ecosystems keep using the cache.

A RepositoryCommit that hits a GitHub rate limit stays `Running` with Ready
reason `RateLimited` and is retried when the limit resets; it is marked `Failed`
only when the reset falls more than 15 minutes after the commit started.
Repositories, Connections, DeployKeys, and Collaborators that hit a rate limit
likewise report Ready reason `RateLimited` (a Connection also reports it on
`Validated`) with the reset time, and are retried when the limit resets rather
than through error backoff. The one-shot RepositoryCheckout and
RepositoryBuildStatus requests fail with the rate-limit message instead of
waiting.

GitHub API calls through the backend's go-github client, including workflow
build-status reads, share a serialized request gate per credential and host.
Complete listing refreshes coalesce separately, releasing that gate between
pages so a long crawl does not monopolize other API operations. Primary exhaustion
pauses network requests until reset (plus one second); secondary throttling uses
`Retry-After`, or exponential delays from one minute up to fifteen minutes when
that header is absent. The backend returns a typed `RateLimitError` with an
absolute `RetryAt` deadline. The package controller schedules `RequeueAfter`
until that deadline; it returns other host or credential-resolution failures
as errors for controller-runtime's exponential workqueue backoff. Successful
crawls keep the normal polling interval and jitter. Listing or version failures
leave the last successful Package CR state intact. The shared GitHub gate still
enforces throttling if resource events trigger reconciliation before a scheduled
retry, or another repository/controller uses the same credential.
Connection spec changes and credential Secret create/update/delete events
enqueue affected repositories in the same tenant immediately, including during
a reset delay. Secret matching respects the configured default namespace. A
rotated token therefore selects fresh backend state on the next reconciliation.
Fresh cached listings may still be used while the network gate is paused.
Paginated refreshes fetch every page anew and publish only on complete success;
failed refreshes never leave independently reusable pages behind. Each GitHub
HTTP request has a 30-second timeout (including gate waits and response reads),
so a stalled response releases the gate. Throttle headers are recorded before
reading the body, including when the body is truncated or times out.

Caches and throttle deadlines are process-local and reset after a process restart
or failover to another replica. Existing controller leader election limits active
controller crawlers, but does not coordinate HTTP callers across replicas.
Replicas, different tokens for the same GitHub user,
and other GitHub clients do not share a budget. There are at most 64 credential/
host states, each with at most 256 cached entries and 1 MiB of serialized entry data.
Oversized listings are fetched normally but not cached. Idle states are reclaimed
on subsequent requests after an hour, except while a throttle is active. At state
capacity, the oldest idle, unthrottled state is evicted. If every slot is busy or
throttled, new credential/host requests fail locally until a slot is available;
active throttle state is never evicted to admit new traffic. This global capacity
bound does not guarantee availability isolation or fairness between tenants. Large
accounts that exceed the response cache bounds will see less request sharing.

For five repositories on one personal account with one page per ecosystem, the
old polling model implied 7,200 listing requests/hour (five repositories × 120
polls × six ecosystems × organization/user attempts). The mock-server regression
measures seven cold requests (one owner lookup plus six listings), zero warm
requests, and six per two-minute refresh: about 181 listing/classification
requests/hour in a steady shared-cache scenario, excluding versions and other
API operations. These are test counts and a model, not historical production
request accounting.

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `CODE_PACKAGE_CRAWL_INTERVAL` | `2m` | Repository package crawl interval; does not bypass the two-minute shared GitHub cache |
| `PORT` | `8083` | Listen port |
| `RAILGRID_HUB_URL` | (unset → heartbeat off) | Hub base URL for heartbeats |
| `RAILGRID_HUB_TOKEN` | (unset) | Bearer token for heartbeats |
| `RAILGRID_PROVIDER_NAME` | `code` | CatalogEntry name |
| `RAILGRID_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `CODE_KUBECONFIG` | (unset → controllers disabled) | kcp kubeconfig for the multicluster controller manager |
| `CODE_WORKSPACE_PATH` | `root:railgrid:providers:code` | Workspace the APIExportEndpointSlice is ensured in |
| `CODE_COMMIT_BUNDLE_DIR` | system temp dir | Directory for provider-owned RepositoryCommit source bundles; use shared storage before running multiple replicas |
| `RAILGRID_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace the Connection credential Secret lives in |
| `RAILGRID_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=`/`?token=` replace identity headers (dev only) |
| `GITHUB_OAUTH_CLIENT_ID` | (unset → OAuth off) | GitHub OAuth App client ID |
| `GITHUB_OAUTH_CLIENT_SECRET` | (unset) | GitHub OAuth App client secret |
| `GITHUB_OAUTH_REDIRECT_URL` | (unset) | Absolute callback URL (must end in `/callback`); either the hub `/services/providers/code/oauth/github/callback` proxy route or the provider's own host. `/start` is derived from it |
| `GITHUB_OAUTH_PORTAL_ORIGIN` | `*` | postMessage target origin (set to the hub origin in prod) |
| `GITHUB_OAUTH_SCOPES` | `repo,delete_repo,read:org,admin:public_key,read:packages` | Requested OAuth scopes |

### `init` subcommand

`code-provider init` is a one-shot bootstrap that ensures the
APIExportEndpointSlice exists (the multicluster provider watches it), then exits.
It uses `CODE_KUBECONFIG` and `CODE_WORKSPACE_PATH`. The Helm deployment does not
run it — the hub provisions everything; `make init-provider-code` runs it for the
local dev flow.

## Running it yourself

This provider can run in your own cluster instead of on the platform. railgrid
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in by railgrid. You still configure your Git backend (GitHub app or
token) as you would on the platform — see the chart values.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.

### Create-only repositories

Clients creating a new repository without importing an existing remote must set
`metadata.annotations["code.railgrid.ai/create-only"]: "true"`. App Studio sets this
on newly created repositories, including when connecting Git to an existing
storage-backed project. That connect-later flow also reserves a fresh suffixed
name instead of reusing the project name verbatim.

The GitHub backend rejects an existing name unless its remote ID matches the
repository's recorded `status.repoID`. Creation races fail rather than falling
back to import. Commits and deletion also check the recorded identity; deleting
a failed creation with no recorded ID leaves the remote untouched. Explicit
imports without this annotation retain their existing behavior.

If remote creation succeeds but recording its ID fails, reconciliation fails
closed. App Studio offers **Create a new repository** in project Git settings for
this conflict. It reserves a fresh name and uploads the project source, leaving
the uncertain Repository resource and GitHub repository untouched for inspection.
Requests are fenced to the current project UID and failed binding; confirmed or
imported repositories cannot be replaced through recovery. A name match is not
evidence that Railgrid created a remote. This contract does not retroactively change
repositories already attached before create-only intent was introduced.

Action bodies must complete within 30 seconds, after repository read and invoke
authorization. Each provider process admits eight actions, with at most one
snapshot action (stage, prepare, or publish) at a time to bound bundle memory.
Excess concurrent requests receive HTTP 503 before body decoding. The chart
default memory limit is 512 MiB to leave headroom for JSON buffers and Git.

### Branch discovery

The read-only Repository-bound `branches/v1` action returns `branches` (up to 50 names) and `nextPage` (zero when complete). Supply the canonical repository, repository UID, connection UID, and an optional one-based `page`. Each page uses the caller's `repositories/branches` invoke permission and rechecks the registered repository against the Git host. No Git credentials are returned. Consumers must follow `nextPage` with their own bounded traversal and must not interpret a failed or partial read as an empty repository.
