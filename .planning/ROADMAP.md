# ROADMAP.md — Edge Refactor (Issue #72)

## Summary

**5 phases** | **26 requirements** | All v1 requirements covered ✓

| # | Phase | Goal | Requirements | Success Criteria |
|---|-------|------|--------------|------------------|
| 1 | API Foundation | New Edge CRD + client; old CRDs deleted | EDGE-01..05, CLI-01..02 | `go build ./apis/...` passes; Edge type registered; Site/Server gone |
| 2 | Hub Controllers | Edge controllers replace site controllers | CTRL-01..06 | Hub starts; lifecycle/mount/rbac reconcile Edge resources |
| 3 | Virtual Workspaces | `edges-proxy` replaces edge-proxy+agent-proxy | VW-01..08 | Agent registers with `?edge=`; /k8s and /ssh subresources work |
| 4 | Agent + CLI | Agent uses Edge; `--type` flag; unified reporter | AGENT-01..07, CLI2-01..03 | Agent connects as Edge; `kedge ssh` works without probing |
| 5 | e2e + Cleanup | All tests green; lint passes | E2E-01..05, BUILD-01..03 | `make lint` passes; e2e suites pass |

---

## Phase 1: API Foundation

**Goal:** Define the `Edge` CRD, regenerate deep copies and CRD manifests, register the type in the scheme, and update the hub client — then delete `Site` and `Server` types. This is the foundation everything else depends on.

**Requirements:** EDGE-01, EDGE-02, EDGE-03, EDGE-04, EDGE-05, CLI-01, CLI-02

### Plans

#### Plan 1.1 — Define Edge type

**Files to create/edit:**
- `apis/kedge/v1alpha1/types_edge.go` — new file
- `apis/kedge/v1alpha1/zz_generated.deepcopy.go` — regenerate (run `controller-gen`)
- `apis/kedge/v1alpha1/groupversion_info.go` — register Edge/EdgeList
- `pkg/hub/bootstrap/crds/` — add `kedge.faros.sh_edges.yaml`, remove `sites.yaml` / `servers.yaml`

**Edge type structure:**
```go
// EdgeType distinguishes kubernetes clusters from bare-metal/SSH servers.
type EdgeType string
const (
    EdgeTypeKubernetes EdgeType = "kubernetes"
    EdgeTypeServer     EdgeType = "server"
)

type Edge struct {
    metav1.TypeMeta
    metav1.ObjectMeta
    Spec   EdgeSpec
    Status EdgeStatus
}
type EdgeSpec struct {
    Type        EdgeType         `json:"type"`
    DisplayName string           `json:"displayName,omitempty"`
    Provider    string           `json:"provider,omitempty"`
    Region      string           `json:"region,omitempty"`
    Kubernetes  *EdgeKubernetes  `json:"kubernetes,omitempty"`
    Server      *EdgeServer      `json:"server,omitempty"`
}
type EdgeKubernetes struct{}  // reserved for future fields
type EdgeServer struct {
    SSHPort          int                       `json:"sshPort,omitempty"`
    SSHKeySecretRef  *corev1.SecretReference   `json:"sshKeySecretRef,omitempty"`
}
type EdgeStatus struct {
    Phase             EdgePhase           `json:"phase"`
    TunnelConnected   bool                `json:"tunnelConnected"`
    LastHeartbeatTime *metav1.Time        `json:"lastHeartbeatTime,omitempty"`
    // kubernetes-only
    URL               string              `json:"url,omitempty"`
    KubernetesVersion string              `json:"kubernetesVersion,omitempty"`
    Capacity          corev1.ResourceList `json:"capacity,omitempty"`
    Allocatable       corev1.ResourceList `json:"allocatable,omitempty"`
    // server-only
    SSHEnabled        bool                `json:"sshEnabled,omitempty"`
    Conditions        []metav1.Condition  `json:"conditions,omitempty"`
    CredentialsSecretRef string           `json:"credentialsSecretRef,omitempty"`
}
```

**Kubebuilder markers on Edge:**
```
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.tunnelConnected"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
```

**Checklist:**
- [ ] `types_edge.go` compiles
- [ ] `zz_generated.deepcopy.go` updated (run `make generate` or `controller-gen`)
- [ ] `types_site.go` and `types_server.go` deleted
- [ ] `groupversion_info.go` no longer references Site/Server
- [ ] `go build ./apis/...` succeeds

#### Plan 1.2 — Update hub client

**Files to edit:**
- `pkg/client/dynamic.go` — add `Edges()` method; remove `Sites()`, `Servers()`
- `pkg/client/informers.go` — add Edge informer; remove Site/Server informers

**Guidance:**
- Follow the exact same pattern as existing `Sites()` / `Servers()` — same RBAC verbs, just different GVR
- GVR: `kedge.faros.sh / v1alpha1 / edges`
- Keep `Placements()` unchanged

**Checklist:**
- [ ] `pkg/client/dynamic.go` compiles; `Sites()` / `Servers()` gone
- [ ] No other callers of `Sites()` / `Servers()` remain (fix in later phases)

---

**Phase 1 Success Criteria:**
1. `go build ./apis/...` and `go build ./pkg/client/...` succeed
2. `Edge` type is registered in scheme; `Site` and `Server` are not
3. CRD YAML for `edges` exists; `sites` and `servers` CRD files deleted
4. No deepcopy compile errors

---

## Phase 2: Hub Controllers

**Goal:** Port the three site reconcilers (lifecycle, mount, RBAC) to watch `Edge` instead of `Site`. The mount reconciler must skip workspace creation for `spec.type=server` edges.

**Requirements:** CTRL-01, CTRL-02, CTRL-03, CTRL-04, CTRL-05, CTRL-06

### Plans

#### Plan 2.1 — Edge controller package

**Files to create:**
- `pkg/hub/controllers/edge/controller.go`
- `pkg/hub/controllers/edge/lifecycle_reconciler.go`
- `pkg/hub/controllers/edge/mount_reconciler.go`
- `pkg/hub/controllers/edge/rbac_reconciler.go`

**Key changes vs site package:**
- All `kedgev1alpha1.Site` references → `kedgev1alpha1.Edge`
- All `SitePhase*` constants → `EdgePhase*`
- `SiteStatus.TunnelConnected` → `EdgeStatus.TunnelConnected` (same field name)
- `mount_reconciler.go`: add early-return guard `if edge.Spec.Type != EdgeTypeKubernetes { return ctrl.Result{}, nil }`
- Controller name: `"edge-lifecycle"`, `"edge-mount"`, `"edge-rbac"`

**Files to delete:**
- `pkg/hub/controllers/site/` (entire directory)

#### Plan 2.2 — Wire edge controllers in hub server

**Files to edit:**
- `pkg/hub/server.go` — call `edge.SetupLifecycleWithManager`, `edge.SetupMountWithManager`, `edge.SetupRBACWithManager`; remove site controller setup
- `pkg/hub/scheme.go` — scheme already updated in Phase 1

**Checklist:**
- [ ] Hub server compiles
- [ ] `pkg/hub/controllers/site/` deleted
- [ ] Mount reconciler skips workspace creation for `type=server`
- [ ] `go build ./pkg/hub/...` succeeds

---

**Phase 2 Success Criteria:**
1. Hub binary compiles
2. Hub starts and controllers register without error
3. Edge lifecycle, mount, and RBAC reconcilers are registered
4. Site controller directory is gone

---

## Phase 3: Virtual Workspaces

**Goal:** Replace the two current virtual workspaces (`edge-proxy` for agent registration, `agent-proxy` for resource access) with a single `edges-proxy` workspace that handles both concerns under a clean URL scheme.

**Requirements:** VW-01, VW-02, VW-03, VW-04, VW-05, VW-06, VW-07, VW-08

### Plans

#### Plan 3.1 — Create `edges_proxy_builder.go`

**File:** `pkg/virtual/builder/edges_proxy_builder.go`

This file merges the tunnel registration logic from `edge_proxy_builder.go` and the subresource dispatch logic from `agent_proxy_builder.go`.

**Tunnel registration endpoint** (`/register`):
```go
mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
    token := extractBearerToken(r)
    clusterName := r.URL.Query().Get("cluster")
    edgeName  := r.URL.Query().Get("edge")    // replaces ?site= / ?server=
    // authorize against "edges" resource
    // key := clusterName + "/" + edgeName
    // upgrade WS, store in connman
})
```

**Subresource dispatch** (all other paths):
```go
// Path: /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/k8s|ssh
// OR legacy: /proxy/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh
```

Parse `edges` from path segments; dispatch on final segment:
- `k8s` → existing k8s reverse-proxy logic (from `buildAgentProxyHandler` / `buildSiteProxyHandler`)
- `ssh` → existing SSH WebSocket proxy logic

**Authorization:** check `proxy` on `edges` resource (not `sites`/`servers`)

**Key helper changes:**
- `getKey(cluster, name)` remains `cluster + "/" + name` — no type prefix
- `getServerKey` deleted (no longer needed)

#### Plan 3.2 — Update build.go and proxy.go

**Files:**
- `pkg/virtual/builder/build.go`:
  - `EdgesProxyVirtualWorkspaceName = "edges-proxy"` (new constant)
  - Remove `EdgeProxyVirtualWorkspaceName` and `AgentProxyVirtualWorkspaceName`
  - `BuildVirtualWorkspaces` returns `edges-proxy` + `cluster-proxy` (drop `edge-proxy`, `agent-proxy`)
- `pkg/virtual/builder/proxy.go`:
  - Update `NewVirtualWorkspaces` to expose `EdgesProxyHandler()`; remove `EdgeProxyHandler()` and `AgentProxyHandler()`
- `pkg/virtual/builder/site_proxy_handler.go`:
  - Update connman key lookup to use `edges` path segments

**Files to delete:**
- `pkg/virtual/builder/edge_proxy_builder.go`
- `pkg/virtual/builder/agent_proxy_builder.go`

**Checklist:**
- [ ] `go build ./pkg/virtual/...` succeeds
- [ ] Old builder files deleted
- [ ] `edges-proxy` workspace registered in hub server with correct path prefix
- [ ] `/register?edge=<name>` endpoint reachable

---

**Phase 3 Success Criteria:**
1. `pkg/virtual/builder` compiles
2. `edge-proxy` and `agent-proxy` constants/builders gone
3. `edges-proxy` workspace handles both tunnel registration and subresource dispatch
4. Authorization checks reference `edges` GVR

---

## Phase 4: Agent + CLI

**Goal:** Update the agent to use the `Edge` resource (not `Site`/`Server`), change the flag name from `--mode` to `--type`, write a unified `edge_reporter.go`, and simplify the CLI `kedge ssh` command.

**Requirements:** AGENT-01..07, CLI2-01..03

### Plans

#### Plan 4.1 — Refactor agent core

**Files to edit:**
- `pkg/agent/agent.go`:
  - `AgentMode` → `AgentType`; `AgentModeSite` → `AgentTypeKubernetes`; `AgentModeServer` → `AgentTypeServer`
  - `opts.Mode` → `opts.Type`
  - Tunnel registration URL: `?edge=<name>` (not `?site=` / `?server=`)
  - Agent creates `Edge` resource on hub (using `hubClient.Edges()`)
- `pkg/cli/cmd/agent.go`:
  - `--mode` flag → `--type` flag
  - Update help text

#### Plan 4.2 — Unified edge reporter

**File to create:** `pkg/agent/status/edge_reporter.go`

Merges `Reporter` (site) and `ServerReporter` (server):
```go
type EdgeReporter struct {
    edgeName        string
    edgeType        kedgev1alpha1.EdgeType
    hubClient       *kedgeclient.Client
    downstreamClient kubernetes.Interface  // nil for type=server
    tunnelState     <-chan bool            // nil for type=kubernetes
}
```
- `type=kubernetes`: sends heartbeat patching `Edge` status; also calls `reportWorkloadStatus` (same logic as current `Reporter`)
- `type=server`: tracks tunnel state from channel; sends heartbeat patching `Edge` status with `sshEnabled`

**Files to delete:**
- `pkg/agent/status/reporter.go`
- `pkg/agent/status/server_reporter.go`

#### Plan 4.3 — Simplify CLI ssh command

**File to edit:** `pkg/cli/cmd/ssh.go`

- Delete `resolveResourceKind` function
- `buildSSHWebSocketURL` directly uses `/proxy/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh`
- No GET probe to hub before connecting

**File to delete / rename:** `pkg/cli/cmd/site.go` → `pkg/cli/cmd/edge.go` (or delete if only Site-specific)

**Checklist:**
- [ ] Agent compiles with `--type` flag
- [ ] No references to `?site=` or `?server=` query params in agent tunnel code
- [ ] `edge_reporter.go` handles both types
- [ ] `kedge ssh` does not call `resolveResourceKind`
- [ ] `go build ./cmd/kedge-agent/...` and `./cmd/kedge/...` succeed

---

**Phase 4 Success Criteria:**
1. `kedge-agent` binary compiles with `--type=kubernetes|server`
2. `kedge` binary compiles; `kedge ssh <name>` uses edges URL directly
3. Agent registers with `?edge=<name>` query param
4. Edge reporter sends heartbeats to `Edge` resource status

---

## Phase 5: e2e Tests + Build Cleanup

**Goal:** Update all e2e test infrastructure and cases to use `Edge` resources, ensure `go build ./...` and `make lint` are green.

**Requirements:** E2E-01, E2E-02, E2E-03, E2E-04, E2E-05, BUILD-01, BUILD-02, BUILD-03

### Plans

#### Plan 5.1 — Update e2e framework

**Files to edit:**
- `test/e2e/framework/agent.go` — pass `--type=kubernetes` or `--type=server` to agent process (not `--mode`)
- `test/e2e/framework/cluster.go` — any Site resource creation → Edge creation
- `test/e2e/framework/client.go` — any `Sites()` / `Servers()` calls → `Edges()`

#### Plan 5.2 — Update e2e test cases

**Files to edit:**
- `test/e2e/cases/site.go` → rename/rewrite as `test/e2e/cases/edge.go` — use `Edge{Spec: {Type: "kubernetes"}}`
- `test/e2e/cases/multisite.go` → update to create/list `Edge` resources
- `test/e2e/cases/ssh.go` → use `Edge{Spec: {Type: "server"}}`; verify `/edges/{name}/ssh` subresource

#### Plan 5.3 — Final build verification

```bash
go generate ./...          # regenerate deep copy if not already done
go build ./...             # must succeed
go test ./...              # unit tests must pass
make lint                  # golangci-lint must pass
```

Fix any remaining stray references to `Site`, `Server`, `--mode`, `?site=`, `?server=` in non-deleted files.

---

**Phase 5 Success Criteria:**
1. `go build ./...` exits 0
2. `go test ./...` exits 0 (all unit tests pass)
3. `make lint` exits 0
4. e2e tests compile under all suites (standalone, external_kcp, ssh, oidc)
5. No references to `Site`, `Server` types remain outside of git history

---

## Dependency Graph

```
Phase 1 (API + Client)
    ↓
Phase 2 (Controllers)   Phase 3 (Virtual Workspaces)
    ↓                           ↓
Phase 4 (Agent + CLI) ←────────┘
    ↓
Phase 5 (e2e + Cleanup)
```

Phases 2 and 3 can be worked in parallel after Phase 1 completes.

---
*Last updated: 2026-02-25 — initial roadmap*
