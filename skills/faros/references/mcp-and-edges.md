# MCP aggregate, MCPServer, edges, kuery reference

Sources: `pkg/hub/mcpaggregate/`, `pkg/apiurl/`, `providers/edges/`,
`providers/kuery/`. There is no `list_targets` tool; use
`edges__cluster_list`.

## 1. The aggregate endpoint

```
https://<hub>/services/mcpserver/{clusterName}/apis/faros.sh/v1alpha1/mcpservers/{name}/mcp
```

- Transport: MCP streamable HTTP, **stateless**; a fresh server per request.
- Auth: `Authorization: Bearer` only. Accepted: the MCPServer's own
  ServiceAccount token (`system:serviceaccount:default:<name>-mcp`), a hub
  user with membership in that workspace, or another tenant ServiceAccount
  with `use` on `faros.sh/mcpservers/<name>`. 401 unauthenticated, 403 wrong
  tenant, 429 rate limited (retry after 60 s), 503 verifier unavailable.
  Verifications and the resolved tenant cache for 60 s. A ServiceAccount
  bearer also needs the cluster's workspace path
  resolved after TokenReview: an unknown cluster is 403, a lookup outage is
  503 (fails closed rather than guessing the platform catalog).
- Server identity `faros-mcpserver`; instructions tell the model tools are
  namespaced `<provider>__<tool>` and append each provider's own
  instructions under "Provider guidance". One resource: `faros://about`.
- Federation: every Ready provider in the verified caller's Org catalog
  (`ListForOrg`: platform providers plus that Org's own) whose backend
  answers `POST /mcp` `tools/list` (8 s discovery, 90 s per call). Tools are
  re-registered as `<provider>__<tool>` in deterministic order. A provider
  returning 404 or 405 on `/mcp` is silently dropped (this is how App Studio,
  an MCP client, is excluded). Provider responses are capped at 96 MiB;
  over the cap is an error
  `provider <method> response exceeds the 96 MiB limit; the result is too large to federate`,
  never a truncated body.
- Org-owned (BYO) providers: federated only for a **human** bearer whose membership
  was verified in a **team workspace**. The org copy **shadows** the
  platform provider of the same name. It is reached over the platform edges
  tunnel with a 10-minute delegated user token minted for (user,
  workspace) plus `X-Faros-User`; the caller's bearer is never sent, and the
  provider's own `BackendURL` is never dialled. When no delegated token can
  be minted — a ServiceAccount bearer (including the MCPServer connect token
  that `faros mcp url` and `faros env` hand out, and App Studio project
  identities), an org-scope cluster, no issuer, no edge route — the provider
  is skipped for that request, and the shadowed platform copy does **not**
  come back.
- Identity forwarding to platform providers: the caller's bearer plus
  `X-Faros-Tenant` and `X-Faros-Cluster` set to the cluster ID.
- "Enabled" is not the filter. The aggregate lists every Ready provider in
  the Org catalog; a tool from a provider you have not enabled fails with
  RBAC or NotFound errors when called.
- **Scope and bearer type are the filter, and they bite.** On a hub where
  `infrastructure` is `scope: org`, your own hub token (OIDC/static) in a
  team workspace gets the org copy if its edge route works, but the
  long-lived connect token, a ServiceAccount, gets **no
  `infrastructure__*` at all** — so MCP clients configured from
  `faros mcp url` see no org-scoped providers.
  Always call `tools/list` with the bearer you will actually use before
  planning a route through a tool; the inventory below is what a provider
  *can* contribute, not what you have.

Get the URL and a token:

```bash
faros mcp url --mcpserver-name default        # long-lived token from the connect endpoint, also on OIDC hubs
eval "$(faros env)"                           # MCP_URL, MCP_TOKEN (same connect token) plus TOKEN, ORG, WS, …
curl -s "$HUB/api/orgs/$ORG/workspaces/$WS/mcpservers/default/connect" -H "$A" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS"
# {"endpointURL":…,"serverName":"faros","token":…,"tokenReady":true}
```

The connect token is the MCPServer's ServiceAccount token: it does not
expire like an OIDC token, but it gets no org-owned (BYO) provider tools.
For those, call the endpoint with your own hub bearer (`$TOKEN`).

**Calling it without an MCP client.** The endpoint is plain JSON-RPC over
HTTP, so `curl` is enough (SKILL.md section 8, "MCP over plain HTTP").
`Accept` must contain **both** `application/json` and `text/event-stream`,
or the endpoint answers
`400 Accept must contain both 'application/json' and 'text/event-stream'`.
Replies always arrive as SSE (`event: message`, then `data: {…}`), so strip
the leading `data: ` and parse the last JSON object. A failing tool still
returns HTTP 200 with the error text in `result.content[].text`, so never
judge success by status code alone.

```bash
curl -s -X POST "$MCP_URL" -H "Authorization: Bearer $MCP_TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"code__list_repositories","arguments":{}}}'
```

Client snippets:

```bash
claude mcp add --transport http faros "<url>" -H "Authorization: Bearer <token>"
export FAROS_MCP_TOKEN='<token>'; codex mcp add faros --url '<url>' --bearer-token-env-var FAROS_MCP_TOKEN
```
```json
{ "mcpServers": { "faros": { "url": "<url>", "headers": { "Authorization": "Bearer <token>" } } } }
```

## 2. MCPServer CRD (`faros.sh/v1alpha1`, cluster-scoped, shortName `mcps`)

```yaml
apiVersion: faros.sh/v1alpha1
kind: MCPServer
metadata: { name: default }        # created automatically in every workspace
spec:
  displayName: ""
  instructions: ""                 # ≤ 8 KiB, overrides ambient guidance
  readOnly: false                  # drops write verbs from the SA role
status:
  phase: Provisioning|Ready|Error
  URL, tokenSecretRef {name, namespace}, conditions
  federatedProviders: [{name, displayName, reachable, tools[{name,title,description}]}]
  toolsRefreshedTime
```

The controller provisions ServiceAccount `<name>-mcp` in namespace `default`,
its token Secret, and ClusterRole `faros:mcpserver:<name>`; federation
status refreshes every 60 s. `status.federatedProviders` is
enumerated as the server's own ServiceAccount: it reflects the Org's
shadowing but lists no org-owned providers (a human bearer may see more).
There is **no** edge label selector on the spec. Hub REST: `GET|POST /api/orgs/{org}/workspaces/{ws}/mcpservers`
(`POST {"name","displayName","readOnly"}` → 201 `{name, displayName, readOnly, phase: Provisioning}`),
`PATCH|DELETE …/{name}`, `GET …/{name}/connect` (token ready within seconds). To hand an agent narrower
access, create a workspace service account and use its token
([access.md](access.md) section 6), or a `readOnly` MCPServer: verified — its
token lists the same tools as `default`, and a write tool fails with the kube
RBAC text (`… is forbidden: User "system:serviceaccount:default:<name>-mcp" cannot create resource …`).

## 3. Per-edge MCP

```
https://<hub>/services/providers/edges/agent/{clusterName}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{edge}/mcp
```

`faros mcp url --edge <name>`. Kubernetes edges only: for a server edge the
URL is still printed and `tools/list` answers 200 with the kube tools, but
every `tools/call` fails with `an error on the server ("upstream
unavailable")` — use `faros ssh`. Same kube toolset as below without the
`cluster` parameter.

## 4. Complete tool inventory

### `edges__*`

Kube tools from `containers/kubernetes-mcp-server` (toolsets core, config,
helm), with a `cluster` parameter selecting the edge on the fleet endpoint:
`pods_list {labelSelector?}`, `pods_list_in_namespace {namespace}`,
`pods_get`, `pods_delete`, `pods_log`, `pods_exec {namespace, name, command}`,
`pods_run`, `pods_top`, `resources_list {apiVersion, kind, namespace?}`,
`resources_get`, `resources_create_or_update {resource}` (apply),
`resources_delete`, `resources_scale`, `namespaces_list`, `events_list`,
`nodes_log`, `nodes_stats_summary`, `nodes_top`, `configuration_view`,
`configuration_contexts_list`, `cluster_list` (enumerates connected edges),
`helm_install {chart (a `.tgz` URL or `oci://` ref — no repos are
configured, so `stable/x` does not resolve), name?, namespace?, values?, cluster?}`
(~1 s for a small chart), `helm_list {namespace?, all_namespaces?, cluster?}`,
`helm_uninstall {name, namespace?, cluster?}`. `projects_list` exists but
OpenShift detection is hardcoded off. All of these return kubectl-style
plain text, not JSON. `pods_exec` runs argv in the container: scratch or
distroless images (e.g. `traefik/whoami`) have no `sh`/`cat`/`wget` and fail
with `executable file not found in $PATH`.

Per-`Service` tools, one bundle per Ready `Service` with a live tunnel,
named `<service>_<tool>`:

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
| `unifi-protect` | `cameras`, `snapshot`, `events` |
| `generic` | none (proxy only) |

Catalog-driven tools all take `{query?: map, form?: map, body?: string}`.
Service credentials live in a tenant Secret referenced by
`Service.spec.authSecretRef`; the provider injects the auth header, so the
token never reaches the agent host. No SSH or Linux tool family exists;
server edges are reached with `faros ssh`.

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

`run_agent`, `get_run`, `list_runs`, `list_agents`, `get_agent`,
`create_agent`, `update_agent`, `delete_agent`, `list_model_credentials`,
`save_model_credential`, `delete_model_credential`, `test_model_credential`,
`list_connections`, `create_connection`, `update_connection`,
`delete_connection`, `test_connection`, `list_toolsets`, `create_toolset`,
`update_toolset`, `delete_toolset`, `list_triggers`, `create_trigger`,
`update_trigger`, `delete_trigger`, `run_trigger`, `list_schedules`,
`create_schedule`, `update_schedule`, `delete_schedule`, `run_schedule`,
`list_tool_families`. Details in [agents.md](agents.md).

### `kuery__*`

| Tool | Input | Notes |
|---|---|---|
| `kuery_query` | `spec` (raw kuery QuerySpec JSON) | One structured query over every engaged edge: filter by kind, namespace, labels; sparse projection; relation expansion. Read-only. |
| `kuery_impact` | `kind`, `name`, `edge?`, `group?`, `namespace?`, `maxDepth?` (5, max 20) | `{impactedBy, impacts, associated, summary}`; declared coupling only |

REST: `POST $HUB/services/providers/kuery/api/query`, `GET …/api/query-schema`,
`GET …/api/edges` → `{tenant: <clusterID>, edges: [<edge>…], clusters: ["<clusterID>/<edge>"…]}`.
`GET …/api/status` reports `engagedEdges` and your `tenant`. Every kuery
route identifies you by `X-Faros-Cluster` (the hub sets it); a workspace path
in `X-Faros-Tenant` is rejected with 400.

- **Enable it first.** `kuery` appears in `GET /api/providers` and its tools
  are on `tools/list` whether or not the workspace enabled it, but nothing is
  engaged until `POST …/providers/kuery/enable` (accept the four claims the
  catalog lists: serviceaccounts, secrets, clusterroles, clusterrolebindings).
  Until then every query answers `{}` and `GET …/api/edges` is `{"edges":[]}`.
- **Engagement is provider-side.** After enabling, the provider polls the
  workspace's `KubernetesCluster` edges and syncs each through the edges
  proxy; connected Kubernetes edges show up in `GET …/api/edges` within a
  minute. `engagedEdges: 0` minutes later with connected edges means that sync
  is failing in the provider, which only the operator can fix. Server edges
  are never engaged (no kube API).
- `kuery__kuery_query {spec}` takes the QuerySpec as a JSON object (the same
  body the REST route takes) and returns the `QueryStatus` as text.
- Example body: `{"filter":{"objects":[{"groupKind":{"group":"","kind":"Pod"},"namespace":"kube-system"}]},"limit":10,"objects":{"cluster":true,"object":{"metadata":{"name":true,"namespace":true}}}}`.
  The response is a `QueryStatus`: `objects[{cluster?, object}]` plus
  `incomplete: true` when `limit` cut the result (page with `cursor`).
  `objects.cluster: true` adds the edge as `<clusterID>/<edge>` (e.g.
  `1ngen6o0so3jwz2h/minis`); without it you cannot tell which edge an object
  came from. `{"root":"clusters"}` returns one
  `kuery.io/v1 Cluster` per engaged edge. A bare `{}` means no engaged edge,
  not an empty fleet. Field list: `GET …/api/query-schema`.
- `kuery__kuery_impact` answers `found: false … not known to kuery yet` when
  the provider has not reconciled your workspace since it started (a few
  seconds after enable or a provider restart); retry, or query with
  `objects.relations`.

Relations: upstream `owners`, `references`, `selects`,
`namespace`; downstream `descendants`, `selected-by`, `namespaced`, `members`;
lateral `linked`, `grouped`; append `+` for transitive.

### `databricks__*`

`list_tables`, `describe_table {tableRef}`, `query_table {actionVersion "v1", tableRef, columns?, limit? ≤100}`.

### Not on the aggregate

App Studio (client only), quickstart (no MCP), secrets (backend lives in a
separate repo; kinds `SecretStore` with a Vault backend and `SyncedSecret`
materializing plain Secrets on a refresh interval; MCP support UNVERIFIED).

## 5. Edges

Kinds in `edges.faros.sh/v1alpha1`: `KubernetesCluster` (`kc`),
`LinuxServer` (`ls`), `Service` (`edgesvc`), namespaced `Workload` and
`Placement`. `faros.sh/v1alpha1` contains only `MCPServer`.

```bash
faros edge create home-lab [--labels env=home]      # KubernetesCluster; prints join guide
faros edge create my-vps --type server              # LinuxServer
faros edge join-command <name>
faros edge list ; faros edge get <name> ; faros edge upgrade <name> ; faros edge delete <name>
```

Join options printed by `edge create`:

```bash
helm install faros-agent oci://ghcr.io/faroshq/charts/faros-agent \
  --namespace faros-agent --create-namespace \
  --set agent.edgeName=<name> --set agent.hub.url=<hub> --set agent.hub.token=<token>
faros agent join --hub-url <hub> --edge-name <name> --type kubernetes|server --token <token>   # persistent
faros agent run  --hub-url <hub> --edge-name <name> --type kubernetes|server --token <token>   # foreground
```

kubectl through the hub: `faros edge kubeconfig <name>` writes a kubeconfig
whose server is
`<hub>/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{edge}/k8s`
with your own credentials; the provider does a SubjectAccessReview for verb
`proxy` and forwards as you (`-o <file>` writes it, `--merge` adds a
`faros-<name>` context without switching). `faros connect <edge>` merges that
context **and makes it current**, so plain `kubectl` hits the edge until
`faros disconnect` (or `faros use`) points it back at the hub — remember that
`faros env`, `faros app` and friends read the `faros` context, so scripts
should prefer `kubectl --kubeconfig <file>` or `--context faros-<edge>`.

SSH: `faros ssh <server> [-- cmd]` over a WebSocket to the `ssh` subresource.
Host key policy on the `LinuxServer` spec: `sshHostKey`, `sshHostKeyPolicy strict|tofu`,
`sshPort`, `sshUserMapping`, `sshKeySecretRef`, `sshCredentialsRef`.

Services: discovered by the agent (`edges.faros.sh/discovered=true`, named
`<edge>-<type>`) or declared. A declared one that worked (2026-09-11):

```yaml
apiVersion: edges.faros.sh/v1alpha1
kind: Service
metadata: { name: kiosk-whoami }
spec:
  type: generic
  edgeRef: { kind: KubernetesCluster, name: minis }     # required
  targetRef: { name: kiosk-whoami, namespace: kiosk }   # in-cluster Service; namespace required
  port: 80
  scheme: http
  auth: none                                            # none | passthrough | secret (default; expects authSecretRef)
```

`spec.host` instead of `targetRef` for LAN hosts (needs the agent's
`--svc-allow-cidr`). `status.url` is a **path**: call
`$HUB<status.url>/` (or a sub-path) with `Authorization: Bearer $TOKEN`
(401 without). Reachability is probed on create/update and then with a
backoff from 5 s up to 10 min while it fails (every 10 min once Ready), so a
Service created before its pod is Ready flips to `Ready` within seconds of
the backend answering. Data plane:
`/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.faros.sh/v1alpha1/services/{name}/{proxy|mcp}`.

Workloads on edges: `Workload` (namespaced on the hub) with
`spec.placement.edgeSelector` and `strategy Spread|Singleton`, exactly one of
`simple`, `template`, `helm` (rendered provider-side), fanned into one
`Placement` per edge (`<workload>-<edge>`, `spec.manifests[]` is the rendered
output — read it before trusting the edge) applied by the agent with
server-side apply and prune. `kubectl get workloads,placements -A` lists them
(`faros get workloads|placements` still works but is deprecated; its READY
column prints total-ready/per-edge replicas, so `2/1` is healthy for two edges). Verified 2026-09-11, both edges Running in 8 s,
pruned within ~5 s of deleting the Workload:

```yaml
apiVersion: edges.faros.sh/v1alpha1
kind: Workload
metadata: { name: kiosk-whoami, namespace: kiosk }   # hub namespace only
spec:
  placement:
    strategy: Spread
    edgeSelector:
      matchExpressions:
        - { key: edges.faros.sh/name, operator: In, values: [home, minis] }
  replicas: 1                                        # per edge
  simple:
    image: traefik/whoami:v1.10
    ports: [{ name: http, containerPort: 80 }]
  access: { port: 80 }                               # renders a ClusterIP Service of the same name
```

- **Namespace on the edge:** `spec.targetNamespace` (DNS label, default
  `default`); the hub namespace is *not* carried over. A non-default target
  is created on the edge with the bundle and never pruned.
- `edgeSelector` matches labels on the `KubernetesCluster` objects; the only
  label every edge carries is `edges.faros.sh/name: <edge>` (add your own with
  `faros edge create --labels` or by labelling the CR).
- `spec.template.metadata` takes `labels` and `annotations` (merged onto the
  pod template; `edges.faros.sh/workload` stays the selector).
- **Private images:** `spec.simple.imagePullSecrets: [{name: ghcr-pull}]` (or
  `spec.template.spec.imagePullSecrets`). A Workload ships no Secrets: create a
  `docker-registry` Secret of that name in the target namespace on every
  selected edge first (through `faros edge kubeconfig`); never put the token
  in the Workload.
