# Contributing to kedge

Thanks for your interest in contributing! This document covers building from source, running tests, understanding the architecture, and the PR workflow.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Building from Source](#building-from-source)
- [Local Development Stack](#local-development-stack)
- [Running Tests](#running-tests)
- [Architecture Overview](#architecture-overview)
- [PR Workflow](#pr-workflow)

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.25+ | `go env GOVERSION` |
| Docker | any recent | must be running |
| kind | latest | for local clusters |
| kubectl | 1.28+ | |
| Helm | 3.x | for chart testing |

---

## Building from Source

```bash
git clone https://github.com/faroshq/kedge.git
cd kedge

# Build all binaries into bin/
make build

# Build just the CLI
make build-kedge

# Build just the hub
make build-hub

# Build the GraphQL gateway
make build-graphql
```

Binaries produced:

| Binary | Description |
|--------|-------------|
| `bin/kedge` | User CLI (also runs as the agent via `kedge agent run`) |
| `bin/kedge-hub` | Hub server |
| `bin/kedge-graphql` | GraphQL gateway (listener + gateway subcommands) |

---

## Local Development Stack

`kedge dev create` spins up a complete local environment with two kind clusters (hub + one agent), deploys the hub via Helm, and wires everything together.

```bash
# Build first
make build

# Create hub + agent kind clusters and deploy the hub
./bin/kedge dev create --chart-path deploy/charts/kedge-hub

# Log in with the static dev token
./bin/kedge login --hub-url https://kedge.localhost:9443 \
  --insecure-skip-tls-verify --token dev-token

# Register a dev edge
./bin/kedge edge create dev-edge-1

# Print the agent command
./bin/kedge edge join-command dev-edge-1

# Run the agent against a kind cluster (writes .kubeconfig-kedge-agent)
hack/scripts/ensure-kind-cluster.sh kedge-agent
./bin/kedge agent run \
  --hub-url https://kedge.localhost:9443 \
  --hub-insecure-skip-tls-verify \
  --token <join-token> \
  --edge-name dev-edge-1 \
  --type kubernetes \
  --kubeconfig .kubeconfig-kedge-agent

# Tear down
./bin/kedge dev delete
```

### Make shortcuts

```bash
make run-hub-embedded-static # run hub with embedded kcp and static token (no helm)
make dev-login-static        # log in with static token
make dev-edge-create         # create dev-edge-1 (kubernetes type)
make dev-run-edge            # run agent for dev-edge-1
make dev-edge-create TYPE=server DEV_EDGE_NAME=my-server
make dev-run-edge TYPE=server DEV_EDGE_NAME=my-server
```

### Lint

```bash
make lint        # golangci-lint (must be 0 issues before pushing)
go build ./...   # must compile clean
```

---

## Running Tests

### Unit tests

```bash
make test          # all packages except /test/e2e
# or target a subtree directly:
go test ./pkg/...
```

### e2e tests

e2e tests spin up real kind clusters and require Docker.

```bash
# Standalone suite (embedded kcp, static token) — also the default `make e2e`
make e2e-standalone

# SSH suite
make e2e-ssh

# OIDC suite (Dex)
make e2e-oidc

# External KCP suite
make e2e-external-kcp

# All suites
make e2e-all
```

**Reuse existing clusters** (faster iteration):

```bash
KEDGE_USE_EXISTING_CLUSTERS=true make e2e-standalone
```

**Keep clusters after failure** (for debugging):

```bash
make e2e-standalone E2E_FLAGS=-keep-clusters
# or the shortcut:
make e2e-keep
```

---

## Architecture Overview

### Components

```
                ┌──────────────────────────────────┐
                │           kedge hub               │
                │                                   │
                │  ┌─────────┐  ┌────────────────┐ │
                │  │  kcp    │  │  agent-proxy   │ │
                │  │  (API)  │  │  virtual WS    │ │
                │  └────┬────┘  └───────┬────────┘ │
                │       │               │           │
                │  ┌────▼───────────────▼────────┐  │
                │  │      hub controllers         │  │
                │  │  token / edge / rbac / ssh  │  │
                │  └─────────────────────────────┘  │
                └──────────────┬───────────────────┘
                               │  revdial reverse tunnel
                    ┌──────────┴──────────┐
                    │                     │
             ┌──────▼──────┐     ┌────────▼──────┐
             │ kedge-agent │     │  kedge-agent  │
             │ (kubernetes)│     │   (server)    │
             └─────────────┘     └───────────────┘
```

### Key packages

| Package | Description |
|---------|-------------|
| `pkg/hub/controllers/edge/` | Edge lifecycle: token reconciler, RBAC, SSH credentials |
| `pkg/hub/controllers/mcp/` | Kubernetes MCP controller — sets status URL, tracks connected edges |
| `pkg/virtual/builder/` | Agent-proxy + MCP virtual workspaces — handles tunnel, status, MCP handler |
| `pkg/agent/` | Agent core: registration, tunnel, edge_reporter |
| `pkg/agent/tunnel/` | revdial tunnel client (`StartProxyTunnel`) |
| `pkg/cli/cmd/` | CLI command implementations (including `kedge mcp url`) |
| `apis/kedge/v1alpha1/` | Edge CRD types |
| `apis/mcp/v1alpha1/` | Kubernetes CRD type (`mcp.kedge.faros.sh`) |

### Join token bootstrap flow

1. `kedge edge create <name>` creates an `Edge` resource.
2. `TokenReconciler` generates a 44-char base64url token → `edge.status.joinToken`.
3. Agent starts with `--token <join-token>` (via `kedge agent run`).
4. Hub validates the token in `authorizeByJoinToken`, calls `markEdgeConnected`.
5. Hub sends the agent's kubeconfig back via `X-Kedge-Agent-Kubeconfig` response header.
6. Agent saves the kubeconfig to `~/.kedge/agent-<name>.kubeconfig`; clears `--token`.
7. On restart, agent loads the saved kubeconfig automatically — no token needed.
8. Hub sets `Registered=True` on the Edge and clears `status.joinToken`.

### revdial tunnel

Agents establish a long-lived WebSocket connection to the hub's `/proxy` endpoint. The hub uses [revdial](https://github.com/bradfitz/revdial) to dial *back* to agents over this connection — the agent never needs an open port.

### Edge proxy URL format

Once an Edge is `Ready`, the hub exposes a virtual workspace endpoint:

```
https://<hub>/clusters/<workspace-id>/apis/kedge.faros.sh/v1alpha1/edges/<name>/proxy/k8s
```

`kedge kubeconfig edge <name>` generates a kubeconfig that points to this URL.

---

## PR Workflow

1. Fork the repo and create a feature branch from `main`.
2. Make your changes. All commits must pass:
   ```bash
   go build ./...   # must compile
   make lint        # 0 issues
   make test        # unit tests pass
   ```
3. Open a PR against `main`. CI runs build, lint, unit tests, and all four e2e suites.
4. Address review comments. The bot (`@mjudeikis-bot`) monitors CI and posts status.
5. A maintainer merges once CI is green and the PR is approved.

### Commit style

```
<type>: <short description> (#issue)

Longer explanation if needed.

Co-authored-by: Your Name <you@example.com>
```

Types: `feat`, `fix`, `test`, `docs`, `refactor`, `chore`.
