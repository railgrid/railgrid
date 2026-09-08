# code provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-code`](https://github.com/faroshq/provider-code)
> repository is **automatically synced** from the faros monorepo
> [`faroshq/faros`](https://github.com/faroshq/faros) (path `providers/code/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/faros`](https://github.com/faroshq/faros) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

A faros provider that manages source-code repositories and their access —
deploy keys, collaborators, and (read-only) published packages — across git
hosting providers (**GitHub** today) on behalf of faros tenants. A tenant adds a
**Connection** (a credential for one git account) in the faros portal — or via
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
   │  proxy injects X-Faros-Tenant + X-Faros-User
   ▼
this provider pod
   │
   │  controllers (as the provider SA, via the APIExport VW)
   │    Connection  → validate credential against GitHub
   │    Repository  → ensure repo exists on the host
   │    DeployKey   → register/generate keys
   │    Collaborator→ invite/manage access
   │    Package     → crawl host packages on a timer → Package CRs
   │      └ kubeconfig: /var/run/secrets/faros/faros-provider-kubeconfig
   │
   └  MCP (AS THE CALLER, caller's own bearer token)
```

CRUD does **not** go through this pod's HTTP surface: the portal drives every CR —
Connections, Repositories, DeployKeys, Collaborators, and the crawled Packages —
through the hub's GraphQL gateway at `/graphql/<workspace>`. Reads are
`code_faros_sh { v1alpha1 { … } }` queries; writes are create/update/delete
mutations (plus `applyYaml` for create-or-update, which also writes the credential
Secret). The pod's HTTP surface is only for the MCP tools and the GitHub OAuth
callback.

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

`make run-provider-code` auto-sources `providers/code/.env` (gitignored) so
GitHub OAuth + other dev env reach the provider — copy `.env.example` to `.env`
to enable "Connect with GitHub" locally. In dev, `FAROS_DEV_ALLOW_TENANT_QUERY=true`
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

## Register with the hub

The CatalogEntry registers the provider with the hub for routing + the portal
Enable flow. It is a kcp resource, so it lives in the provider workspace — not
the hosting cluster. With `catalogEntry.enabled=true` (default) the chart renders
it into a ConfigMap and the init container self-registers it into the workspace
via the provider kubeconfig; alternatively apply the raw manifest yourself:

```sh
kubectl --kubeconfig kcp-admin.kubeconfig ws use root:faros:providers
kubectl apply -f manifest.yaml
kubectl get catalogentry code -o yaml   # Ready flips True once heartbeats land
```

Open the portal at `https://<hub>/ui/providers/code/`.

## Build the image

A three-stage build (portal → Go binary → distroless) that bakes the portal
into the binary. Listens on `:8083`.

```sh
docker build -t ghcr.io/faroshq/faros-code-provider:dev providers/code/
```

## Deploy with Helm

The chart ships the provider Deployment, a ClusterIP Service, the ServiceAccount,
and (optionally) the CatalogEntry ConfigMap the init container applies to kcp.
The runtime kubeconfig the controllers need
is **minted by the hub** when it reconciles the CatalogEntry and mounted from the
`faros-provider-kubeconfig` Secret — the volume is `optional`, so the pod serves
portal/MCP/packages reads immediately and the controller manager engages once
the Secret appears.

### Minimal (PAT-only connections)

```sh
helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://faros-hub.faros.svc.cluster.local:9443 \
  --set image.tag=0.1.0
```

### With "Connect with GitHub" (OAuth)

Create a GitHub OAuth App, store its client secret in a Secret, then enable the
`githubOAuth.*` block. The portal probes `/services/providers/code/oauth/github/config`
through the hub; once OAuth is enabled and the provider backend is reachable, the
**Connect with GitHub** button appears.

```sh
kubectl -n code create secret generic faros-code-github-oauth \
  --from-literal=clientSecret=<oauth-app-client-secret>

helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://faros-hub.faros.svc.cluster.local:9443 \
  --set githubOAuth.enabled=true \
  --set githubOAuth.clientId=<oauth-app-client-id> \
  --set githubOAuth.clientSecretRef.name=faros-code-github-oauth \
  --set githubOAuth.redirectURL=https://<hub-host>/services/providers/code/oauth/github/callback \
  --set githubOAuth.portalOrigin=https://<hub-host>
```

#### Choosing `redirectURL`

GitHub's callback is a **top-level browser redirect with no faros auth**, so
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
kubeconfig the controllers need is supplied as the `faros-provider-kubeconfig`
Secret (its key **must** be `kubeconfig`) — mint it via the admin onboarding flow
(`/bonkers`).

```sh
# 1. Namespace.
kubectl create namespace faros-prod-provider-code

# 2. Provider kubeconfig Secret (key MUST be "kubeconfig").
kubectl -n faros-prod-provider-code create secret generic faros-provider-kubeconfig \
  --from-file=kubeconfig=faros/provider-code.kubeconfig

# 3. GitHub OAuth App client secret.
kubectl -n faros-prod-provider-code create secret generic code-github-oauth \
  --from-literal=clientSecret=<oauth-app-client-secret>

# 4. Install the chart from the published OCI registry.
helm upgrade --install code oci://ghcr.io/faroshq/charts/faros-code-provider:0.0.82 \
  -n faros-prod-provider-code \
  --set hub.url=https://faros-faros-hub.faros-prod.svc.cluster.local:9443 \
  --set hub.insecure=true \
  --set hub.tokenSecretRef.name="" \
  --set image.tag=v0.0.82 \
  --set catalogEntry.enabled=false \
  --set githubOAuth.enabled=true \
  --set githubOAuth.clientId=<oauth-app-client-id> \
  --set githubOAuth.clientSecretRef.name=code-github-oauth \
  --set githubOAuth.clientSecretRef.key=clientSecret \
  --set githubOAuth.redirectURL=https://faros.example.com/services/providers/code/oauth/github/callback \
  --set githubOAuth.portalOrigin=https://faros.example.com
```

Notes:
- `hub.insecure=true` + `hub.tokenSecretRef.name=""` suit an in-cluster hub with
  a self-signed cert and no static heartbeat token. For a real heartbeat token,
  create a Secret and set `hub.tokenSecretRef.name`/`.key` instead.
- `catalogEntry.enabled=false` means the chart does **not** manage the
  CatalogEntry — the hub uses whatever `backend.url` the existing CatalogEntry
  declares. **Make sure that `backend.url` points at this deployment's Service**
  (`http://code-faros-code-provider.<namespace>.svc.cluster.local:8083`); a stale
  namespace there makes the hub→provider proxy return **502** (and the OAuth
  button stays hidden). Leaving `catalogEntry.enabled=true` lets the init
  container keep `backend.url` in sync with the release namespace automatically.
- After install, verify the OAuth probe returns `{"enabled":true}`:
  ```sh
  curl -s https://faros.example.com/services/providers/code/oauth/github/config
  ```

`values.yaml` documents the full surface — image, replicas, hub URL + token
Secret, the runtime kubeconfig Secret name, the `githubOAuth.*` block, the
tenant credential namespace, and the CatalogEntry toggle.

## MCP integration

```jsonc
{
  "mcpServers": {
    "faros-code": {
      "url": "https://<your-faros-hub>/services/providers/code/mcp",
      "headers": { "Authorization": "Bearer <faros-bearer>" }
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
garbage-collected with it) and labelled `code.faros.sh/repository=<repo>`.
The portal then reads those CRs through the hub's GraphQL gateway
(`/graphql/<workspace>`, `code_faros_sh { v1alpha1 { Packages(labelselector: …) } }`)
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
| `FAROS_HUB_URL` | (unset → heartbeat off) | Hub base URL for heartbeats |
| `FAROS_HUB_TOKEN` | (unset) | Bearer token for heartbeats |
| `FAROS_PROVIDER_NAME` | `code` | CatalogEntry name |
| `FAROS_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `CODE_KUBECONFIG` | (unset → controllers disabled) | kcp kubeconfig for the multicluster controller manager |
| `CODE_WORKSPACE_PATH` | `root:faros:providers:code` | Workspace the APIExportEndpointSlice is ensured in |
| `CODE_COMMIT_BUNDLE_DIR` | system temp dir | Directory for provider-owned RepositoryCommit source bundles; use shared storage before running multiple replicas |
| `FAROS_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace the Connection credential Secret lives in |
| `FAROS_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=`/`?token=` replace identity headers (dev only) |
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

This provider can run in your own cluster instead of on the platform. faros
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in by faros. You still configure your Git backend (GitHub app or
token) as you would on the platform — see the chart values.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
