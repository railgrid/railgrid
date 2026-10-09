# Access reference: CLI, auth, hub REST, URL grammar

Source citations are repo-relative paths in the railgrid repository.

## 1. CLI command tree

Binary `railgrid`; krew installs `kubectl-railgrid`, so `kubectl railgrid <cmd>` is
equivalent. Install paths (source: `install.sh`, `.goreleaser.yml`):
`curl -fsSL https://downloads.railgrid.ai/install.sh | sh` (latest GitHub
release, or `RAILGRID_VERSION`; into `INSTALL_DIR`, default `~/.local/bin`,
which must be given explicitly when `HOME` is unset; downloads from
`RAILGRID_BASE_URL`, default `downloads.railgrid.ai/cli/railgrid/<tag>/`, and
falls back to the GitHub release asset; Linux and Darwin only; `RAILGRID_HARNESS`
only changes the `--harness` value in the printed next steps), krew from
`github.com/railgrid/krew-index`,
`go install github.com/railgrid/railgrid/cmd/railgrid@latest`, or the tarball
`kubectl-railgrid_<OS>_<arch>.tar.gz` named after `uname -s`/`uname -m`
(`Linux_x86_64`, `Linux_aarch64`, `Linux_ppc64le`, `Darwin_x86_64`,
`Darwin_arm64`, `Windows_x86_64`, `Windows_arm64`; every archive is a
`.tar.gz` containing `kubectl-railgrid`) with a `kubectl-railgrid_<version>_checksums.txt` beside it.
`railgrid version` prints version, git commit, build date, go version and
platform. Two global flags: `--kubeconfig <path>` (default `$KUBECONFIG`,
else `~/.kube/config`) and `--insecure-skip-tls-verify`. `railgrid --help`
groups commands (getting started, edges, organizations and access, developer
workflow, agents/hub/local development); the generated per-command reference is
`docs/cli/README.md` (`make docs-cli`). List commands take
`-o wide|json|yaml|name`; `get`/`whoami` take `-o json|yaml`. Any command
that asks for confirmation takes `-y/--yes`; without a TTY it errors with
`confirmation needed but stdin is not a terminal; pass --yes`.

| Command | Flags | What it does |
|---|---|---|
| `login` | `--hub-url` (or `$RAILGRID_HUB_URL`, required), `--token`, `-i/--interactive` | OIDC browser flow with PKCE and a localhost callback (`$BROWSER` overrides the opener), or `POST /auth/token-login` with a static token (the hub refuses the browser flow with `hub at <url> does not have OIDC configured — use: ... --token`). Writes cluster, context, and user named `railgrid` and sets it current. Re-login keeps the selected workspace on the same hub. Prints `Logged in as <email>.`, token validity, and next steps; warns when the IdP issued no refresh token. `-i` runs `railgrid use` afterwards. |
| `logout` | `--keep-edge-contexts` | Deletes the OIDC token cache for the hub and removes the `railgrid` context (plus every `railgrid-<edge>` context) from the kubeconfig. |
| `whoami` (alias `status`) | `-o json\|yaml` | Hub, user (ID-token email, else `GET /api/users/me` email/display name/RBAC identity, else the personal org name), auth mode (`oidc`/`static-token`/`other`) with token expiry and whether it auto-refreshes, active org and workspace with your role in each, the kcp cluster, what the current kubectl context targets (`hub workspace`, `edge <name>`, or `other`), and every org you belong to. |
| `token` | `--refresh` | Prints the bearer the kubeconfig produces (OIDC id_token refreshed when expired). `--refresh` forces the refresh grant against the IdP and reports the new validity — the quickest check that refresh works. Static-token kubeconfigs print the configured token with a note. |
| `get-token` (hidden) | `--oidc-issuer-url`, `--oidc-client-id`, `--insecure-skip-tls-verify` | kubectl exec plugin. Prints an ExecCredential with `.status.token`; refreshes from `~/.config/railgrid/tokens/` under a file lock and persists the rotated refresh token before answering. |
| `use` (aliases `switch`, `ctx`) | `--org`, `--workspace` | Lists orgs and workspaces via hub REST, rewrites the `railgrid` cluster server to `<hub>/clusters/<clusterName>` and makes `railgrid` the current context again (undoing `connect`). Names match case-insensitively; UUIDs win; ambiguity errors (`... is ambiguous (N matches); use a UUID: ...`). No TTY plus a missing flag errors with `no interactive terminal; pass --org and --workspace`. A workspace with no cluster yet: `is not ready yet (no cluster assigned)`. |
| `connect [<edge>]` | | Adds context `railgrid-<edge>` (server = the edge's `k8s` verb URL, section 4; user = the `railgrid` user entry, TLS settings inherited from the hub cluster) and makes it current, so plain `kubectl` hits the edge. No argument opens a picker of Kubernetes edges (connected first). A server edge answers `edge "<n>" is a Linux server, not a Kubernetes cluster; use: railgrid ssh <n>`; a disconnected edge `edge "<n>" is not connected (phase …)`; a name shared by a cluster and a server resolves to the cluster (`kubernetes/<name>` says so explicitly). |
| `disconnect` | | Makes `railgrid` the current context again; the `railgrid-<edge>` contexts stay for `kubectl --context`. |
| `edge kubeconfig <name>` | `-o/--output <file>`, `--merge` | Standalone one-context kubeconfig `railgrid-<name>` on stdout or in a file (credentials copied), or `--merge` to add the context without switching. |
| `edge create <name>` | `--type kubernetes\|server\|macos`, `--labels k=v,…`, `--harness auto\|none\|claude,codex` | Creates a `KubernetesCluster`, `LinuxServer` or `MacOSServer` (service-only: no kubectl or SSH), waits up to 30 s for `status.joinToken`, prints the harness posture and the join guide (helm install, `railgrid agent join`, `railgrid agent run`). `--harness` writes `spec.harness` on host edges (`server`/`macos`) and is refused with a value other than `auto` on a Kubernetes edge. |
| `edge list` (alias `ls`) | `-o wide\|json\|yaml\|name` | `NAME TYPE PHASE CONNECTED AGENT VERSION AGE`; `wide` adds `HOSTNAME LAST HEARTBEAT LABELS`; `json`/`yaml` print a `v1 List` of the objects. Empty: `No edges found. Create one with: railgrid edge create <name> [--type server]`. |
| `edge get <name>` (aliases `describe`, `show`) | `-o json\|yaml` | Name, type (from the kind), phase, connected, agent version, hostname, last heartbeat, proxy URL, created, labels, conditions, and the next command (`railgrid connect`/`railgrid ssh`). `server/<name>` or `kubernetes/<name>` qualifies a shared name. |
| `edge join-command <name>` | | Reprint the join guide (type from the kind). |
| `edge upgrade <name>` | | Prints helm upgrade or binary replace instructions when the agent is behind the CLI |
| `edge delete <name>` (aliases `rm`, `remove`) | `-y/--yes` | Asks for confirmation on a TTY; irreversible |
| `org list` | `-o wide\|json\|yaml\|name` | `CURRENT NAME ROLE KIND UUID` (`*` marks the org owning the kubeconfig's workspace; `wide` adds `WORKSPACE CREATION CATALOG ENTRIES AGE`) |
| `org create <display-name>` | `--workspace-creation members\|admin`, `--catalog-entry-creation members\|admin` | `POST /api/orgs`; you become admin |
| `org members [list]` | `--org`, `-o json\|yaml\|name` | `MEMBER ROLE NAME USER ID` for the org (`--org` name or UUID; default: the org owning the current workspace, else your only org). Org routes never send `X-Railgrid-Workspace`. |
| `org members add <user>` | `--role admin\|member` (member), `--invite`, `--org` | `<user>` is an email, user id or RBAC identity of an existing user; `--invite` pre-provisions an unknown email. Admin only. |
| `org members set-role <user> <admin\|member>` | `--org` | Resolves `<user>` from the member list (id, email, RBAC identity or display name) and `PATCH`es the role; no-op message when unchanged. |
| `org members remove <user>` | `--org`, `--cascade`, `-y` | `DELETE …/memberships/<user id>`; `--cascade` sends `?cascade=true`, which also drops the user's workspace rows. |
| `workspace list` (aliases `ws`, `workspaces`) | `--org`, `-o …` | `CURRENT NAME ROLE UUID CLUSTER` for one org |
| `workspace create <display-name>` | `--org` | `POST /api/orgs/{org}/workspaces` |
| `workspace members [list\|add\|set-role\|remove]` | `--org`, `--workspace`, same flags as the org variants (no `--cascade`) | Workspace-scope memberships; requests carry both tenant headers |
| `mcp claude`, `mcp codex` | `--mcpserver-name` (default `default`), `--name` (default `railgrid-<mcpserver-name>`), `--ca-file`, `--dry-run`, `--scope user\|local\|project` (claude) | Registers the aggregate MCP server with the local Claude Code or Codex using the workspace's long-lived MCP token, and prints how to start the client so it trusts the hub. Details in [cli.md](cli.md). |
| `mcp proxy` | `--mcpserver-name` (default `default`), `--org`, `--workspace` | Stdio MCP server that relays the workspace aggregate as you (kubeconfig credentials, OIDC refreshed, hub CA trusted); what the railgrid Claude Code plugin registers. Details in [cli.md](cli.md). |
| `mcp url` | `--mcpserver-name <name>` or `--edge <name>` (exactly one) | Prints the endpoint plus Claude Code, Claude Desktop, and Codex snippets. With `--mcpserver-name` it fetches the long-lived MCPServer token from the hub's `…/mcpservers/{name}/connect` (works on OIDC hubs; the current context must target the same cluster as the railgrid context). Falls back to the kubeconfig `authInfo.token`, and prints a note on stderr when neither exists (OIDC + `--edge`) or the hub has not minted the token yet. `--edge` prints the edge's `mcp` verb URL (section 4). |
| `env` | `--json`, `--no-mcp`, `--org`, `--workspace` | Prints `export` lines for `HUB CLUSTER ORG WS TOKEN AS` plus `MCP_URL`/`MCP_TOKEN` when the connect call succeeds: `eval "$(railgrid env)"`. `AS` is `$HUB/clusters/$CLUSTER/apis/ai.railgrid.ai/v1alpha1`, the App Studio kube API base; its verbs are custom subresources (`$AS/projects/<name>/view`). Details in [cli.md](cli.md). |
| `app`, `commit`, `sandbox` | see [cli.md](cli.md) | App Studio projects (`create\|list\|status\|sync\|promote\|publish`), railgrid-recorded git commits, and the dev-instance data plane (`sync\|exec\|logs\|restart\|env\|status`) from a terminal. |
| `ssh <name> [-- cmd…]` | | WebSocket to the LinuxServer `ssh` verb (section 4) with the kubeconfig bearer in `Authorization`. Interactive needs a TTY (`stdin is not a terminal; use 'railgrid ssh <name> -- <command>'`). `cat f \| railgrid ssh x -- "cat > /tmp/f"` copies files. No `-L`/`-R`. A Kubernetes edge answers `use: railgrid connect <name>`; a disconnected one `edge "<n>" is not connected`; `server/<name>` qualifies a shared name. |
| `skills list\|install [skill…]` | `--repo owner/name` (railgrid/railgrid), `--ref` (main), `--target claude\|codex\|all`, `--scope user\|project`, `--dir`, `--force`, `-o json\|yaml` | Downloads the repo's source tarball from `codeload.github.com` at that moment and installs every `skills/<name>/` (or the named ones) to `~/.claude/skills` and `~/.agents/skills` (project scope: `./.claude/skills`, `./.agents/skills`). Writes `.railgrid-skill.json` (repo, ref, commit, skill, installedAt, cliVersion, files) and only replaces directories carrying it; `is a symlink`, `was not installed by 'railgrid skills'; pass --force`, `has no branch, tag or commit "<ref>"`. No hub access needed. |
| `agent run\|join\|install\|uninstall\|upgrade`, `agent token create --edge-name`, `install` | see below | Edge agent lifecycle on the target host, not laptop workflow |
| `runner run` | `--harness codex\|claude`, `--listen 127.0.0.1:8787`, `--state-dir`, `--token-file`, `--config`, `--version`, `--codex-*`, `--claude-*` | Loopback-only coding runner (`runner/v1`) the edge agent normally supervises on a host whose `spec.harness` asks for it; refuses root and non-loopback addresses. |
| `version` | | version, commit, build date, go version, platform |
| `completion bash\|zsh\|fish\|powershell` | | Shell completion; edge names, roles and `-o` values complete. |
| `docs` (hidden) | `--dir` | Regenerates the markdown reference (`make docs-cli`). |
| `dev init\|update\|delete` | `--providers`, `--with-edge`, `--edge-name`, `--worker-count`, `--chart-path`, `--provider-chart-repo`, ports, `--with-dex`, … | Local kind-based hub at `https://console.127.0.0.1.sslip.io:9443` with static token `dev-token`; installs the edges, infrastructure, code, agents and App Studio providers into the same cluster and joins that cluster as edge `local` by default. No `/etc/hosts` entry needed: `*.127.0.0.1.sslip.io` resolves to `127.0.0.1`. |
| `init` | `--listen-addr`, `--data-dir`, `--external-kcp`, `--hub-kubeconfig` | Runs an in-process hub. Server command, not client. |
| `kcp-workspace` (hidden, alias `kcp-ws`) | `-i` | kcp `kubectl ws` navigation (`:`, `..`, `-`, `~`, `root:…`). Rewrites the **current** kubeconfig context; `railgrid disconnect` returns to `railgrid`. |

Agent flags shared by `agent run` and `agent join`: `--hub-url`, `--token`,
`--hub-kubeconfig`, `--hub-context`, `--tunnel-url`, `--edge-name`,
`--type kubernetes|server|macos`, `--labels`, `--kubeconfig`, `--context`, `--cluster`,
`--ssh-proxy-port` (22), `--ssh-user`, `--ssh-password`, `--ssh-private-key`,
`--hub-insecure-skip-tls-verify`, `--debug-addr`, `--svc-allow-cidr`
(repeatable, also `$RAILGRID_AGENT_SVC_ALLOW_CIDR`), `--svc-policy enforce|warn|allow-any`
(also `$RAILGRID_AGENT_SVC_POLICY`, default `warn`), `--harness auto|none|claude,codex`
(also `$RAILGRID_AGENT_HARNESS`; seeds `spec.harness` only, the edge object wins),
`--runner-user` (also `$RAILGRID_AGENT_RUNNER_USER`). `agent join` adds
`--dry-run`, `--launchd-plist`, `--worker-user` (macOS). `agent join --type server`
writes a systemd unit `railgrid-agent-<edge>.service`; `--type macos` a
LaunchDaemon `/Library/LaunchDaemons/com.railgrid.agent.<edge>.plist`;
`--type kubernetes` applies a namespace `railgrid-agent`, a per-edge
ServiceAccount, and a Deployment `railgrid-agent-<edge>`.
`railgrid install --type server|macos|kubernetes --hub-url --edge-name --token [--cluster] [--dry-run]`
is the one-shot variant (its systemd unit is `railgrid-agent.service`).

Environment variables the CLI reads: `RAILGRID_HUB_URL`, `RAILGRID_AGENT_IMAGE`,
`RAILGRID_AGENT_IMAGE_TAG`, `RAILGRID_AGENT_IMAGE_PULL_POLICY`,
`RAILGRID_AGENT_SVC_ALLOW_CIDR`, `RAILGRID_AGENT_SVC_POLICY`,
`RAILGRID_AGENT_HARNESS`, `RAILGRID_AGENT_RUNNER_USER`, `RAILGRID_KCP_IMAGE`
(`dev init`), plus `KUBECONFIG`, `BROWSER` and `HOME`. `RAILGRID_MCP_TOKEN` is
only printed for (and read by) the Codex snippet.

Sources: `pkg/cli/cmd/{root,login,logout,whoami,token,get_token,use,connect,kubeconfig,edge,org,workspace,members,output,mcp,mcp_clients,mcp_proxy,ssh,skills,agent,install,runner,version,docs}.go`,
`pkg/cli/cmd/{env,app,commit,sandbox,hubclient,helpers}.go`, `pkg/cli/cmd/dev/`. E2E: `test/e2e/suites/cli` (`make e2e-cli`).

## 2. Where credentials live

- Kubeconfig entries named `railgrid` in the default kubeconfig file.
- OIDC cache: `~/.config/railgrid/tokens/<sha256(issuer+"\n"+client)[:32]>.json`,
  mode 0600, with a `.lock` sibling. Not `~/.railgrid`.
- The edge agent, unrelated, keeps `~/.railgrid/agent-<edge>.kubeconfig`,
  `agent-<edge>.json`, `agent-<edge>.credential.json` and
  `agent-<edge>.harness.json`; `railgrid mcp claude|codex --ca-file` writes
  CA bundles under `~/.railgrid/ca/`.
- `railgrid logout` removes the token cache and the `railgrid`/`railgrid-<edge>`
  contexts. One hub per kubeconfig file; use `KUBECONFIG` to hold two.

## 3. Workspace tree and identifiers

```
root:railgrid
  providers:<provider>          platform provider workspaces
  tenants:<orgUUID>             org workspace (hub-mediated only; 403 via /clusters)
    <wsUUID>                    team workspace   ← this is what you work in
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
thing lives tells you what kind of thing it is. Edges are objects inside the
team workspace, not child workspaces.

Org and Workspace `metadata.name` are server-assigned UUIDs. Display names
are metadata. You never type paths: the hub proxy rejects `/clusters/root:…`
with 403 (`address workspaces by cluster ID (/clusters/{id}), not by path —
resolve the id via /api/orgs/{org}/workspaces/{ws}`) and an org workspace's
cluster with 403 too. Source: `pkg/kcppaths/paths.go`,
`pkg/server/proxy/proxy.go` (`addressByIDBody`, `orgWorkspaceForbiddenBody`).

Membership model: personal org plus default workspace on first login; scopes
`org` and `workspace`; roles `admin` and `member`; org admins are admins of
every child workspace; members are added by email or ID of an existing user
(`invite: true` pre-provisions an unknown email); inside a workspace any
membership row binds the user as `cluster-admin` of that workspace's kcp
cluster, so the role only matters for org/workspace management
(`pkg/hub/restapi/workspace_rbac.go`). `workspaceCreation: members|admin`
(default `members`) and `catalogEntryCreation` (default `admin`) are per-org
policies.

## 4. URL grammar

| Purpose | Pattern |
|---|---|
| kcp workspace API (kubectl) | `https://<hub>/clusters/<clusterName>/<kube path>` |
| Kube REST by cluster | `/clusters/{clusterName}/apis/{group}/{version}/{resource}` (core group: `/clusters/{clusterName}/api/v1/…`) — what the portals and `kubectl` use |
| Provider verb or action (the only provider data plane) | `/clusters/{clusterName}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail}][?component={c}]` — a kcp custom subresource the shard forwards to the provider; the bearer and headers are the kube ones. An action's version is not in the path. |
| Edge kubectl proxy | `/clusters/{clusterName}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{edge}/k8s` (kubectl appends `/api/v1/…` as the tail); stamped on the edge as `status.URL` |
| Edge ssh | `wss://<hub>/clusters/{clusterName}/apis/edges.railgrid.ai/v1alpha1/linuxservers/{edge}/ssh[?cmd=…]` |
| Per-edge MCP | `/clusters/{clusterName}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{edge}/mcp` |
| Edge Service proxy | `/clusters/{clusterName}/apis/edges.railgrid.ai/v1alpha1/services/{name}/proxy` |
| Aggregate MCP | `/services/mcpserver/{clusterName}/apis/railgrid.ai/v1alpha1/mcpservers/{name}/mcp` |
| Provider backend proxy | `https://<hub>/services/providers/<provider>/…` — only `/mcp` (+`/mcp/sse`), `/oauth/…`, `/webhooks/…`, `/agent/…` (the edge tunnel) and health; there is no `/api`, `/dataplane`, `/actions` or `/s2s` any more |
| Provider UI assets | `/ui/providers/{provider}/…` (no token forwarded) |
| kcp APIExport virtual workspace | `/services/apiexport/…` (provider ServiceAccount tokens only) |

Source: `pkg/apiurl/urls.go` (`ProviderVerbPath`, `EdgeVerbPath`,
`EdgeServiceProxyPath`, `MCPServerPath`), `docs/provider-connectivity-contract.md`,
`docs/provider-actions.md`.

## 5. Headers

What you send:

| Header | When |
|---|---|
| `Authorization: Bearer <token>` | Always. No bearer means 401 (except `/healthz`, `/readyz`, `/version`, OAuth callbacks). |
| `X-Railgrid-Org: <org uuid>` | Every tenant-scoped hub REST call (`/api/orgs/{org}/…`); optional on `GET /api/providers` to see org-owned providers; on `/services/providers/*` it picks the workspace instead of your personal default |
| `X-Railgrid-Workspace: <ws uuid>` | Workspace-scoped hub REST (400 without it), and `/services/providers/*` together with `X-Railgrid-Org` |
| `Content-Type: application/json` | Bodies |

Kube REST and provider verbs under `/clusters/{id}` need neither tenant
header: the cluster in the path is the scope, and the proxy authorizes it
against your memberships.

What the hub injects toward providers on `/services/providers/*`, after
stripping anything you sent: `X-Railgrid-User`, `X-Railgrid-Tenant` and
`X-Railgrid-Cluster` (both the kcp cluster ID — tenant identity is never a
workspace path). Org-owned providers always receive a 10-minute delegated
ServiceAccount token instead of your bearer; platform providers follow the
hub's `--provider-delegated-tokens` policy. Verbs under `/clusters/{id}`
reach the provider with kcp's `X-Remote-User`/`X-Remote-Group` identity and
no bearer at all.

Tokens accepted at the front door: static token, kcp ServiceAccount token,
OIDC ID token. The aggregate MCP endpoint additionally accepts the MCPServer's
own ServiceAccount token (`…/mcpservers/{name}/connect`) and other tenant
ServiceAccounts with RBAC on that MCPServer (`railgrid mcp proxy` sends your
own bearer instead).

## 6. Hub REST surface

Identity-only (`/api`):

```
GET    /api/orgs                       → {"items":[{uuid,displayName,personal,role,workspaceCreation,catalogEntryCreation,workspaceQuota,createdAt,deletionRequestedAt,initialWorkspacePending}]}
POST   /api/orgs                       {"displayName":…,"workspaceCreation":…,"catalogEntryCreation":…}
GET    /api/users/me                   → {user,email,displayName,rbacIdentity}
GET    /api/users/search?q=<5..254 chars>   prefix match on email/name (or a static member id) → items[{user,memberId,email,displayName}]
DELETE /api/users/me
POST   /api/users/me/undelete
GET    /api/providers                  provider catalog (send X-Railgrid-Org to see org-owned ones)
```

Tenant-scoped (need `X-Railgrid-Org`, plus `X-Railgrid-Workspace` for workspace routes):

```
GET|PATCH|DELETE /api/orgs/{org}
POST             /api/orgs/{org}/undelete
GET|POST         /api/orgs/{org}/providers                       org-owned (BYO) providers
GET              /api/orgs/{org}/providers/install-targets
DELETE           /api/orgs/{org}/providers/{name}
GET              /api/orgs/{org}/providers/{name}/kubeconfig
POST             /api/orgs/{org}/providers/{name}/credentials/rotate
GET|POST         /api/orgs/{org}/memberships                     POST {"user","role":"admin|member","invite"?}
DELETE           /api/orgs/{org}/memberships/me
PATCH|DELETE     /api/orgs/{org}/memberships/{user}              PATCH {"role"}; DELETE ?cascade=true drops workspace rows too
GET|POST         /api/orgs/{org}/workspaces                      → items[{uuid,orgUUID,displayName,clusterName,role,deletionRequestedAt}]; POST {"displayName"}
GET|PATCH|DELETE /api/orgs/{org}/workspaces/{ws}
POST             /api/orgs/{org}/workspaces/{ws}/undelete
GET|POST         /api/orgs/{org}/workspaces/{ws}/memberships
DELETE           /api/orgs/{org}/workspaces/{ws}/memberships/me
PATCH|DELETE     /api/orgs/{org}/workspaces/{ws}/memberships/{user}
GET              /api/orgs/{org}/workspaces/{ws}/kubeconfig[?install=railgrid|krew]
GET|PUT          /api/orgs/{org}/workspaces/{ws}/dashboard/layout
GET|POST         /api/orgs/{org}/workspaces/{ws}/mcpservers      POST {"name","displayName","instructions","readOnly"}
PATCH|DELETE     /api/orgs/{org}/workspaces/{ws}/mcpservers/{name}
GET              /api/orgs/{org}/workspaces/{ws}/mcpservers/{name}/connect   → {endpointURL,serverName,token,tokenReady}
POST             /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable     {"acceptedClaims":[{"group":"","resource":"secrets"}],"acceptedHubAccess":[{"capability":"memberships.read","scope":"org"}],"acceptedCompositions":[{"provider":"infrastructure","group":"infrastructure.railgrid.ai","resource":"instances"}]} → {"bindingName","hubAccess","compositions"}; org-scoped hubAccess needs an org admin
POST             /api/orgs/{org}/workspaces/{ws}/providers/{name}/disable
GET              /api/orgs/{org}/workspaces/{ws}/providers/enabled   bindingsByProvider[p] = {bindingName, exportPath, selfHosted, terminating, hubAccess {granted, pending, implicit}, compositions {…}, deletionBlocked}
GET              /api/orgs/{org}/workspaces/{ws}/app-access       → items[{binding,app,user,createdAt}]
DELETE           /api/orgs/{org}/workspaces/{ws}/app-access/{binding}
GET|POST         /api/orgs/{org}/workspaces/{ws}/serviceaccounts             POST {"displayName","role":"admin|member"} (both required: 400 `invalid role "" (want admin or member)`, `displayName is required`) → 201 {uuid,displayName,role,createdAt,lastTokenIssuedAt}; no token in the response
GET|PATCH|DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{uuid}      address by `uuid`, not display name (DELETE of an unknown id is a silent 204)
POST|DELETE      /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{uuid}/tokens   POST {} → 201 {token,expiresAt} (one year); DELETE revokes every token (the SA is re-created) → 204
```

Platform admin (`/api/admin`, platform admins only): `GET access`, `GET users`,
`GET organizations`, `GET|POST providers`, `DELETE providers/{name}`,
`GET providers/{name}/kubeconfig`, `POST providers/{name}/credentials/rotate`,
`POST providers/{name}/claims/reaccept`. `/api/identities` is for providers
holding their own ServiceAccount token, not for users.

Unauthenticated: `GET /healthz` → `{"status","oidc","tokenLogin"[,"issuerUrl","clientId"]}`,
`GET /readyz` (`ok`), `GET /version` → `{"version","gitCommit","buildDate"}`.
Auth: `GET /auth/authorize`, `GET /auth/callback`, `POST /auth/refresh`,
`POST /auth/token-login` (static-token hubs only; absent when the hub runs
with `--disable-token-login`, bearer API auth still works).

App access: `POST /auth/apps/token` (your hub
bearer → a short-lived `fapp_` token bound to one private app) and
`POST /auth/apps/verify` (called by the app's access gate, not by you);
`GET /auth/apps/authorize` and `POST /auth/apps/exchange` are the browser flow.
Private apps accept only `fapp_` tokens in `Authorization`; raw hub tokens
are refused at the gate. Request, limits and errors:
[infrastructure.md](infrastructure.md) section 5, "App access tokens".

Enable semantics: the hub resolves org-scoped providers first (they shadow
platform providers of the same name), rejects providers without an
APIExport (built-ins are implicitly enabled), returns 409
`provider <name> requires provider(s) to be enabled first: …` (the providers
named by `spec.requires[].provider`), builds claims only from the provider's
declared `spec.requires` (you choose accept or reject per resource), then
creates the kcp `APIBinding` named after the provider. If the provider declares
`spec.hub.access` (hub REST capabilities its delegated token may use, e.g. App
Studio reading member lists and inviting), `acceptedHubAccess` records which
you accept in a `Grant` (org-scoped ones need an org admin; unaccepted ones are
refused with a 403 naming the capability). `acceptedCompositions` does the
same for the kinds of another provider this one's reconcilers will create in
your workspace (each must be declared under `spec.requires` with a `provider`).
Source: `pkg/hub/restapi/providers_enable.go`, `pkg/hub/hubaccess`.

Workspace kubeconfig download emits cluster `railgrid` at
`<hub>/clusters/<clusterName>` with an exec plugin on OIDC hubs or an
embedded bearer on static-token hubs. `?install=krew` swaps the command to
`kubectl-railgrid`.

## 7. Provider catalog fields that matter

`GET /api/providers` items: `name`, `displayName`, `description`, `version`,
`scope` (`global`|`org`), `ownerOrg`, `shadowsPlatform`, `ready`,
`readinessReason`, `readinessMessage`, `builtin`, `category`, `iconURL`, plus
the same four sections as the CatalogEntry spec:

- `export` — `name` (the APIExport to bind; absent means nothing to bind),
  `path`, `apiGroups` (the groups it actually serves, which are not the export
  name), and `resources[]`. Each resource is `{name, apiVersion, kind}` with
  `verbs[]` (`name`, `description`, `stream`, `readOnly`) and `actions[]`
  (`id` = `name/version`, `name`, `version`, `displayName`, `description`,
  `inputSchema`, `outputSchema`, `schemaDigest`, `executionMode`, `readOnly`,
  `risk`, `idempotency`, `limits`, `consent`, `deprecation`). The action's bound
  resource is the parent entry, not a field on the action; both are called at
  the verb URL in section 4.
- `requires[]` — what to accept at Enable: per API group, the `provider` that
  serves it and the `resources[]` (`name`, `verbs`, `selector`) it needs. An
  entry naming a `provider` is also a dependency edge.
- `serving` — `ui` (`builtinRoute`, `children`, `mainJSIntegrity`; the UI URL
  is never published), `backend`, `selfHosting` (`supported`, `docsURL`, …).
- `hub` — `access[]` and `assistantSkills[]` (provider skill packages:
  `packageName`, `version`, `digest`, `skill`, `resources[]`).

## 8. Kube REST by cluster

Tenant resources are plain Kubernetes REST through the hub's kcp proxy at
`https://<hub>/clusters/<clusterName>/…` with the bearer. The proxy authorizes
the caller by workspace membership and forwards to kcp as that user, so any
workspace you are a member of works, not only your default one. Creates are
`POST`, full updates `PUT`, partial updates `PATCH` with
`application/merge-patch+json`, create-or-update is server-side apply
(`PATCH` with `application/apply-patch+yaml`, `?force=true`). A provider verb
is the same request one segment deeper (`…/{resource}/{name}/{verb}`).

```bash
curl -s "$HUB/clusters/$CLUSTER/apis/code.railgrid.ai/v1alpha1/repositories" -H "Authorization: Bearer $TOKEN"
curl -s "$AS/projects/shop/view" -H "Authorization: Bearer $TOKEN"     # an App Studio verb
```

The kubectl equivalent:

```bash
kubectl --server="$HUB/clusters/$CLUSTER" --token="$TOKEN" get repositories.code.railgrid.ai
```

## 9. kubectl cheat sheet in a workspace

```bash
kubectl api-resources | grep railgrid.ai
kubectl get kubernetesclusters.edges.railgrid.ai,linuxservers.edges.railgrid.ai,macosservers.edges.railgrid.ai
kubectl get services.edges.railgrid.ai,workloads.edges.railgrid.ai,placements.edges.railgrid.ai
kubectl get templates.infrastructure.railgrid.ai
kubectl get instances.infrastructure.railgrid.ai -o wide
kubectl get connections.code.railgrid.ai,repositories.code.railgrid.ai,repositorycommits.code.railgrid.ai,packages.code.railgrid.ai,pullrequests.code.railgrid.ai
kubectl get projects.ai.railgrid.ai,sessions.ai.railgrid.ai,studios.ai.railgrid.ai
kubectl get agents.agents.railgrid.ai,connections.agents.railgrid.ai,schedules.agents.railgrid.ai,triggers.agents.railgrid.ai,toolsets.agents.railgrid.ai
kubectl get mcpservers.railgrid.ai
kubectl get secrets -n default            # the credentials namespace every provider reads
```

All provider CRDs listed here are cluster-scoped inside the workspace.
Secrets referenced by providers live in namespace `default`.

## 10. Local development hub

`railgrid dev init` creates one kind cluster `railgrid-hub` running the hub at
`https://console.127.0.0.1.sslip.io:9443` (static token `dev-token`), the `edges`,
`infrastructure`, `code`, `agents` and `app-studio` providers (enabled in the
default workspace; `--providers` picks the set, `quickstart` is also
supported, `app-studio` brings `infrastructure`, `--providers ""` installs
none) and a railgrid-agent that joins the cluster
itself as edge `local` in the dev user's default workspace (`--with-edge`,
`--edge-name`). The kubeconfig `railgrid-hub.kubeconfig` and the CA
`railgrid-hub-ca.crt` (for `railgrid mcp claude|codex --ca-file`) are written to the
current directory. Apps published by infrastructure templates are served at
`https://<app>.apps.127.0.0.1.sslip.io:10443` through an in-cluster Envoy Gateway.
Log in with
`railgrid login --hub-url https://console.127.0.0.1.sslip.io:9443 --insecure-skip-tls-verify --token dev-token`;
`railgrid edge list` then shows `local` Ready. `--worker-count N` adds plain kind
clusters (`railgrid-agent`; with N > 1, `railgrid-agent-1`, `-2`, …) for joining more edges by hand, as
`docs/getting-started.md` walks through. Tear down with `railgrid dev delete`
(same `--worker-count`).
