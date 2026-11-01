# Developer Reference

Deep-dive reference for contributors working on railgrid internals.

## Table of Contents

- [Edge CRD Spec](#edge-crd-spec)
- [Join Token Flow](#join-token-flow)
- [Edge Proxy URL Format](#edge-proxy-url-format)
- [SSH Server-Mode Internals](#ssh-server-mode-internals)
- [kcp Workspace Hierarchy](#kcp-workspace-hierarchy)
- [MCP Integration](#mcp-integration)
- [Hub Controller Reference](#hub-controller-reference)

## Local Development with Tilt

For the fastest local dev loop, use [Tilt](https://tilt.dev/) instead of running `make` in multiple terminals.

### Prerequisites
- [tilt](https://docs.tilt.dev/install.html) ≥ v0.35.0

### Start everything

```bash
make tilt
```

Use `make tilt` to start the supported single-checkout development stack. It
allocates checkout-specific ports and resource names, creates or reuses that
checkout's kind cluster, and starts Tilt with the matching Kubernetes context
and API port. `make tilt-status` prints the saved checkout key and ports.

Bare `tilt up` still evaluates the Tiltfile's `prepare-env` helper, which
allocates the checkout's child-service ports and resource names. It bypasses
the launcher, though, so it skips the startup checks, pre-start kind-cluster
creation, Kubernetes context selection, Tilt API-port selection, and ownership
lifecycle. It uses Tilt's default API port and the current Kubernetes context,
so it can collide with another stack or target the wrong cluster. `make tilt`
and `make tilt-cluster` still cannot run side by side.

The main browser-facing resources are:

1. **`portal`** — Vite dev server on `http://localhost:<PORTAL_PORT>/ui/`; get
   the allocated port from `make tilt-status`. Builds provider portal symlinks
   automatically and watches `portal/src/` for hot reload.

2. **`hub`** — `railgrid-hub` binary with embedded KCP, static auth, and portal dev proxy  
   Serves HTTPS at `https://console-<checkout-key>.127.0.0.1.sslip.io:<HUB_PORT>`; `make tilt-status` shows the key and port. `https://localhost:<HUB_PORT>` also works. The hub depends on the portal resource and rebuilds on Go file changes.

### Smoke test

```bash
tilt_status="$(make -s tilt-status)"
hub_port="$(printf '%s' "$tilt_status" | python3 -c 'import json, sys; print(json.load(sys.stdin)["ports"]["HUB_PORT"])')"
portal_port="$(printf '%s' "$tilt_status" | python3 -c 'import json, sys; print(json.load(sys.stdin)["ports"]["PORTAL_PORT"])')"
curl -k "https://localhost:${hub_port}/healthz"   # hub healthz
curl -k "https://localhost:${hub_port}/ui/"       # portal via hub proxy
curl "http://localhost:${portal_port}/ui/"         # portal direct (Vite dev server)
```

### Stop everything

For Tilt-only shutdown, use the allocated API port:

```bash
tilt_port="$(make -s tilt-status | python3 -c 'import json, sys; print(json.load(sys.stdin)["ports"]["TILT_PORT"])')"
tilt down --port "$tilt_port"
```

For checkout-scoped teardown of this Tilt stack's processes, kind clusters, and
containers while retaining local evidence, run `make dev-browser-down`.

### Tips
- The Tiltfile intentionally skips the slow provider-portal Vite builds (`make build-hub`) because `--portal-dev-url` proxies all UI traffic to the Vite dev server.
- If the portal's allocated port is already held, check `make tilt-status`, stop the process using that port, then retry `make tilt`.

### Application preview domain

Tilt generates a checkout-specific `apps-<checkout-key>.127.0.0.1.sslip.io`
domain when `RAILGRID_APP_BASE_DOMAIN` is unset or empty. To use a custom
wildcard zone, copy [`.env.example`](.env.example) to the ignored root `.env`
file and set `RAILGRID_APP_BASE_DOMAIN` to the base zone, such as
`apps.example.com` (omit `*.`). The domain must resolve to the local preview
listener or to a trusted front proxy that forwards to it.

For these settings, precedence is Make command-line assignment, then the root
`.env`, then an inherited shell value, then the checkout default. For a
one-run override, pass the value to Make:

```bash
make tilt RAILGRID_APP_BASE_DOMAIN=apps.example.com
```

Prefixing with `RAILGRID_APP_BASE_DOMAIN=apps.example.com make tilt` supplies
only an inherited shell value; a value in root `.env` overrides it. Tilt
watches `.env` and reloads the preview Gateway and Infrastructure provider
when it changes. A Make command-line value stays in effect across those
reloads even if it matches `.env`; an explicitly empty command-line domain
keeps the checkout default, and an empty public port remains 443. Restart with
`make tilt` and no override to use the updated `.env` value.

Tilt uses this one value for the Infrastructure provider's generated app hosts,
the preview Gateway's wildcard listener and certificate, the in-cluster browser
DNS override, and the host-side tunnel probe. `RAILGRID_APP_PUBLIC_PORT` is
optional: when unset, Tilt advertises its local preview listener port; when
explicitly empty, app URLs use standard HTTPS on port 443.

If you need to run `make dev-preview-gateway-up` manually while this checkout's
Tilt stack is running, note that the standalone target's fallback domain is
`apps.127.0.0.1.sslip.io`, not the checkout-specific domain. Prefer rerunning
the Tilt-managed resource, which has the resolved domain and cluster settings:

```bash
tilt trigger preview-gateway-up --port <TILT_PORT>
```

Get `TILT_PORT`, `PREVIEW_GATEWAY_PORT`, and the checkout key from
`make tilt-status`. If you use the manual target, pass its matching target
context, listener port, wildcard hostname, and client-facing service port
explicitly:

```bash
make dev-preview-gateway-up \
  PREVIEW_GATEWAY_CONTEXT=kind-railgrid-kro-<checkout-key> \
  PREVIEW_GATEWAY_PORT=<PREVIEW_GATEWAY_PORT> \
  PREVIEW_GATEWAY_HOSTNAME='*.<base-domain>' \
  PREVIEW_GATEWAY_SERVICE_PORT=<effective-public-port>
```

Use `apps-<checkout-key>.127.0.0.1.sslip.io` as `<base-domain>` when the
resolved domain is the checkout default. The effective public port is 443 when
`RAILGRID_APP_PUBLIC_PORT` is empty, its configured value when set, or the
local listener port when unset. The target's kubeconfig defaults to
`.railgrid-kro.kubeconfig`; override `PREVIEW_GATEWAY_KUBECONFIG` too if your
running checkout uses a different file.

### Providers from other repositories

If you develop an external provider, such as the private providers in
`../providers`, add it to the `make tilt-cluster` session with
`make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers`. See
[External providers with Tilt](docs/external-providers-tilt.md) for the
settings and for the contract a provider repository implements.

---

**API group:** `railgrid.ai/v1alpha1`  
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
    secretRef: ""        # name of Secret in railgrid-system holding the SSH password/key
  conditions:
  - type: Registered     # True once the agent has completed its first join
    status: "False"      # False = AwaitingAgent, True = registered
    reason: ""
    message: ""
```

### Example manifests

**Kubernetes-type edge:**

```yaml
apiVersion: railgrid.ai/v1alpha1
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
apiVersion: railgrid.ai/v1alpha1
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
railgrid edge create <name>
        │
        ▼
TokenReconciler (pkg/hub/controllers/edge/token_reconciler.go)
  - generates 44-char crypto/rand base64url token
  - writes to edge.status.joinToken
  - sets Registered=False condition
        │
        ▼
railgrid edge join-command <name>
  → prints: railgrid agent run --token <joinToken> --edge-name <name> ...
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
  - extractSSHCredsFromHeaders reads X-Railgrid-SSH-* headers
  - markEdgeConnected(edge, sshCreds):
      • builds agent kubeconfig (SA token from edge-<name>-kubeconfig secret)
      • sends kubeconfig back to agent in X-Railgrid-Agent-Kubeconfig response header
      • storeSSHCredentials → creates Secret, writes status.sshCredentials
      • clears joinToken, sets Registered=True, sets phase=Ready
        │
        ▼
Agent receives kubeconfig (cmd/railgrid-agent/main.go)
  - saves to ~/.railgrid/agent-<name>.kubeconfig
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
https://<hub-external-url>/clusters/<workspace-id>/apis/railgrid.ai/v1alpha1/edges/<name>/proxy/k8s
```

This URL is a virtual workspace endpoint served by the hub's agent-proxy virtual workspace handler. The hub:

1. Validates the caller's bearer token against kcp.
2. Looks up the Edge resource to verify it is Ready and the tunnel is active.
3. Forwards the raw TCP stream to the agent over the revdial tunnel.
4. The agent forwards to `localhost:<kubeAPIPort>` on the target cluster.

`railgrid edge kubeconfig <name>` generates a kubeconfig pointing to this URL with the user's hub bearer token embedded.

---

## SSH Server-Mode Internals

### Connection path

```
railgrid ssh <name>
    │
    │  WebSocket upgrade → hub /clusters/<ws>/…/edges/<name>/proxy/ssh
    ▼
hub agent-proxy handler (pkg/virtual/builder/agent_proxy_builder_v2.go)
    │
    │  dials agent over revdial tunnel
    ▼
railgrid-agent (server mode, pkg/agent/agent.go)
    │
    │  forwards raw TCP to localhost:<ssh-proxy-port> (default 22)
    ▼
sshd on the target host
```

### SSH credentials in join-token mode

When the agent starts with `--token` and `--ssh-user`/`--ssh-password`:

1. Agent builds `X-Railgrid-SSH-User` and `X-Railgrid-SSH-Password` headers.
2. These are passed to `StartProxyTunnel` as `extraHeaders`.
3. Hub's `extractSSHCredsFromHeaders` reads them on the first connection.
4. `storeSSHCredentials` creates a `railgrid-ssh-<name>` Secret in `railgrid-system`.
5. `edge.status.sshCredentials` is populated with the username and secret ref.

### SSH host key verification

The provider verifies the sshd host key on every SSH session and fails closed
(`providers/edges/internal/tunnel/agent_proxy_builder.go`, `newSSHClient`):

- **Key source.** `spec.sshHostKey` (operator pin, authorized_keys format) wins.
  Otherwise `status.sshHostKey`, which the agent reports once on tunnel connect
  (`X-Railgrid-SSH-HostKey`). The provider records the reported key **write-once**:
  a later report with a different fingerprint is not applied and instead sets
  the `SSHHostKeyChanged` condition (both fingerprints in the message). Resolve
  it by pinning `spec.sshHostKey` or clearing `status.sshHostKey`.
- **Unparseable key** → the session is refused.
- **No key known** → `spec.sshHostKeyPolicy` decides:
  - `strict` (default): the session is refused until a key is pinned or
    reported.
  - `tofu`: the key presented on the first session is trusted, recorded in
    `status.sshHostKey`, and enforced from then on.
- **Legacy escape hatch.** `edges-provider serve --allow-unverified-ssh-host-key`
  (env `RAILGRID_EDGES_ALLOW_UNVERIFIED_SSH_HOST_KEY=true`, chart value
  `allowUnverifiedSSHHostKey`) opens sessions to edges with no known key
  without verification. It is logged at verbosity 0 on startup and on every
  use, and never affects an edge whose key is known. A malformed value for the
  env form (e.g. `treu`) is a startup error rather than a silent `false`.

The handshake itself is bounded: `newSSHClient` arms a deadline on the tunnelled
connection (the caller's context deadline, else 30s) before
`gossh.NewClientConn`, which takes no context, and clears it once the session is
up. Without it a stalled tunnel pins the handler goroutine indefinitely.

Every session emits two structured lines at verbosity 0: `SSH session opened`
(cluster, edge, caller, SSH user, mode, exec command, remote address, host key
fingerprint) and `SSH session ended` (plus duration and error). A session
refused before opening still gets the `ended` line with `opened=false`. When the
caller's TokenReview fails the lines carry `caller=unknown` and `callerError`.

Two properties keep those lines trustworthy:

- **`remoteAddr` is the LAST `X-Forwarded-For` hop**, not the first. Exactly one
  trusted proxy (the hub backend) fronts this handler and *appends* the peer it
  saw; every entry to its left is caller-supplied and forgeable. With no header
  the peer address is used, port stripped. The value is always a parsed IP or
  `unknown` — junk is never echoed.
- **`exec` is escaped and bounded.** The command is a raw query parameter;
  control characters (newlines above all, which would otherwise forge extra
  audit records) are escaped rather than dropped, and the value is truncated at
  256 runes with an explicit `...[truncated]` marker.

Fingerprints in conditions and audit lines distinguish `unknown` (no key
recorded) from `unparseable` (a key is recorded but malformed).

### SSH username mapping (OIDC)

With OIDC auth, the SSH username is derived from the user's token claim:
- Default: email local-part (`alice@example.com` → `alice`)
- Configurable via the OIDC username claim in hub config

With static token auth, username defaults to `root`.

### Keepalive

The SSH test suite holds connections open for `--ssh-keepalive-duration` (default 60s). The hub sends keepalive pings every 30s over the WebSocket; agents respond to prevent idle-timeout disconnection.

---

## kcp Workspace Hierarchy

railgrid uses kcp for multi-tenant API isolation. Each user/team gets a dedicated kcp workspace:

```
root workspace
└── user-<id> workspace
    ├── Edge resources
    ├── VirtualWorkload resources
    └── Placement resources
```

The hub deploys kcp's `APIBinding` resources to make the railgrid CRDs available in each workspace.

### Static token scoping

Static dev tokens (e.g. `dev-token`) are scoped to a specific kcp workspace path. The workspace path appears in the kubeconfig server URL:

```
https://console.127.0.0.1.sslip.io:9443/clusters/<workspace-id>/...
```

`ClusterNameFromKubeconfig` (in `test/e2e/framework/cluster.go`) extracts the workspace ID from the server URL.

---

## MCP Integration

railgrid exposes all connected Kubernetes clusters as a single [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server. AI agents (Claude, Cursor, Copilot, etc.) connect to this endpoint and can interact with all registered clusters using natural language.

### KubernetesMCP CRD (railgrid.ai/v1alpha1)

**API group:** `railgrid.ai/v1alpha1`  
**Kind:** `KubernetesMCP`

The `KubernetesMCP` object is automatically created as `default` in every tenant workspace by the hub bootstrapper. It acts as the configuration object for the multi-cluster MCP endpoint.

```yaml
apiVersion: railgrid.ai/v1alpha1
kind: KubernetesMCP
metadata:
  name: default
spec:
  edgeSelector: {}   # empty = all kubernetes-type edges; use label selectors to restrict
  readOnly: false    # set true to disable write operations (create/delete/apply)
status:
  URL: "https://hub.example.com/services/mcp/root:railgrid:user-<id>/apis/railgrid.ai/v1alpha1/kubernetesmcps/default/mcp"
  connectedEdges:
  - my-cluster
  - home-lab
```

### MCP URL structure

```
https://<hub>/services/mcp/<workspace-cluster-id>/apis/railgrid.ai/v1alpha1/kubernetesmcps/<name>/mcp
```

- `<workspace-cluster-id>` — kcp logical cluster name for the tenant workspace (from the server URL in the user's kubeconfig)
- `<name>` — name of the `KubernetesMCP` object (usually `default`)

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
    │  3. Fetch KubernetesMCP object for edgeSelector
    │  4. List all edges in the workspace
    │  5. Filter: kubernetes-type only + connected (tunnel active) + label selector
    │  6. Build MultiEdgeRailgridEdgeProvider (one per request)
    │
    ▼
kubernetes-mcp-server (github.com/containers/kubernetes-mcp-server)
    │  MCP Streamable-HTTP protocol (tools/list, tools/call)
    │
    ▼
RailgridEdgeProvider / MultiEdgeRailgridEdgeProvider (pkg/virtual/builder/mcp_provider.go)
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
railgrid mcp url --name default
# prints URL + ready-to-use claude mcp add command with your token
```

Or manually:

```bash
claude mcp add --transport http railgrid \
  "https://hub.example.com/services/mcp/<cluster-id>/apis/railgrid.ai/v1alpha1/kubernetesmcps/default/mcp" \
  -H "Authorization: Bearer <token-from-kubeconfig>"
```

### Per-edge MCP (single cluster)

Each edge also exposes a direct MCP endpoint (independent of the multi-edge `Kubernetes` resource):

```
https://<hub>/services/agent-proxy/<workspace-cluster-id>/apis/railgrid.ai/v1alpha1/edges/<name>/mcp
```

```bash
railgrid mcp url --edge my-cluster
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
