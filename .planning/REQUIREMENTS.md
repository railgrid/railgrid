# REQUIREMENTS.md — Edge Refactor (Issue #72)

## v1 Requirements

### API (EDGE-CRD)

- [ ] **EDGE-01**: `Edge` CRD defined in `apis/kedge/v1alpha1/types_edge.go` with `spec.type` (kubernetes|server), `spec.kubernetes`, `spec.server.sshPort`, `spec.server.sshKeySecretRef`, and full status fields
- [ ] **EDGE-02**: Deep copy generated (`zz_generated.deepcopy.go` updated)
- [ ] **EDGE-03**: CRD YAML manifest generated and embedded in hub bootstrap (`pkg/hub/bootstrap/crds/`)
- [ ] **EDGE-04**: `Site` and `Server` types deleted from `apis/kedge/v1alpha1/`
- [ ] **EDGE-05**: `groupversion_info.go` / scheme registration updated to include `Edge`/`EdgeList`, remove `Site`/`Server`

### Hub Client (EDGE-CLI)

- [ ] **CLI-01**: `pkg/client/` updated — `Edges()` client method replacing `Sites()` / `Servers()`
- [ ] **CLI-02**: `pkg/client/informers.go` updated for `Edge` informer

### Hub Controllers (EDGE-CTRL)

- [ ] **CTRL-01**: `pkg/hub/controllers/edge/controller.go` — constants (HeartbeatTimeout=90s, GCTimeout=24h), controller wiring
- [ ] **CTRL-02**: `pkg/hub/controllers/edge/lifecycle_reconciler.go` — heartbeat staleness check → mark Disconnected
- [ ] **CTRL-03**: `pkg/hub/controllers/edge/mount_reconciler.go` — create kcp workspace for `type=kubernetes` edges only; skip for `type=server`
- [ ] **CTRL-04**: `pkg/hub/controllers/edge/rbac_reconciler.go` — workspace RBAC setup
- [ ] **CTRL-05**: `pkg/hub/controllers/site/` directory deleted
- [ ] **CTRL-06**: Hub server (`pkg/hub/server.go`) wires edge controllers, drops site controllers

### Virtual Workspaces (EDGE-VW)

- [ ] **VW-01**: `edges-proxy` virtual workspace created — single workspace replacing `edge-proxy` + `agent-proxy`
- [ ] **VW-02**: Tunnel registration handler: agents connect via WebSocket with `?edge=<name>&cluster=<cluster>`; connman key is `{cluster}/{name}`
- [ ] **VW-03**: Authorization checks against `edges` resource (not `sites`/`servers`)
- [ ] **VW-04**: `/k8s` subresource proxies Kubernetes API through reverse tunnel (type=kubernetes only)
- [ ] **VW-05**: `/ssh` subresource proxies SSH WebSocket through reverse tunnel (type=server; also available for type=kubernetes)
- [ ] **VW-06**: Old `edge-proxy`, `agent-proxy` virtual workspace builders deleted; `cluster-proxy` retained
- [ ] **VW-07**: `build.go` updated — exports only `edges-proxy` and `cluster-proxy` named workspaces
- [ ] **VW-08**: `site_proxy_handler.go` updated to look up connman by `edges` path rather than `sites` path

### Agent (EDGE-AGENT)

- [ ] **AGENT-01**: `--type=kubernetes|server` flag added to `kedge agent join` (replaces `--mode=site|server`)
- [ ] **AGENT-02**: `AgentType` replaces `AgentMode` in `pkg/agent/agent.go`
- [ ] **AGENT-03**: Agent registers tunnel with `?edge=<name>` query param (not `?site=` / `?server=`)
- [ ] **AGENT-04**: `pkg/agent/status/edge_reporter.go` — unified reporter handles both type=kubernetes (k8s status + workload sync) and type=server (SSH tunnel state only)
- [ ] **AGENT-05**: `pkg/agent/status/reporter.go` (site reporter) deleted
- [ ] **AGENT-06**: `pkg/agent/status/server_reporter.go` deleted
- [ ] **AGENT-07**: Agent creates `Edge` resource on hub (not `Site`/`Server`) when it first connects

### CLI (EDGE-CLI2)

- [ ] **CLI2-01**: `kedge ssh <name>` builds URL `/proxy/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh` directly (no `resolveResourceKind` probe)
- [ ] **CLI2-02**: `resolveResourceKind` function deleted from `pkg/cli/cmd/ssh.go`
- [ ] **CLI2-03**: `pkg/cli/cmd/site.go` deleted or repurposed as `edge.go`

### e2e Tests (EDGE-E2E)

- [ ] **E2E-01**: `test/e2e/cases/site.go` updated to use `Edge` resource with `type=kubernetes`
- [ ] **E2E-02**: `test/e2e/cases/multisite.go` updated for `Edge`
- [ ] **E2E-03**: `test/e2e/cases/ssh.go` updated — SSH test uses `Edge` with `type=server`
- [ ] **E2E-04**: `test/e2e/framework/agent.go` passes `--type` flag (not `--mode`)
- [ ] **E2E-05**: All test suites (standalone, external_kcp, ssh, oidc) compile and pass

### Build & Quality (EDGE-BUILD)

- [ ] **BUILD-01**: `go build ./...` succeeds with no errors
- [ ] **BUILD-02**: `make lint` passes (golangci-lint)
- [ ] **BUILD-03**: All existing unit tests pass (`go test ./...`)

---

## v2 Requirements (deferred)

- Migration / conversion webhook for existing clusters with `Site`/`Server` resources
- `spec.kubernetes` sub-fields (node selectors, resource limits, etc.)
- Edge labels/tagging UI

---

## Out of Scope

- Dashboard / UI — none exists yet
- Multi-type edges — an Edge is always a single type
- Server-side `kubectl` exec / logs (existing behaviour unchanged)
- Auto-creation of `Edge` resource by agent (agent assumes resource pre-exists, as today)

---

## Traceability

| Requirement | Phase |
|-------------|-------|
| EDGE-01..05 | Phase 1 |
| CLI-01..02 | Phase 1 |
| CTRL-01..06 | Phase 2 |
| VW-01..08 | Phase 3 |
| AGENT-01..07 | Phase 4 |
| CLI2-01..03 | Phase 4 |
| E2E-01..05 | Phase 5 |
| BUILD-01..03 | Phase 5 |
