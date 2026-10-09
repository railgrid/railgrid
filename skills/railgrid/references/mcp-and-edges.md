# MCP aggregate, MCPServer, edges, kuery reference

Sources: `pkg/hub/mcpaggregate/`, `pkg/apiurl/`, `providers/edges/`,
`providers/kuery/`. There is no `list_targets` tool; with several edges
connected, `edges__cluster_list` enumerates them.

## 1. The aggregate endpoint

```
https://<hub>/services/mcpserver/{clusterName}/apis/railgrid.ai/v1alpha1/mcpservers/{name}/mcp
```

(`apiurl.MCPServerURL`; `{clusterName}` is the workspace's kcp logical-cluster
ID — the same `/clusters/<id>` segment your kubeconfig server URL carries.)

- Transport: MCP streamable HTTP, **stateless**; a fresh server per request.
- Auth: `Authorization: Bearer` only. Accepted: the MCPServer's own
  ServiceAccount token (`system:serviceaccount:default:<name>-mcp`), a hub
  user with membership in that workspace, or another tenant ServiceAccount
  with `use` on `railgrid.ai/mcpservers/<name>`. 401 unauthenticated, 403 wrong
  tenant, 429 rate limited (`Retry-After: 60`), 503 verifier unavailable.
  Verifications and the resolved tenant cache for 60 s (keyed by
  sha256(bearer)+cluster+name). A ServiceAccount
  bearer also needs the cluster's workspace path
  resolved after TokenReview: an unknown cluster is 403, a lookup outage is
  503 (fails closed rather than guessing the platform catalog).
- Server identity `railgrid-mcpserver`; instructions tell the model tools are
  namespaced `<provider>__<tool>` and append each provider's own
  instructions under "Provider guidance". One resource: `railgrid://about`.
- Federation: every Ready provider in the verified caller's Org catalog
  (`ListForOrg`: platform providers plus that Org's own) whose backend
  answers `POST /mcp` `tools/list` (8 s to reach a provider, 15 s for its
  discovery, 90 s per call). Tools are
  re-registered as `<provider>__<tool>` in deterministic order. A provider
  returning 404 or 405 on `/mcp` is silently dropped (this is how App Studio
  and quickstart, which serve no `/mcp`, are excluded). Provider responses are
  capped at 96 MiB; over the cap is an error
  `provider <method> response exceeds the 96 MiB limit; the result is too large to federate`,
  never a truncated body.
- Org-owned (BYO) providers: federated only for a **human** bearer whose membership
  was verified in a **team workspace**. The org copy **shadows** the
  platform provider of the same name. It is reached over the platform edges
  tunnel with a 10-minute delegated user token minted for (user,
  workspace) plus `X-Railgrid-User`; the caller's bearer is never sent, and the
  provider's own `BackendURL` is never dialled. When no delegated token can
  be minted — a ServiceAccount bearer (including the MCPServer connect token
  that `railgrid mcp url` and `railgrid env` hand out, and App Studio project
  identities), an org-scope cluster, no issuer, no edge route — the provider
  is skipped for that request, and the shadowed platform copy does **not**
  come back.
- Identity forwarding to platform providers: the caller's bearer plus
  `X-Railgrid-Tenant` and `X-Railgrid-Cluster` set to the cluster ID.
- "Enabled" is not the filter. The aggregate lists every Ready provider in
  the Org catalog; a tool from a provider you have not enabled fails with
  RBAC or NotFound errors when called.
- **Scope and bearer type are the filter, and they bite.** On a hub where
  `infrastructure` is `scope: org`, your own hub token (OIDC/static) in a
  team workspace gets the org copy if its edge route works, but the
  long-lived connect token, a ServiceAccount, gets **no
  `infrastructure__*` at all** — so MCP clients configured from
  `railgrid mcp url` see no org-scoped providers; clients that run
  `railgrid mcp proxy` call as you and do.
  Always call `tools/list` with the bearer you will actually use before
  planning a route through a tool; the inventory below is what a provider
  *can* contribute, not what you have.

### Connecting

**As yourself: `railgrid mcp proxy`** (recommended; in the CLI since v0.1.33). A stdio MCP server in the
CLI. Each JSON-RPC line on stdin becomes one POST to the aggregate made with
your kubeconfig credentials (OIDC refreshed through `railgrid get-token`, the
hub CA trusted), and every JSON-RPC message of the reply comes back on
stdout as one line. A 401 reloads the credentials and retries once; any other
failure is a JSON-RPC error (code -32000) on the request's id; stderr is its
log. Flags: `--mcpserver-name` (default `default`), `--org`, `--workspace`;
without them it serves the `railgrid` context's workspace as it was when the
proxy started (after `railgrid use`, restart the client's MCP connection). The
railgrid Claude Code plugin registers it; elsewhere:

```bash
claude mcp add railgrid -- railgrid mcp proxy
codex mcp add railgrid -- railgrid mcp proxy
# mcpServers JSON: { "railgrid": { "command": "railgrid", "args": ["mcp", "proxy"] } }
```

The aggregate is stateless, so a shell needs no `initialize` handshake: pipe
one `tools/call` in and read one JSON line out. That is all `fmcp` (SKILL.md
section 0) does:

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"code__list_repositories","arguments":{}}}' \
  | railgrid mcp proxy 2>/dev/null | jq '.result.structuredContent'
```

**With the workspace's MCP token** (a machine without a railgrid login). The
`default` MCPServer's ServiceAccount token does not expire like an OIDC token,
but it gets no org-owned (BYO) provider tools.

```bash
railgrid mcp claude          # or: railgrid mcp codex — registers URL + token with the client (cli.md)
                             # flags: --mcpserver-name default, --name railgrid-<mcpserver-name>, --ca-file, --dry-run; claude adds --scope user|local|project
railgrid mcp url --mcpserver-name default        # prints the URL and client snippets with the token
eval "$(railgrid env)"                           # MCP_URL, MCP_TOKEN (the same token) plus HUB, CLUSTER, ORG, WS, TOKEN, AS
fc "$HUB/api/orgs/$ORG/workspaces/$WS/mcpservers/default/connect"
# {"endpointURL":…,"serverName":"railgrid","token":…,"tokenReady":true}
```

**Raw HTTP.** The endpoint is JSON-RPC over HTTP. `Accept` must contain
**both** `application/json` and `text/event-stream`, or it answers
`400 Accept must contain both 'application/json' and 'text/event-stream'`.
Replies arrive as SSE (`event: message`, then `data: {…}`): strip the leading
`data: ` and parse the last JSON object. A failing tool still returns HTTP 200
with `result.isError: true` and the text in `result.content[].text`.

```bash
curl -s -X POST "$MCP_URL" -H "Authorization: Bearer $MCP_TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"code__list_repositories","arguments":{}}}'
```

The aggregate caches each provider's tools and instructions per bearer
(fresh for 30 s, then stale-while-revalidate): a
provider that becomes Ready (or stops) shows up on the next request, but a
change in a Ready provider's own tool list reaches you up to 30 s late, one
request after that.

## 2. MCPServer CRD (`railgrid.ai/v1alpha1`, cluster-scoped, shortName `mcps`)

```yaml
apiVersion: railgrid.ai/v1alpha1
kind: MCPServer
metadata: { name: default }        # created automatically in every workspace
spec:
  displayName: ""
  instructions: ""                 # ≤ 8 KiB, overrides ambient guidance
  readOnly: false                  # drops write verbs from the SA role
status:
  phase: Provisioning|Ready|Error
  URL, tokenSecretRef {name, namespace}, conditions
  federatedProviders: [{name, displayName, reachable, message?, tools[{name,title,description}]}]
  toolsRefreshedTime
```

The controller provisions ServiceAccount `<name>-mcp` in namespace `default`,
its token Secret (`<name>-mcp-token`), and ClusterRole `railgrid:mcpserver:<name>`
built from the resources the workspace has bound plus the verbs and actions
each provider declares in its CatalogEntry (`readOnly` keeps only the ones
declared `readOnly`); federation
status refreshes every 60 s. `status.federatedProviders` is
enumerated as the server's own ServiceAccount: it reflects the Org's
shadowing but lists no org-owned providers (a human bearer may see more).
There is **no** edge label selector on the spec. Hub REST: `GET|POST /api/orgs/{org}/workspaces/{ws}/mcpservers`
(`POST {"name","displayName","instructions","readOnly"}` → 201 `{name, displayName, instructions, readOnly, phase: Provisioning}`),
`PATCH|DELETE …/{name}` (`DELETE …/{name}?uid=<metadata.uid>` deletes only
that object, so a recreated server of the same name is never removed by a stale
request), `GET …/{name}/connect` (token ready within seconds). To hand an agent narrower
access, create a workspace service account and use its token
([access.md](access.md) section 6), or a `readOnly` MCPServer: verified — its
token lists the same tools as `default`, and a write tool fails with the kube
RBAC text (`… is forbidden: User "system:serviceaccount:default:<name>-mcp" cannot create resource …`).

## 3. Per-edge MCP

```
https://<hub>/clusters/{clusterName}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{edge}/mcp
```

`railgrid mcp url --edge <name>` prints it (always the `kubernetesclusters`
resource). It is the edge's `mcp` verb — a kcp custom subresource on the edges
APIExport, authorized with your own RBAC on `kubernetesclusters/mcp` and
reverse-proxied to the provider. Kubernetes edges only: a `LinuxServer` or
`MacOSServer` has no `mcp` verb, so the printed URL does not resolve for a
server edge — use `railgrid ssh`. Same kube toolset as below without the
`cluster` parameter (the single edge is the default target).

## 4. Complete tool inventory

### `edges__*`

Kube tools from `containers/kubernetes-mcp-server` (toolsets core, config,
helm) over the workspace's connected Kubernetes edges. **The multi-edge
parts appear only with more than one connected edge**: then every tool takes
a `cluster` parameter naming the edge (defaulting to the first connected
one), and `cluster_list` exists. With a single connected
edge (verified 2026-09-13 on a hub with edge `local`) neither exists and every
call goes to that edge; an extra `cluster` argument is ignored.
`configuration_contexts_list` is never registered (it belongs to the
kubeconfig provider, not railgrid's). Tools:
`pods_list {labelSelector?}`, `pods_list_in_namespace {namespace}`,
`pods_get`, `pods_delete`, `pods_log`, `pods_exec {namespace, name, command}`,
`pods_run`, `pods_top`, `resources_list {apiVersion, kind, namespace?}`,
`resources_get`, `resources_create_or_update {resource}` (apply),
`resources_delete`, `resources_scale`, `namespaces_list`, `events_list`,
`nodes_log`, `nodes_stats_summary`, `nodes_top`, `configuration_view`,
(multi-edge only: `cluster_list`),
`helm_install {chart (a `.tgz` URL or `oci://` ref — no repos are
configured, so `stable/x` does not resolve), name?, namespace?, values?, cluster?}`
(~1 s for a small chart), `helm_list {namespace?, all_namespaces?, cluster?}`,
`helm_uninstall {name, namespace?, cluster?}`. `projects_list` exists but
OpenShift detection is hardcoded off. All of these return kubectl-style
plain text, not JSON. `pods_exec` runs argv in the container: scratch or
distroless images (e.g. `traefik/whoami`) have no `sh`/`cat`/`wget` and fail
with `executable file not found in $PATH`.

Per-`Service` tools, one bundle per Ready `Service` with a live tunnel,
named `<service>_<tool>` (the Service's `metadata.name`, sanitized, as the
prefix); the same list is projected onto the CR as `status.tools`:

| Service type | Tools |
|---|---|
| `home-assistant` | `states {domain?, limit?}`, `get_state {entity_id}`, `call_service {domain, service, entity_id?, data?}` (real physical action) |
| `qbittorrent` | `torrents`, `transfer`, `add`, `pause`, `resume`, `delete` |
| `prowlarr` | `indexers`, `search`, `status` |
| `sonarr` | `series`, `queue`, `calendar`, `lookup` |
| `radarr` | `movies`, `queue`, `lookup` |
| `grafana` | `search`, `datasources`, `health`, `query` |
| `grafana-loki` | `query`, `query_range`, `labels` |
| `prometheus` | `query`, `query_range`, `targets`, `alerts` |
| `jellyfin` | `sessions`, `system`, `search` |
| `plex` | `sessions`, `libraries`, `identity` |
| `portainer` | `endpoints`, `stacks`, `status` |
| `adguard` | `status`, `stats`, `filtering`, `protection` |
| `proxmox` | `nodes`, `resources`, `cluster_status` |
| `pihole` | `summary`, `blocking`, `disable`, `top_domains` |
| `unifi-network` | `sites`, `clients`, `devices` |
| `unifi-protect` | `cameras`, `snapshot` |
| `runner` | none (a coding harness the agent publishes on a host edge; `status.harness` says which) |
| `generic` | none (proxy only) |

Catalog-driven tools all take `{query?: map, form?: map, body?: string}`.
Service credentials live in a tenant Secret referenced by
`Service.spec.authSecretRef`; the provider injects the auth header, so the
token never reaches the agent host. No SSH or Linux tool family exists;
server edges are reached with `railgrid ssh`.

### `infrastructure__*`

`list_templates`, `describe_template`, `provision`, `list_instances`,
`get_instance`, `update_instance`, `delete_instance`, `dev_sync`, `dev_exec`,
`dev_logs`, `dev_restart`. Details in
[infrastructure.md](infrastructure.md).

### `code__*`

`list_connections`, `list_repositories`, `create_connection`,
`create_repository`, `delete_repository`, `commit_files`,
`checkout_repository`, `build_status`, `rebuild`, `add_deploy_key`,
`add_collaborator`, `remove_collaborator`. Details in [code.md](code.md).

### `agents__*`

Runs, agents, model credentials, connections, toolsets, schedules and
triggers (32 tools): inputs and what is deliberately left out in
[agents.md](agents.md) section 7.

### `kuery__*`

| Tool | Input | Notes |
|---|---|---|
| `kuery_query` | `spec` (raw kuery QuerySpec JSON), `savedView?` | One structured query over every engaged edge: filter by kind, namespace, labels; sparse projection; relation expansion. Read-only. Returns `{status: <QueryStatus>}` as structured content. |
| `kuery_impact` | `kind`, `name`, `edge?`, `group?`, `namespace?`, `maxDepth?` (5, max 20), `savedView?` | `{object, found, impactedBy, impacts, associated, summary}`; declared coupling only |

Both tools run the same gated verb the REST route does. Omit `savedView` and
they use your own scratch view, `playground-<first 12 hex of sha256(your identity)>`,
created in your workspace with your credential on first use.

REST: there is **one** route, a verb on a named `SavedView`
(`kuery.providers.railgrid.ai/v1alpha1`, cluster-scoped, shortName `sv`):

```bash
curl -sS -X POST "$HUB/clusters/$CLUSTER/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/<name>/run" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"input":{"query":<QuerySpec>}}'        # or {"input":{}} to run the view's own spec.query
```

The response is an action envelope: `result` is the `QueryStatus`, or `error`
carries `{code, message}`. kcp authorizes you on `savedviews/run` for that
name with ordinary RBAC (a ServiceAccount needs `get` on the view and `*` on
`savedviews/run`), then the provider checks you can `get` the view; every
refusal — no such view, no visibility, no grant — is the same `404`. A
workspace path in the cluster position is refused, not translated. The
QuerySpec JSON Schema is `GET $HUB/ui/providers/kuery/query-schema.json`.
The old `POST …/services/providers/kuery/api/query`, `GET …/api/edges`,
`GET …/api/status` and `GET …/api/query-schema` were deleted; nothing answers
under `/services/providers/kuery/api/`.

- **Enable it first.** `kuery` appears in `GET /api/providers` and its tools
  are on `tools/list` whether or not the workspace enabled it, but nothing is
  engaged until `POST …/providers/kuery/enable`. It depends on `edges`
  (enable that first); the claims to accept are edges `kubernetesclusters`
  (get/list/watch), the `kubernetesclusters/k8s` verb and
  `authorization.k8s.io` `subjectaccessreviews`. Until an edge is engaged a
  query is refused with `error.code: not_engaged` (also for a `cluster.name`
  outside the engaged set) — never answered with an empty result.
- **Engagement is provider-side.** After enabling, the provider's engagement
  reconciler watches the workspace's `KubernetesCluster` edges and syncs each
  connected one through the edges `k8s` verb as itself; a connected Kubernetes
  edge becomes queryable within about a minute. Still `not_engaged` minutes
  later with connected edges means that sync is failing in the provider, which
  only the operator can fix. Server and macOS edges are never engaged (no kube API).
- `kuery__kuery_query {spec}` takes the QuerySpec as a JSON object (the same
  `input.query` the REST route takes).
- Example body: `{"filter":{"objects":[{"groupKind":{"group":"","kind":"Pod"},"namespace":"kube-system"}]},"limit":10,"objects":{"cluster":true,"object":{"metadata":{"name":true,"namespace":true}}}}`.
  The response is a `QueryStatus`: `objects[{id?, cluster?, object, relations?}]`
  plus `incomplete: true` when `limit` cut the result (set `cursor: true` in
  the spec and page with the returned `cursor`), `count`, `warnings`.
  `objects.cluster: true` adds the edge as `<clusterID>/<edge>` (e.g.
  `1ngen6o0so3jwz2h/minis`); without it you cannot tell which edge an object
  came from. `{"root":"clusters"}` returns one
  `kuery.io/v1alpha1 Cluster` node per engaged edge (expand `members`).
- `kuery__kuery_impact` answers `found: false` with summary
  `<Kind>/<name> not found in the kuery store (sync may be catching up)` when
  the provider has not synced your workspace yet (a few
  seconds after enable or a provider restart); retry, or query with
  `objects.relations`.

Relations: upstream `owners`, `references`, `selects`,
`namespace`; downstream `descendants`, `selected-by`, `namespaced`, `members`;
lateral `linked`, `grouped`; append `+` for transitive.

### `databricks__*`

`list_tables`, `describe_table {tableRef}`, `query_table {actionVersion "v1", tableRef, columns?, limit? ≤100}`
(source in railgrid/providers, not this repo).

### Not on the aggregate

App Studio (an MCP client; its backend serves no `/mcp`) and quickstart (no
MCP).

## 5. Edges

Kinds in `edges.railgrid.ai/v1alpha1`: cluster-scoped `KubernetesCluster` (`kc`),
`LinuxServer` (`ls`), `MacOSServer` (`mac`), `Service` (`edgesvc`); namespaced
`Workload` (`wl`) and `Placement`. `railgrid.ai/v1alpha1` contains only `MCPServer`.
A `MacOSServer` is service-only: no `k8s`, `ssh` or `mcp` verb, just `Service`s
on the host ([docs/macos-edges.md](../../../docs/macos-edges.md)).

```bash
railgrid edge create home-lab [--labels env=home]      # KubernetesCluster; prints join guide
railgrid edge create my-vps --type server              # LinuxServer (--harness auto|none|claude,codex for host edges)
railgrid edge create mac-mini --type macos             # MacOSServer
railgrid edge join-command <name>
railgrid edge list ; railgrid edge get <name> [-o yaml] ; railgrid edge upgrade <name> ; railgrid edge delete <name> [-y]
```

A cluster and a server may share a name; qualify with `server/<name>`,
`kubernetes/<name>` where a command accepts either kind (`railgrid edge get
server/minis`, `railgrid ssh server/minis`, `railgrid connect kubernetes/minis`).

Join options printed by `edge create`:

```bash
helm install railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \
  --namespace railgrid-agent --create-namespace \
  --set agent.edgeName=<name> --set agent.hub.url=<hub> --set agent.hub.token=<token>
railgrid agent join --hub-url <hub> --edge-name <name> --type kubernetes|server --token <token>   # persistent
railgrid agent run  --hub-url <hub> --edge-name <name> --type kubernetes|server --token <token>   # foreground
sudo railgrid agent join --hub-url <hub> --edge-name <name> --type macos --worker-user "$USER" [--cluster <id>] --token <token>   # launchd
```

The join token is exchanged once for a hub-minted, TTL'd agent credential
(24 h cap) that the agent refreshes on reconnect at 80 % of the TTL. An agent
powered off past the TTL cannot refresh: annotate the edge
`edges.railgrid.ai/regenerate-join-token` and restart the agent with the new
join token ([docs/edges-agent-credentials.md](../../../docs/edges-agent-credentials.md)).

kubectl through the hub: `railgrid edge kubeconfig <name>` writes a kubeconfig
whose server is
`<hub>/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{edge}/k8s`
— the edge's `k8s` verb, a kcp custom subresource — with your own credentials:
kcp authorizes you on `kubernetesclusters/k8s` with RBAC, the provider checks
you can `get` the edge and forwards as you (`-o <file>` writes it, `--merge` adds a
`railgrid-<name>` context without switching). `railgrid connect <edge>` merges that
context **and makes it current**, so plain `kubectl` hits the edge until
`railgrid disconnect` (or `railgrid use`) points it back at the hub — remember that
`railgrid env`, `railgrid app` and friends read the `railgrid` context, so scripts
should prefer `kubectl --kubeconfig <file>` or `--context railgrid-<edge>`.

SSH: `railgrid ssh <server> [-- cmd]` over a WebSocket to the `ssh` verb
(`…/linuxservers/{name}/ssh`, the CLI sends the bearer in `Authorization`; a
browser uses the `base64url.bearer.authorization.k8s.io.<token>` subprotocol).
With `-- cmd` the command travels as `?cmd=` and a piped or file stdin is
forwarded (`stdin=1`); interactive mode needs a terminal. No port forwarding.
Host key policy on the `LinuxServer` spec: `sshHostKey`, `sshHostKeyPolicy strict|tofu`,
`sshPort`, `sshUserMapping inherited|provided|identity`, `sshKeySecretRef`, `sshCredentialsRef`,
plus `harness {mode auto|none|explicit, enabled[]}`.

Services: discovered by the agent (`edges.railgrid.ai/discovered=true`, named
`<edge>-<type>`) or declared. A declared one that worked (2026-09-11):

```yaml
apiVersion: edges.railgrid.ai/v1alpha1
kind: Service
metadata: { name: kiosk-whoami }
spec:
  type: generic
  edgeRef: { kind: KubernetesCluster, name: minis }     # kind LinuxServer (default) | MacOSServer | KubernetesCluster
  targetRef: { name: kiosk-whoami, namespace: kiosk }   # in-cluster Service; namespace required
  port: 80
  scheme: http
  auth: none                                            # none | passthrough | secret (default; expects authSecretRef)
```

`spec.host` instead of `targetRef` for LAN hosts (needs the agent's
`--svc-allow-cidr`; `host` wins when both are set; a `KubernetesCluster`
Service needs one of the two, a host edge defaults to loopback);
`tlsInsecureSkipVerify` for self-signed LAN devices; `instructions` (≤ 8 KiB)
is appended to the MCP guidance. `status.url` is a **path**, the `proxy` verb:
call `$HUB<status.url>/` (or a sub-path) with `Authorization: Bearer $TOKEN`
(401 without); `status.mcpURL` is the `mcp` verb for types with tools.
Reachability is probed on create/update and then with a
backoff from 5 s doubling up to 10 min while it fails (every 10 min once Ready), so a
Service created before its pod is Ready flips to `Ready` within seconds of
the backend answering. Data plane:
`/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/services/{name}/{proxy|mcp}`.

Workloads on edges: `Workload` (namespaced on the hub) with
`spec.placement.edgeSelector` and `strategy Spread|Singleton`, exactly one of
`simple`, `template`, `helm {repoURL, chart, version, values?}` (rendered provider-side), fanned into one
`Placement` per edge (`<workload>-<edge>`, `spec.manifests[]` is the rendered
output — read it before trusting the edge) applied by the agent with
server-side apply and prune. `kubectl get workloads,placements -A` lists them.
The portal no longer shows marketplace entry points for Helm workloads (#795);
the API is unchanged. Verified 2026-09-11, both edges Running in 8 s,
pruned within ~5 s of deleting the Workload:

```yaml
apiVersion: edges.railgrid.ai/v1alpha1
kind: Workload
metadata: { name: kiosk-whoami, namespace: kiosk }   # hub namespace only
spec:
  placement:
    strategy: Spread
    edgeSelector:
      matchExpressions:
        - { key: edges.railgrid.ai/name, operator: In, values: [home, minis] }
  replicas: 1                                        # per edge
  simple:
    image: traefik/whoami:v1.10
    ports: [{ name: http, containerPort: 80 }]
  access: { port: 80 }                               # renders a ClusterIP Service of the same name
```

- **Namespace on the edge:** `spec.targetNamespace` (DNS label, default
  `default`); the hub namespace is *not* carried over. A non-default target
  is created on the edge with the bundle and never pruned.
- `edgeSelector` matches `metadata.labels` on the `KubernetesCluster` objects; the only
  label every edge carries is `edges.railgrid.ai/name: <edge>` (stamped by the
  provider; add your own with
  `railgrid edge create --labels` or by labelling the CR).
- `spec.template.metadata` takes `labels` and `annotations` (merged onto the
  pod template; `edges.railgrid.ai/workload` stays the selector).
- **Private images:** `spec.simple.imagePullSecrets: [{name: ghcr-pull}]` (or
  `spec.template.spec.imagePullSecrets`). A Workload ships no Secrets: create a
  `docker-registry` Secret of that name in the target namespace on every
  selected edge first (through `railgrid edge kubeconfig`); never put the token
  in the Workload.
