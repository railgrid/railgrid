# Developer Reference

Deep-dive reference for contributors working on kedge internals.

## Table of Contents

- [Edge CRD Spec](#edge-crd-spec)
- [Join Token Flow](#join-token-flow)
- [Edge Proxy URL Format](#edge-proxy-url-format)
- [SSH Server-Mode Internals](#ssh-server-mode-internals)
- [kcp Workspace Hierarchy](#kcp-workspace-hierarchy)
- [MCP Integration](#mcp-integration)
- [Hub Controller Reference](#hub-controller-reference)

---

## Edge CRD Spec

**API group:** `kedge.faros.sh/v1alpha1`  
**Kind:** `Edge`

### Spec fields

```yaml
spec:
  type: kubernetes       # "kubernetes" | "server"
  displayName: ""        # human-readable label (optional)
  hostname: ""           # FQDN of the host, informational (optional)
  provider: ""           # aws | gcp | onprem | bare-metal (optional)
  region: ""             # deployment region label (optional)
```

### Status fields

```yaml
status:
  phase: ""              # "Ready" | "Pending" | "Error"
  connected: false       # true when the agent tunnel is active
  joinToken: ""          # base64url token set by TokenReconciler; cleared after registration
  URL: ""                # proxy URL for kubernetes-type edges (kubectl endpoint)
  sshCredentials:        # populated when agent sends SSH creds via WebSocket headers
    username: ""
    secretRef: ""        # name of Secret in kedge-system holding the SSH password/key
  conditions:
  - type: Registered     # True once the agent has completed its first join
    status: "False"      # False = AwaitingAgent, True = registered
    reason: ""
    message: ""
```

### Example manifests

**Kubernetes-type edge:**

```yaml
apiVersion: kedge.faros.sh/v1alpha1
kind: Edge
metadata:
  name: my-cluster
spec:
  type: kubernetes
  displayName: "Production k3s cluster"
  provider: onprem
  region: home-lab
```

**Server-type edge:**

```yaml
apiVersion: kedge.faros.sh/v1alpha1
kind: Edge
metadata:
  name: my-server
spec:
  type: server
  displayName: "NUC home server"
  hostname: nuc.local
  provider: bare-metal
  region: home-lab
```

---

## Join Token Flow

```
kedge edge create <name>
        │
        ▼
TokenReconciler (pkg/hub/controllers/edge/token_reconciler.go)
  - generates 44-char crypto/rand base64url token
  - writes to edge.status.joinToken
  - sets Registered=False condition
        │
        ▼
kedge edge join-command <name>
  → prints: kedge agent run --token <joinToken> --edge-name <name> ...
        │
        ▼
Agent starts (pkg/agent/agent.go)
  opts.Token != "" → join-token mode
  - skips registerEdge (token is NOT a kcp bearer)
  - skips edge_reporter (hub owns status in this mode)
  - builds sshHeaders from --ssh-user/--ssh-password flags
        │
        ▼
pkg/agent/tunnel/tunneler.go: StartProxyTunnel(extraHeaders)
  - WebSocket upgrade to hub /proxy endpoint
  - merges sshHeaders into upgrade request
        │
        ▼
pkg/virtual/builder/agent_proxy_builder_v2.go: ServeHTTP
  - authorizeByJoinToken validates token against edge.status.joinToken
  - extractSSHCredsFromHeaders reads X-Kedge-SSH-* headers
  - markEdgeConnected(edge, sshCreds):
      • builds agent kubeconfig (SA token from edge-<name>-kubeconfig secret)
      • sends kubeconfig back to agent in X-Kedge-Agent-Kubeconfig response header
      • storeSSHCredentials → creates Secret, writes status.sshCredentials
      • clears joinToken, sets Registered=True, sets phase=Ready
        │
        ▼
Agent receives kubeconfig (cmd/kedge-agent/main.go)
  - saves to ~/.kedge/agent-<name>.kubeconfig
  - clears opts.Token (reconnect without token from now on)
        │
        ▼
On restart: agent loads saved kubeconfig automatically
  opts.UsingSavedKubeconfig=true → skips registerEdge, skips edge_reporter
```

### Why the agent skips `edge_reporter` in join-token mode

The `edge_reporter` uses the agent's credentials to patch `edge.status` directly via the kcp API. Join tokens are not kcp Service Account credentials — they're a hub-internal bootstrap secret validated only by the hub's `authorizeByJoinToken` handler. So the hub itself calls `markEdgeConnected` / `markEdgeDisconnected` to update status, and the agent skips the reporter entirely.

After token exchange, the agent holds a real kcp SA kubeconfig and future reconnects use the normal `edge_reporter` path.

---

## Edge Proxy URL Format

Once an Edge is `Ready`, `edge.status.URL` is set to:

```
https://<hub-external-url>/clusters/<workspace-id>/apis/kedge.faros.sh/v1alpha1/edges/<name>/proxy/k8s
```

This URL is a virtual workspace endpoint served by the hub's agent-proxy virtual workspace handler. The hub:

1. Validates the caller's bearer token against kcp.
2. Looks up the Edge resource to verify it is Ready and the tunnel is active.
3. Forwards the raw TCP stream to the agent over the revdial tunnel.
4. The agent forwards to `localhost:<kubeAPIPort>` on the target cluster.

`kedge kubeconfig edge <name>` generates a kubeconfig pointing to this URL with the user's hub bearer token embedded.

---

## SSH Server-Mode Internals

### Connection path

```
kedge ssh <name>
    │
    │  WebSocket upgrade → hub /clusters/<ws>/…/edges/<name>/proxy/ssh
    ▼
hub agent-proxy handler (pkg/virtual/builder/agent_proxy_builder_v2.go)
    │
    │  dials agent over revdial tunnel
    ▼
kedge-agent (server mode, pkg/agent/agent.go)
    │
    │  forwards raw TCP to localhost:<ssh-proxy-port> (default 22)
    ▼
sshd on the target host
```

### SSH credentials in join-token mode

When the agent starts with `--token` and `--ssh-user`/`--ssh-password`:

1. Agent builds `X-Kedge-SSH-User` and `X-Kedge-SSH-Password` headers.
2. These are passed to `StartProxyTunnel` as `extraHeaders`.
3. Hub's `extractSSHCredsFromHeaders` reads them on the first connection.
4. `storeSSHCredentials` creates a `kedge-ssh-<name>` Secret in `kedge-system`.
5. `edge.status.sshCredentials` is populated with the username and secret ref.

### SSH username mapping (OIDC)

With OIDC auth, the SSH username is derived from the user's token claim:
- Default: email local-part (`alice@example.com` → `alice`)
- Configurable via the OIDC username claim in hub config

With static token auth, username defaults to `root`.

### Keepalive

The SSH test suite holds connections open for `--ssh-keepalive-duration` (default 60s). The hub sends keepalive pings every 30s over the WebSocket; agents respond to prevent idle-timeout disconnection.

---

## kcp Workspace Hierarchy

kedge uses kcp for multi-tenant API isolation. Each user/team gets a dedicated kcp workspace:

```
root workspace
└── user-<id> workspace
    ├── Edge resources
    ├── VirtualWorkload resources
    └── Placement resources
```

The hub deploys kcp's `APIBinding` resources to make the kedge CRDs available in each workspace.

### Static token scoping

Static dev tokens (e.g. `dev-token`) are scoped to a specific kcp workspace path. The workspace path appears in the kubeconfig server URL:

```
https://kedge.localhost:9443/clusters/<workspace-id>/...
```

`ClusterNameFromKubeconfig` (in `test/e2e/framework/cluster.go`) extracts the workspace ID from the server URL.

---

## MCP Integration

kedge exposes all connected Kubernetes clusters as a single [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server. AI agents (Claude, Cursor, Copilot, etc.) connect to this endpoint and can interact with all registered clusters using natural language.

### Kubernetes CRD (mcp.kedge.faros.sh/v1alpha1)

**API group:** `mcp.kedge.faros.sh/v1alpha1`  
**Kind:** `Kubernetes`

The `Kubernetes` object is automatically created as `default` in every tenant workspace by the hub bootstrapper. It acts as the configuration object for the multi-cluster MCP endpoint.

```yaml
apiVersion: mcp.kedge.faros.sh/v1alpha1
kind: Kubernetes
metadata:
  name: default
spec:
  edgeSelector: {}   # empty = all kubernetes-type edges; use label selectors to restrict
  readOnly: false    # set true to disable write operations (create/delete/apply)
status:
  URL: "https://hub.example.com/services/mcp/root:kedge:user-<id>/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp"
  connectedEdges:
  - my-cluster
  - home-lab
```

### MCP URL structure

```
https://<hub>/services/mcp/<workspace-cluster-id>/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/<name>/mcp
```

- `<workspace-cluster-id>` — kcp logical cluster name for the tenant workspace (from the server URL in the user's kubeconfig)
- `<name>` — name of the `Kubernetes` object (usually `default`)

### Request flow

```
AI agent (Claude / Cursor / …)
    │  POST /services/mcp/<cluster>/…/default/mcp
    │  Authorization: Bearer <user-token>
    ▼
hub MCP virtual workspace handler (pkg/virtual/builder/mcp_builder.go)
    │
    │  1. Extract bearer token, validate against kcp
    │  2. Resolve workspace cluster from URL path
    │  3. Fetch Kubernetes object for edgeSelector
    │  4. List all edges in the workspace
    │  5. Filter: kubernetes-type only + connected (tunnel active) + label selector
    │  6. Build MultiEdgeKedgeEdgeProvider (one per request)
    │
    ▼
kubernetes-mcp-server (github.com/containers/kubernetes-mcp-server)
    │  MCP Streamable-HTTP protocol (tools/list, tools/call)
    │
    ▼
KedgeEdgeProvider / MultiEdgeKedgeEdgeProvider (pkg/virtual/builder/mcp_provider.go)
    │  GetTargets() → list of connected cluster names
    │  GetDerivedKubernetes(cluster) → rest.Config via revdial tunnel
    │
    ▼
Agent tunnel → target cluster kube-apiserver
```

### Target parameter

When an MCP client wants to target a specific cluster, it passes `?cluster=<edge-name>` as a query parameter. The parameter name is `cluster` (not `edge`) because users think in terms of clusters.

### Toolsets

The MCP server registers toolsets from `kubernetes-mcp-server` via blank imports in `mcp_builder.go`:

```go
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/config"
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/core"
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/helm"
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kcp"
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kiali"
_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kubevirt"
```

Each toolset registers its tools via `init()`. Without the blank imports, `tools/list` returns empty.

### Adding to Claude Code

```bash
kedge mcp url --name default
# prints URL + ready-to-use claude mcp add command with your token
```

Or manually:

```bash
claude mcp add --transport http kedge \
  "https://hub.example.com/services/mcp/<cluster-id>/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp" \
  -H "Authorization: Bearer <token-from-kubeconfig>"
```

### Per-edge MCP (single cluster)

Each edge also exposes a direct MCP endpoint (independent of the multi-edge `Kubernetes` resource):

```
https://<hub>/services/agent-proxy/<workspace-cluster-id>/apis/kedge.faros.sh/v1alpha1/edges/<name>/mcp
```

```bash
kedge mcp url --edge my-cluster
```

This bypasses the `Kubernetes` MCP resource and connects directly to a single edge's Kubernetes API.

---

## Hub Controller Reference

| Controller | Package | Responsibility |
|-----------|---------|----------------|
| `TokenReconciler` | `pkg/hub/controllers/edge/token_reconciler.go` | Generates join tokens; skips if `Registered=True` |
| `RBACReconciler` | `pkg/hub/controllers/edge/rbac_reconciler.go` | Creates SA, ClusterRole, ClusterRoleBinding, kubeconfig Secret per edge |
| `EdgeController` | `pkg/hub/controllers/edge/controller.go` | Main edge lifecycle reconciler |
| `agent-proxy builder` | `pkg/virtual/builder/` | Virtual workspace that handles tunnel auth, status updates, SSH credential storage |
