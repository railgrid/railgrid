# PROJECT.md — Edge Refactor (Issue #72)

## Overview

Refactor the two separate resource types (`Site` = Kubernetes cluster, `Server` = SSH host) into a single unified `Edge` resource for faroshq/kedge. This reduces API surface, eliminates duplicate controllers/builders/reporters, and gives both connection paths a consistent registration and access model.

**Issue:** https://github.com/faroshq/kedge/issues/72
**Branch:** `ssh` (current working branch)
**Stack:** Go 1.22+, controller-runtime, kcp virtual workspaces, gorilla/websocket, multicluster-runtime

---

## Problem Statement

Currently kedge maintains two parallel resource types:

| Resource | Purpose | Agent mode | VW path |
|----------|---------|------------|---------|
| `Site` | Kubernetes cluster | `--mode=site` | `/services/agent-proxy/.../sites/{name}/k8s` |
| `Server` | SSH bare-metal host | `--mode=server` | `/services/agent-proxy/.../servers/{name}/ssh` |

This duplication manifests as:
- Two CRDs with nearly identical lifecycle (phase, tunnelConnected, heartbeat)
- Two sets of controllers (`pkg/hub/controllers/site/`) — lifecycle, mount, RBAC reconcilers
- Two virtual workspace builders (`edge_proxy_builder.go`, `agent_proxy_builder.go`) with `?site=` / `?server=` branch logic
- Two status reporters (`status/reporter.go`, `status/server_reporter.go`)
- Branching `if serverName != "" { ... } else { ... }` code throughout
- CLI `kedge ssh` needing to probe which resource kind the name belongs to

---

## Target Architecture

### New `Edge` CRD

```yaml
apiVersion: kedge.faros.sh/v1alpha1
kind: Edge
metadata:
  name: my-cluster
spec:
  type: kubernetes | server
  kubernetes: {}             # k8s-specific config (reserved for future fields)
  server:
    sshPort: 22
    sshKeySecretRef:
      name: my-ssh-key
      namespace: default
status:
  phase: Connected | Disconnected | Ready | NotReady
  tunnelConnected: true
  lastHeartbeatTime: "..."
  # kubernetes-only
  kubernetesVersion: "1.31.0"
  capacity: {...}
  allocatable: {...}
  url: "https://..."
  # server-only
  sshEnabled: true
```

### Single virtual workspace: `edges-proxy`

Replaces both `edge-proxy` (tunnel registration) and `agent-proxy` (resource access):

**Tunnel registration** (agent → hub):
```
POST /services/edges-proxy/register?edge=<name>&cluster=<cluster>
```

**Resource access** (user → hub):
```
/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/k8s   # k8s only
/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh   # server only
```

### Connection pool

Single `connman.ConnectionManager` pool keyed by `{cluster}/{name}` (no type prefix).

### Controller: `pkg/hub/controllers/edge/`

Merges all site sub-reconcilers into a unified set:
- `lifecycle_reconciler.go` — heartbeat timeout → Disconnected
- `mount_reconciler.go` — create kcp workspace for `type=kubernetes` only
- `rbac_reconciler.go` — workspace RBAC
- `controller.go` — constants (HeartbeatTimeout, GCTimeout)

### Agent

Single registration path. Agent flag `--type=kubernetes|server` (replaces `--mode=site|server`).
- `type=kubernetes`: starts downstream k8s client + workload reconciler + site reporter → hub creates workspace
- `type=server`: SSH proxy only, no k8s client — minimal resource footprint
- Both paths use `edge_reporter.go` (replaces `reporter.go` + `server_reporter.go`)

### CLI

`kedge ssh <name>` calls `/ssh` subresource on the `edges` resource — no probe needed.
`kubectl` access via workspace URL as before (workspace created by mount reconciler for `type=kubernetes`).

---

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Single `Edge` CRD with `spec.type` | Avoids split lifecycle; consistent RBAC | Adopted |
| `/k8s` and `/ssh` as URL subresources | Maps cleanly to k8s subresource pattern | Adopted |
| connman key `{cluster}/{name}` (no type prefix) | Names unique per cluster already | Adopted |
| Agent flag `--type` replaces `--mode` | Consistent with CRD field naming | Adopted |
| Mount workspace only for `type=kubernetes` | SSH hosts don't need kube API access | Adopted |
| Delete `agent-proxy` + `edge-proxy`, add `edges-proxy` | Single VW reduces surface area | Adopted |

---

## Requirements

### Validated

*(None — brownfield refactor, existing capabilities are the baseline)*

### Active

- [ ] Edge CRD defined with `spec.type`, `spec.kubernetes`, `spec.server` fields
- [ ] Deep copy, CRD manifest generated for `Edge`
- [ ] `Site` and `Server` CRDs deleted (types + generated files)
- [ ] `pkg/hub/controllers/edge/` implements lifecycle, mount, RBAC reconcilers
- [ ] Mount workspace created only for `type=kubernetes` edges
- [ ] `pkg/hub/controllers/site/` deleted
- [ ] `edges-proxy` virtual workspace with unified tunnel registration (`?edge=<name>`)
- [ ] `edges-proxy` serves `/k8s` and `/ssh` subresources
- [ ] Old `edge-proxy` and `agent-proxy` virtual workspaces deleted
- [ ] `agent_proxy_builder.go` deleted; `edge_proxy_builder.go` replaced by `edges_proxy_builder.go`
- [ ] Agent `--type=kubernetes|server` flag (replaces `--mode`)
- [ ] Agent `edge_reporter.go` replaces `reporter.go` + `server_reporter.go`
- [ ] `kedge ssh <name>` uses `/edges/{name}/ssh` directly (no resource-kind probe)
- [ ] `kedge agent join --type=kubernetes|server` help text updated
- [ ] `pkg/client/` updated: `Sites()` / `Servers()` replaced by `Edges()`
- [ ] e2e tests updated: `site.go`, `multisite.go`, `ssh.go` test cases
- [ ] e2e framework updated: agent framework uses `--type` flag
- [ ] `go build ./...` passes; `make lint` passes
- [ ] All existing e2e scenarios pass under new resource model

### Out of Scope

- Migration tooling / conversion webhook (no production deployments yet)
- `spec.kubernetes` sub-fields (reserved empty struct for now)
- UI / dashboard changes (none exist)
- Multi-type edge (an Edge is exclusively kubernetes OR server)

---
*Last updated: 2026-02-25 after initialization*
