# Access reference: CLI, auth, hub REST, URL grammar

Source citations are repo-relative paths in the faros repository.

## 1. CLI command tree

Binary `faros`; krew installs `kubectl-faros`, so `kubectl faros <cmd>` is
equivalent. Two global flags: `--kubeconfig <path>` (default `$KUBECONFIG`,
else `~/.kube/config`) and `--insecure-skip-tls-verify`. `faros --help`
groups commands (getting started, edges, organizations and access, developer
workflow, agents/hub/dev); the generated per-command reference is
`docs/cli/README.md` (`make docs-cli`). List commands take
`-o wide|json|yaml|name`; `get`/`whoami` take `-o json|yaml`. Any command
that asks for confirmation takes `-y/--yes`; without a TTY it errors with
`confirmation needed but stdin is not a terminal; pass --yes`.

| Command | Flags | What it does |
|---|---|---|
| `login` | `--hub-url` (or `$FAROS_HUB_URL`, required), `--token`, `-i/--interactive` | OIDC browser flow with PKCE and a localhost callback (`$BROWSER` overrides the opener), or `POST /auth/token-login` with a static token. Writes cluster, context, and user named `faros` and sets it current. Re-login keeps the selected workspace on the same hub. Prints `Logged in as <email>`, token validity, and next steps; warns when the IdP issued no refresh token. |
| `logout` | `--keep-edge-contexts` | Deletes the OIDC token cache for the hub and removes the `faros` context (plus every `faros-<edge>` context) from the kubeconfig. |
| `whoami` (alias `status`) | `-o json\|yaml` | Hub, user (ID-token email, else the personal org name), auth mode (`oidc`/`static-token`) with token expiry and whether it auto-refreshes, active org and workspace with your role in each, the kcp cluster, what the current kubectl context targets (`hub workspace`, `edge <name>`, or `other`), and every org you belong to. |
| `token` | `--refresh` | Prints the bearer the kubeconfig produces (OIDC id_token refreshed when expired). `--refresh` forces the refresh grant against the IdP and reports the new validity — the quickest check that refresh works. |
| `get-token` (hidden) | `--oidc-issuer-url`, `--oidc-client-id` | kubectl exec plugin. Prints an ExecCredential with `.status.token`; refreshes from `~/.config/faros/tokens/` under a file lock and persists the rotated refresh token before answering. |
| `use` (aliases `switch`, `ctx`) | `--org`, `--workspace` | Lists orgs and workspaces via hub REST, rewrites the `faros` cluster server to `<hub>/clusters/<clusterName>` and makes `faros` the current context again (undoing `connect`). Names match case-insensitively; UUIDs win; ambiguity errors. No TTY plus a missing flag is an error. |
| `connect [<edge>]` | | Adds context `faros-<edge>` (server = the edge's hub proxy URL, user = the `faros` user entry, TLS settings inherited from the hub cluster) and makes it current, so plain `kubectl` hits the edge. No argument opens a picker of Kubernetes edges (connected first). A server edge answers `use: faros ssh <edge>`; a disconnected edge `edge "<n>" is not connected`. |
| `disconnect` | | Makes `faros` the current context again; the `faros-<edge>` contexts stay for `kubectl --context`. |
| `edge kubeconfig <name>` (also hidden `kubeconfig edge`) | `-o/--output <file>`, `--merge` | Standalone one-context kubeconfig `faros-<name>` on stdout or in a file (credentials copied), or `--merge` to add the context without switching. |
| `edge create <name>` | `--type kubernetes\|server\|macos`, `--labels k=v,…` | Creates a `KubernetesCluster`, `LinuxServer` or `MacOSServer` (service-only: no kubectl or SSH), waits up to 30 s for `status.joinToken`, prints the join guide (helm install, `faros agent join`, `faros agent run`). |
| `edge list` (alias `ls`; hidden `faros list`/`faros ls`) | `-o wide\|json\|yaml\|name` | `NAME TYPE PHASE CONNECTED AGENT VERSION AGE`, sorted by name; `wide` adds `HOSTNAME LAST HEARTBEAT LABELS`; `json`/`yaml` print a `v1 List` of the objects. Empty: `No edges found. Create one with: faros edge create <name> [--type server]`. |
| `edge get <name>` (aliases `describe`, `show`) | `-o json\|yaml` | Name, type (from the kind), phase, connected, agent version, hostname, last heartbeat, proxy URL, created, labels, conditions, and the next command (`faros connect`/`faros ssh`). |
| `edge join-command <name>` | | Reprint the join guide (type from the kind). |
| `edge upgrade <name>` | | Prints helm upgrade or binary replace instructions when the agent is behind the CLI |
| `edge delete <name>` (aliases `rm`, `remove`) | `-y/--yes` | Asks for confirmation on a TTY; irreversible |
| `org list` | `-o wide\|json\|yaml\|name` | `CURRENT NAME ROLE KIND UUID` (`*` marks the org owning the kubeconfig's workspace; `wide` adds the creation policies and age) |
| `org create <display-name>` | `--workspace-creation members\|admin`, `--catalog-entry-creation members\|admin` | `POST /api/orgs`; you become admin |
| `org members [list]` | `--org`, `-o json\|yaml\|name` | `MEMBER ROLE NAME USER ID` for the org (`--org` name or UUID; default: the org owning the current workspace, else your only org). Org routes never send `X-Faros-Workspace`. |
| `org members add <user>` | `--role admin\|member` (member), `--invite`, `--org` | `<user>` is an email, user id or RBAC identity of an existing user; `--invite` pre-provisions an unknown email. Admin only. |
| `org members set-role <user> <admin\|member>` | `--org` | Resolves `<user>` from the member list (id, email, RBAC identity or display name) and `PATCH`es the role; no-op message when unchanged. |
| `org members remove <user>` | `--org`, `--cascade`, `-y` | `DELETE …/memberships/<user id>`; `--cascade` also drops the user's workspace rows. |
| `workspace list` (aliases `ws`, `workspaces`) | `--org`, `-o …` | `CURRENT NAME ROLE UUID CLUSTER` for one org |
| `workspace create <display-name>` | `--org` | `POST /api/orgs/{org}/workspaces` |
| `workspace members [list\|add\|set-role\|remove]` | `--org`, `--workspace`, same flags as the org variants (no `--cascade`) | Workspace-scope memberships; requests carry both tenant headers |
| `get <resource>` (hidden, deprecated) | | Only `edges`, `workloads` (`vw`), `placements`. Use `edge list` or kubectl. |
| `apply -f <file>` (hidden, deprecated) | `-f` | Single YAML doc, naive pluralization. Prefer kubectl. |
| `mcp url` | `--mcpserver-name <name>` or `--edge <name>` (exactly one) | Prints the endpoint plus Claude Code, Claude Desktop, and Codex snippets. With `--mcpserver-name` it fetches the long-lived MCPServer token from the hub's `…/mcpservers/{name}/connect` (works on OIDC hubs; the faros context must target the same workspace as the current context). Falls back to the kubeconfig `authInfo.token`, and prints a note on stderr when neither exists (OIDC + `--edge`) or the hub has not minted the token yet. |
| `env` | `--json`, `--no-mcp`, `--org`, `--workspace` | Prints `export` lines for `HUB CLUSTER ORG WS TOKEN AS MCP_URL MCP_TOKEN`: `eval "$(faros env)"`. Details in [cli.md](cli.md). |
| `app`, `commit`, `sandbox` | see [cli.md](cli.md) | App Studio projects, faros-recorded git commits, and the dev-instance data plane from a terminal. |
| `ssh <name> [-- cmd…]` | | WebSocket to the LinuxServer `ssh` subresource. Interactive needs a TTY. `cat f \| faros ssh x -- "cat > /tmp/f"` copies files. No `-L`/`-R`. A Kubernetes edge answers `use: faros connect <name>`; a disconnected one `edge "<n>" is not connected`. |
| `agent run\|join\|install\|uninstall\|upgrade`, `install` | see below | Edge agent lifecycle on the target host, not laptop workflow |
| `version` | | version, commit, build date, go version, platform |
| `completion bash\|zsh\|fish\|powershell` | | Shell completion; edge names, roles and `-o` values complete. |
| `docs` (hidden) | `--dir` | Regenerates the markdown reference (`make docs-cli`). |
| `dev init\|update\|delete` | `--worker-count`, `--chart-path`, ports, `--with-dex`, … | Local kind-based hub at `https://faros.localhost:9443` with static token `dev-token`. Needs `127.0.0.1 faros.localhost` in `/etc/hosts`. |
| `init` | | Runs an in-process hub. Server command, not client. |
| `kcp-workspace` (hidden, alias `kcp-ws`) | `-i` | kcp `kubectl ws` navigation (`:`, `..`, `-`, `~`, `root:…`). Rewrites the **current** kubeconfig context; `faros disconnect` returns to `faros`. Formerly `faros connect`/`ws`. |

Agent flags shared by `agent run` and `agent join`: `--hub-url`, `--token`,
`--hub-kubeconfig`, `--hub-context`, `--tunnel-url`, `--edge-name`,
`--type`, `--labels`, `--kubeconfig`, `--context`, `--cluster`,
`--ssh-proxy-port` (22), `--ssh-user`, `--ssh-password`, `--ssh-private-key`,
`--hub-insecure-skip-tls-verify`, `--debug-addr`, `--svc-allow-cidr`
(repeatable, also `$FAROS_AGENT_SVC_ALLOW_CIDR`), `--svc-policy enforce|warn|allow-any`
(also `$FAROS_AGENT_SVC_POLICY`). `agent join --type server` writes a
systemd unit `faros-agent-<edge>.service`; `--type kubernetes` applies a
namespace `faros-agent`, a per-edge ServiceAccount, and a Deployment
`faros-agent-<edge>`. `faros install --type server|kubernetes --hub-url --edge-name --token [--dry-run]`
is the one-shot variant.

Environment variables the CLI reads: `FAROS_HUB_URL`, `FAROS_AGENT_IMAGE`,
`FAROS_AGENT_IMAGE_TAG`, `FAROS_AGENT_IMAGE_PULL_POLICY`,
`FAROS_AGENT_SVC_ALLOW_CIDR`, `FAROS_AGENT_SVC_POLICY`, plus `KUBECONFIG`
and `HOME`. `FAROS_MCP_TOKEN` is only printed for the Codex snippet.

Sources: `pkg/cli/cmd/{login,logout,whoami,token,use,connect,kubeconfig,edge,org,workspace,members,output,get,apply,mcp,ssh,agent,install,version,docs}.go`,
`pkg/cli/cmd/{env,app,commit,sandbox,hubclient}.go`, `pkg/cli/cmd/dev/`. E2E: `test/e2e/suites/cli` (`make e2e-cli`).

## 2. Where credentials live

- Kubeconfig entries named `faros` in the default kubeconfig file.
- OIDC cache: `~/.config/faros/tokens/<sha256(issuer+client)[:32]>.json`,
  mode 0600, with a `.lock` sibling. Not `~/.faros`.
- The edge agent, unrelated, keeps `~/.faros/agent-<edge>.kubeconfig`.
- `faros logout` removes the token cache and the `faros`/`faros-<edge>`
  contexts. One hub per kubeconfig file; use `KUBECONFIG` to hold two.

## 3. Workspace tree and identifiers

```
root:faros
  providers:<provider>          platform provider workspaces
  tenants:<orgUUID>             org workspace (hub-mediated only; 403 via /clusters)
    <wsUUID>                    team workspace   ← this is what you work in
    <wsUUID>:<edge>             edge mount
    providers:<provider>        org-owned (BYO) provider workspaces
  system                        the platform's own objects
    controllers                 platform APIExports and APIResourceSchemas
    providers                   Provider and CatalogEntry objects
    tenants                     User, Organization, Membership objects
```

The `system` branch is worth knowing even though you never address it,
because it explains a naming collision that otherwise looks like a mistake:
`providers` holds provider *workspaces*, while `system:providers` holds the
*objects* that describe them. The same split applies to tenants. Where a
thing lives tells you what kind of thing it is.

Org and Workspace `metadata.name` are server-assigned UUIDs. Display names
are metadata. You never type paths: the hub proxy rejects `/clusters/root:…`
with 403 and demands the cluster ID. Source: `pkg/kcppaths/paths.go`,
`pkg/server/proxy/proxy.go:712-728`.

Membership model: personal org plus default workspace on first login; scopes
`org` and `workspace`; roles `admin` and `member`; org admins are admins of
every child workspace; members are added by email or ID of an existing user;
inside a workspace membership is full admin over that workspace's resources.
`workspaceCreation: members|admin` and `catalogEntryCreation` are per-org
policies.

## 4. URL grammar

| Purpose | Pattern |
|---|---|
| kcp workspace API (kubectl) | `https://<hub>/clusters/<clusterName>/<kube path>` |
| Edge mount | `https://<hub>/clusters/<clusterName>:<edge>/…` |
| Provider backend | `https://<hub>/services/providers/<provider>/<root>/…` where `<root>` is `api`, `mcp`, `dataplane`, `actions`, `s2s`, `agent`, `edgeproxy` |
| Provider actions | `POST /services/providers/{provider}/actions/clusters/{clusterName}/{resource}/{name}/{action}/{version}` |
| Infrastructure data plane | `/services/providers/infrastructure/dataplane/clusters/{clusterName}/instances/{name}[/components/{c}]/{verb}` |
| Edge kubectl proxy | `/services/providers/edges/edgeproxy/clusters/{clusterName}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{edge}/k8s` |
| Edge ssh | `wss://<hub>/services/providers/edges/edgeproxy/clusters/{clusterName}/apis/edges.faros.sh/v1alpha1/linuxservers/{edge}/ssh[?cmd=…]` |
| Per-edge MCP | `/services/providers/edges/agent/{clusterName}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{edge}/mcp` |
| Aggregate MCP | `/services/mcpserver/{clusterName}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp` |
| Kube REST by cluster | `/clusters/{clusterName}/apis/{group}/{version}/{resource}` (core group: `/clusters/{clusterName}/api/v1/…`) — what the portals and `kubectl` use |
| Provider UI assets | `/ui/providers/{provider}/…` (no token forwarded) |
| kcp APIExport virtual workspace | `/services/apiexport/…` (provider ServiceAccount tokens only) |

Source: `pkg/apiurl/urls.go`, `docs/cross-provider-simplification.md`.

## 5. Headers

What you send:

| Header | When |
|---|---|
| `Authorization: Bearer <token>` | Always. No bearer means 401 (except `/healthz`, `/readyz`, `/version`, OAuth callbacks). |
| `X-Faros-Org: <org uuid>` | Every tenant-scoped hub REST call and every provider REST call |
| `X-Faros-Workspace: <ws uuid>` | Every provider REST call (403 without it for delegated-token providers), and workspace-scoped hub REST |
| `Content-Type: application/json` | Bodies |

What the hub injects toward providers, after stripping anything you sent:
`X-Faros-User`, `X-Faros-Tenant` and `X-Faros-Cluster` (both the kcp cluster ID — tenant identity is never a workspace path)
(cluster ID), `X-Faros-Base-Path` (UI proxy). Org-owned providers always
receive a 10-minute delegated ServiceAccount token instead of your bearer;
platform providers follow the hub's `--provider-delegated-tokens` policy.

Tokens accepted at the front door: static token, kcp ServiceAccount token,
OIDC ID token. Provider `/mcp` endpoints and the aggregate additionally
accept the MCPServer's own ServiceAccount token.

## 6. Hub REST surface

Identity-only (`/api`):

```
GET    /api/orgs                       → {"items":[{uuid,displayName,personal,role,workspaceCreation,catalogEntryCreation,createdAt}]}
POST   /api/orgs                       {"displayName":…,"workspaceCreation":…,"catalogEntryCreation":…}
DELETE /api/users/me
POST   /api/users/me/undelete
GET    /api/providers                  provider catalog (send X-Faros-Org to see org-owned ones)
```

Tenant-scoped (need `X-Faros-Org`, plus `X-Faros-Workspace` for workspace routes):

```
GET|PATCH|DELETE /api/orgs/{org}
POST             /api/orgs/{org}/undelete
GET|POST         /api/orgs/{org}/providers                       org-owned (BYO) providers
GET              /api/orgs/{org}/providers/install-targets
DELETE           /api/orgs/{org}/providers/{name}
GET              /api/orgs/{org}/providers/{name}/kubeconfig
POST             /api/orgs/{org}/providers/{name}/credentials/rotate
GET|POST         /api/orgs/{org}/memberships
DELETE           /api/orgs/{org}/memberships/me
PATCH|DELETE     /api/orgs/{org}/memberships/{user}
GET|POST         /api/orgs/{org}/workspaces                      → items[{uuid,orgUUID,displayName,clusterName,role}]
GET|PATCH|DELETE /api/orgs/{org}/workspaces/{ws}
POST             /api/orgs/{org}/workspaces/{ws}/undelete
GET|POST         /api/orgs/{org}/workspaces/{ws}/memberships
DELETE           /api/orgs/{org}/workspaces/{ws}/memberships/me
PATCH|DELETE     /api/orgs/{org}/workspaces/{ws}/memberships/{user}
GET              /api/orgs/{org}/workspaces/{ws}/kubeconfig[?install=faros|krew]
GET|PUT          /api/orgs/{org}/workspaces/{ws}/dashboard/layout
GET|POST         /api/orgs/{org}/workspaces/{ws}/mcpservers      POST {"name","displayName","instructions","readOnly"}
PATCH|DELETE     /api/orgs/{org}/workspaces/{ws}/mcpservers/{name}
GET              /api/orgs/{org}/workspaces/{ws}/mcpservers/{name}/connect   → {endpointURL,serverName,token,tokenReady}
POST             /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable     {"acceptedClaims":[{"group":"","resource":"secrets"}]} → {"bindingName"}
POST             /api/orgs/{org}/workspaces/{ws}/providers/{name}/disable
GET              /api/orgs/{org}/workspaces/{ws}/providers/enabled
GET              /api/orgs/{org}/workspaces/{ws}/app-access
DELETE           /api/orgs/{org}/workspaces/{ws}/app-access/{binding}
GET|POST         /api/orgs/{org}/workspaces/{ws}/serviceaccounts             POST {"displayName","role":"admin|member"} (both required: 400 `invalid role "" (want admin or member)`, `displayName is required`) → 201 {uuid,displayName,role,createdAt}; no token in the response
GET|PATCH|DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{uuid}      address by `uuid`, not display name (DELETE of an unknown id is a silent 204)
POST|DELETE      /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{uuid}/tokens   POST {} → 201 with the bearer; DELETE revokes every token
```

Unauthenticated: `GET /healthz` → `{"status","oidc","tokenLogin","issuerUrl","clientId"}`,
`GET /readyz`, `GET /version`. Auth: `GET /auth/authorize`, `GET /auth/callback`,
`POST /auth/refresh`, `POST /auth/token-login`.

App access: `POST /auth/apps/token` (your hub
bearer → a short-lived `fapp_` token bound to one private app) and
`POST /auth/apps/verify` (called by the app's access gate, not by you).
Private apps accept only `fapp_` tokens in `Authorization`; raw hub tokens
are refused at the gate. Request, limits and errors:
[infrastructure.md](infrastructure.md) section 5, "App access tokens".

Enable semantics: the hub resolves org-scoped providers first (they shadow
platform providers of the same name), rejects providers without an
APIExport (built-ins are implicitly enabled), returns 409 listing missing
dependencies, builds claims only from the provider's declared
`permissionClaims` (you choose accept or reject per resource), then creates
the kcp `APIBinding` named after the provider. Source:
`pkg/hub/restapi/providers_enable.go`.

Workspace kubeconfig download emits cluster `faros` at
`<hub>/clusters/<clusterName>` with an exec plugin on OIDC hubs or an
embedded bearer on static-token hubs. `?install=krew` swaps the command to
`kubectl-faros`.

## 7. Provider catalog fields that matter

`GET /api/providers` items: `name`, `displayName`, `scope` (`global`|`org`),
`ready`, `readinessReason`, `apiExportPath`, `apiExportName` (empty means
nothing to bind), `permissionClaims`, `edgeProxyAccess`, `dependencies`,
`builtin`, `category`, `children`, and for actions `actions[]` with
`name`, `version`, `boundResource`, `inputSchema`, `outputSchema`,
`schemaDigest`, `readOnly`, `risk`, `limits`, `consent`, `deprecation`.
Provider skills appear as `assistantSkills[]`.

## 8. Kube REST by cluster

Tenant resources are plain Kubernetes REST through the hub's kcp proxy at
`https://<hub>/clusters/<clusterName>/…` with the bearer. The proxy authorizes
the caller by workspace membership and forwards to kcp as that user, so any
workspace you are a member of works, not only your default one. Creates are
`POST`, full updates `PUT`, partial updates `PATCH` with
`application/merge-patch+json`, create-or-update is server-side apply
(`PATCH` with `application/apply-patch+yaml`, `?force=true`).

```bash
curl -s "$HUB/clusters/$CLUSTER/apis/code.faros.sh/v1alpha1/repositories" -H "Authorization: Bearer $TOKEN"
```

The kubectl equivalent:

```bash
kubectl --server="$HUB/clusters/$CLUSTER" --token="$TOKEN" get repositories.code.faros.sh
```

## 9. kubectl cheat sheet in a workspace

```bash
kubectl api-resources | grep faros.sh
kubectl get kubernetesclusters.edges.faros.sh,linuxservers.edges.faros.sh
kubectl get templates.infrastructure.faros.sh
kubectl get instances.infrastructure.faros.sh -o wide
kubectl get connections.code.faros.sh,repositories.code.faros.sh,repositorycommits.code.faros.sh,packages.code.faros.sh
kubectl get projects.ai.faros.sh,sessions.ai.faros.sh,studios.ai.faros.sh
kubectl get agents.agents.faros.sh,connections.agents.faros.sh,schedules.agents.faros.sh,triggers.agents.faros.sh
kubectl get mcpservers.faros.sh
kubectl get secrets -n default            # the credentials namespace every provider reads
```

All provider CRDs listed here are cluster-scoped inside the workspace.
Secrets referenced by providers live in namespace `default`.

## 10. Local development hub

`faros dev init --worker-count 1` creates kind clusters `faros-hub` and
`faros-agent`, hub at `https://faros.localhost:9443`, static token
`dev-token`, kubeconfigs written to the current directory. Log in with
`faros login --hub-url https://faros.localhost:9443 --insecure-skip-tls-verify --token dev-token`.
Tear down with `faros dev delete --worker-count 1`. `docs/getting-started.md`
walks through joining the worker as an edge.
