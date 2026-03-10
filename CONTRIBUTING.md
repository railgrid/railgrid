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

# Cross-compile for all platforms
make build-all
```

Binaries produced:

| Binary | Description |
|--------|-------------|
| `bin/kedge` | User CLI |
| `bin/kedge-hub` | Hub server |
| `bin/kedge-agent` | Edge agent (standalone binary) |

---

## Local Development Stack

`kedge dev create` spins up a complete local environment with two kind clusters (hub + one agent), deploys the hub via Helm, and wires everything together.

```bash
# Build first
make build

# Create hub + agent kind clusters and deploy the hub
./bin/kedge dev create --chart-path deploy/charts/kedge-hub

# Log in with the static dev token
./bin/kedge login --hub-url https://kedge.localhost:8443 \
  --insecure-skip-tls-verify --token dev-token

# Register a dev edge
./bin/kedge edge create dev-edge-1

# Print the agent command
./bin/kedge edge join-command dev-edge-1

# Run the agent in the background (foreground process)
./bin/kedge agent run \
  --hub-url https://kedge.localhost:8443 \
  --token <join-token> \
  --edge-name dev-edge-1 \
  --type kubernetes \
  --kubeconfig $(kind get kubeconfig --name kedge-e2e-agent-1 2>/dev/null)

# Tear down
./bin/kedge dev delete
```

### Make shortcuts

```bash
make dev-login-static       # log in with static token
make dev-edge-create        # create dev-edge-1 (kubernetes type)
make dev-run-edge           # run agent for dev-edge-1
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
go test ./pkg/...
```

### e2e tests

e2e tests spin up real kind clusters and require Docker.

```bash
# Standalone suite (embedded kcp, static token)
make test-e2e

# SSH suite
make test-e2e-ssh

# OIDC suite (Dex)
make test-e2e-oidc

# External KCP suite
make test-e2e-external-kcp
```

**Reuse existing clusters** (faster iteration):

```bash
KEDGE_USE_EXISTING_CLUSTERS=true make test-e2e
```

**Keep clusters after failure** (for debugging):

```bash
make test-e2e E2E_FLAGS=--keep-clusters
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
| `pkg/virtual/builder/` | Agent-proxy virtual workspace — handles tunnel + status |
| `pkg/agent/` | Agent core: registration, tunnel, edge_reporter |
| `pkg/agent/tunnel/` | revdial tunnel client (`StartProxyTunnel`) |
| `pkg/cli/cmd/` | CLI command implementations |
| `apis/kedge/v1alpha1/` | Edge CRD types |

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
   go build ./...    # must compile
   make lint         # 0 issues
   go test ./pkg/... # unit tests pass
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
