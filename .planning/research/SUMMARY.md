# SUMMARY.md — Research Summary (Edge Refactor)

## Project at a Glance

kedge is a Kubernetes-native edge connectivity platform built on kcp. It lets you connect remote clusters and SSH hosts to a central hub, then access them through a unified API. The `ssh` branch being refactored adds bare-metal/SSH host support alongside the existing Kubernetes cluster support.

## Key Findings

### Stack

| Component | Library/Pattern |
|-----------|----------------|
| API types | `k8s.io/apimachinery` + kubebuilder markers + `controller-gen` |
| Hub controllers | `sigs.k8s.io/controller-runtime` + `multicluster-runtime` (mcbuilder/mcmanager) |
| kcp workspace management | `github.com/kcp-dev/sdk` tenancy API |
| Connection pooling | custom `connman.ConnectionManager` (sync.Map under the hood) |
| Reverse tunnel | custom `revdial` (WebSocket-based, à la Go playground) |
| SSH proxying | `golang.org/x/crypto/ssh` + WebSocket bridge |
| Build generation | `make generate` → `controller-gen` for deepcopy + CRD YAML |
| Linting | `golangci-lint` via `make lint` |

### What the codebase does today (site branch state)

- `Site` CRD: cluster-scoped, non-namespaced; stores k8s cluster connectivity + kube API proxy URL
- `Server` CRD: cluster-scoped, non-namespaced; stores SSH host connectivity
- `edge-proxy` VW: WebSocket endpoint where agents register reverse tunnels; supports `?site=` and `?server=` query params with different connman key formats
- `agent-proxy` VW: HTTP endpoint serving `k8s` / `ssh` / `exec` / `logs` subresources for both resource kinds
- `cluster-proxy` VW: routes requests to specific kcp workspaces
- `site/` controllers: lifecycle (heartbeat timeout), mount (kcp workspace creation, k8s only), RBAC
- Status reporters: `Reporter` (site, k8s) and `ServerReporter` (server, SSH) — parallel implementations
- CLI `kedge ssh`: probes hub to determine resource kind before connecting

### What changes in the refactor

| Before | After |
|--------|-------|
| `Site` + `Server` CRDs | Single `Edge` CRD with `spec.type` |
| 2 connman key formats | 1 key format: `{cluster}/{name}` |
| 3 virtual workspaces | 2 virtual workspaces (`edges-proxy` + `cluster-proxy`) |
| `edge-proxy` + `agent-proxy` builders | `edges_proxy_builder.go` |
| `site/` controller package | `edge/` controller package |
| 2 status reporters | 1 `EdgeReporter` |
| `--mode=site|server` agent flag | `--type=kubernetes|server` flag |
| CLI probes resource kind | CLI uses `edges` path directly |

### Watch Out For

1. **deep copy regeneration** — must run `make generate` after type changes (see PITFALLS.md)
2. **stray string references** to `"sites"` / `"servers"` in URL paths, SAR checks, test fixtures
3. **mount reconciler guard** for `type=server` edges (skip workspace creation)
4. **embedded CRD FS** — old `sites.yaml` / `servers.yaml` must be physically deleted from `pkg/hub/bootstrap/crds/`
5. **`--mode` flag deprecation** — add alias or deprecation warning for backward compatibility

## Confidence Levels

| Area | Confidence |
|------|-----------|
| Phase ordering (API → Controllers → VW → Agent → e2e) | High |
| Edge type structure | High — mirrors current Site/Server cleanly |
| connman key simplification | High — names are unique per cluster |
| Virtual workspace merge | Medium — need to verify all path-parsing edge cases |
| kcp mount workspace guard | High — trivial early-return |
| e2e framework changes | Medium — depends on test helper structure |
