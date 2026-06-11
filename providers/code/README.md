# code provider

A kedge provider that manages source-code repositories and their access —
deploy keys, collaborators, and (read-only) published packages — across git
hosting providers (**GitHub** today) on behalf of kedge tenants. A tenant adds a
**Connection** (a credential for one git account) in the kedge portal — or via
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
   │  proxy injects X-Kedge-Tenant + X-Kedge-User
   ▼
this provider pod
   │
   │  controllers (as the provider SA, via the APIExport VW)
   │    Connection  → validate credential against GitHub
   │    Repository  → ensure repo exists on the host
   │    DeployKey   → register/generate keys
   │    Collaborator→ invite/manage access
   │    Package     → crawl host packages on a timer → Package CRs
   │      └ kubeconfig: /var/run/secrets/kedge/kedge-provider-kubeconfig
   │
   └  MCP (AS THE CALLER, caller's own bearer token)
```

Reads do **not** go through this pod's HTTP surface: the portal reads every CR —
Connections, Repositories, DeployKeys, Collaborators, and the crawled Packages —
through the hub's GraphQL gateway at `/graphql/<workspace>` (`code_kedge_faros_sh
{ v1alpha1 { … } }`). Writes still use the kcp REST proxy with the user's token
(create/patch/delete + the credential Secret). The pod's HTTP surface is only for
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

`make run-provider-code` auto-sources `providers/code/.env` (gitignored) so
GitHub OAuth + other dev env reach the provider — copy `.env.example` to `.env`
to enable "Connect with GitHub" locally. In dev, `KEDGE_DEV_ALLOW_TENANT_QUERY=true`
lets `?tenant=` / `?token=` stand in for the hub-injected identity headers.

## Connecting an account

- **Personal Access Token (default):** paste a PAT in the portal's Connections
  view. A classic PAT needs `repo` (+ `admin:public_key` for deploy keys,
  `read:org` for org repos, and **`read:packages`** for the repo Packages panel).
- **Connect with GitHub (OAuth):** enable the OAuth App (below) and the portal
  shows a one-click button — no copy-paste. OAuth tokens are requested with
  `read:packages` by default so the Packages panel works out of the box.

The token is stored as a Secret in the tenant workspace, **owned by** its
Connection — deleting the Connection garbage-collects the Secret.

## Register with the hub

The CatalogEntry is what makes the hub provision the provider's workspace,
schemas, APIExport, and runtime kubeconfig. The Helm chart renders it
(`catalogEntry.enabled=true`), or apply the raw manifest:

```sh
kubectl --kubeconfig kcp-admin.kubeconfig ws use root:kedge:providers
kubectl apply -f manifest.yaml
kubectl get catalogentry code -o yaml   # Ready flips True once heartbeats land
```

Open the portal at `https://<hub>/ui/providers/code/`.

## Build the image

A three-stage build (portal → Go binary → distroless) that bakes the portal
into the binary. Listens on `:8083`.

```sh
docker build -t ghcr.io/faroshq/kedge-code-provider:dev providers/code/
```

## Deploy with Helm

The chart ships the provider Deployment, a ClusterIP Service, the ServiceAccount,
and (optionally) the CatalogEntry. The runtime kubeconfig the controllers need
is **minted by the hub** when it reconciles the CatalogEntry and mounted from the
`kedge-provider-kubeconfig` Secret — the volume is `optional`, so the pod serves
portal/MCP/packages reads immediately and the controller manager engages once
the Secret appears.

### Minimal (PAT-only connections)

```sh
helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-code-hub-token \
  --set image.tag=0.1.0
```

### With "Connect with GitHub" (OAuth)

Create a GitHub OAuth App with callback `https://<provider-host>/oauth/github/callback`,
store its client secret in a Secret, then:

```sh
kubectl -n code create secret generic kedge-code-github-oauth \
  --from-literal=clientSecret=<oauth-app-client-secret>

helm install code providers/code/deploy/chart \
  -n code --create-namespace \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.tokenSecretRef.name=kedge-code-hub-token \
  --set githubOAuth.enabled=true \
  --set githubOAuth.clientId=<oauth-app-client-id> \
  --set githubOAuth.clientSecretRef.name=kedge-code-github-oauth \
  --set githubOAuth.redirectURL=https://code.example.com/oauth/github/callback \
  --set githubOAuth.portalOrigin=https://kedge.example.com
```

The OAuth callback is a top-level redirect from GitHub (no kedge auth), so
`redirectURL` must point at the provider's **own externally-reachable URL**, not
the hub `/services` proxy. `portalOrigin` should be the hub origin so the popup
returns the token only to your portal.

`values.yaml` documents the full surface — image, replicas, hub URL + token
Secret, the runtime kubeconfig Secret name, the `githubOAuth.*` block, the
tenant credential namespace, and the CatalogEntry toggle.

## MCP integration

```jsonc
{
  "mcpServers": {
    "kedge-code": {
      "url": "https://<your-kedge-hub>/services/providers/code/mcp",
      "headers": { "Authorization": "Bearer <kedge-bearer>" }
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
garbage-collected with it) and labelled `code.kedge.faros.sh/repository=<repo>`.
The portal then reads those CRs through the hub's GraphQL gateway
(`/graphql/<workspace>`, `code_kedge_faros_sh { v1alpha1 { Packages(labelselector: …) } }`)
like any other CRD — no provider round-trip, no throttling. Crawling still needs
the connection token's `read:packages` scope.

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8083` | Listen port |
| `KEDGE_HUB_URL` | (unset → heartbeat off) | Hub base URL for heartbeats |
| `KEDGE_HUB_TOKEN` | (unset) | Bearer token for heartbeats |
| `KEDGE_PROVIDER_NAME` | `code` | CatalogEntry name |
| `KEDGE_HUB_INSECURE` | (unset) | `true` skips TLS verify on heartbeats |
| `CODE_KUBECONFIG` | (unset → controllers disabled) | kcp kubeconfig for the multicluster controller manager |
| `CODE_WORKSPACE_PATH` | `root:kedge:providers:code` | Workspace the APIExportEndpointSlice is ensured in |
| `KEDGE_TENANT_CREDENTIALS_NAMESPACE` | `default` | Namespace the Connection credential Secret lives in |
| `KEDGE_DEV_ALLOW_TENANT_QUERY` | (unset) | `true` lets `?tenant=`/`?token=` replace identity headers (dev only) |
| `GITHUB_OAUTH_CLIENT_ID` | (unset → OAuth off) | GitHub OAuth App client ID |
| `GITHUB_OAUTH_CLIENT_SECRET` | (unset) | GitHub OAuth App client secret |
| `GITHUB_OAUTH_REDIRECT_URL` | (unset) | Absolute callback URL on the provider's own host |
| `GITHUB_OAUTH_PORTAL_ORIGIN` | `*` | postMessage target origin (set to the hub origin in prod) |
| `GITHUB_OAUTH_SCOPES` | `repo,read:org,admin:public_key,read:packages` | Requested OAuth scopes |

### `init` subcommand

`code-provider init` is a one-shot bootstrap that ensures the
APIExportEndpointSlice exists (the multicluster provider watches it), then exits.
It uses `CODE_KUBECONFIG` and `CODE_WORKSPACE_PATH`. The Helm deployment does not
run it — the hub provisions everything; `make init-provider-code` runs it for the
local dev flow.
