# Kedge

The ultimate home lab tool for managing distributed Kubernetes clusters.

## Why Kedge?

Managing multiple Kubernetes clusters across your home lab, remote locations, or edge sites is painful. You end up juggling kubeconfigs, SSH tunnels, VPNs, and port forwards. Kedge solves this by providing a single control plane that connects all your clusters through secure reverse tunnels.

**Perfect for:**

- **Home labs** — Manage k3s/k0s clusters on Raspberry Pis, NUCs, or old laptops from anywhere
- **Remote sites** — Connect clusters behind NAT, firewalls, or without public IPs
- **Edge deployments** — Deploy workloads to distributed locations with simple placement rules
- **Small teams** — Multi-tenant workspaces with OIDC authentication

## How It Works

![Kedge Architecture](docs/assets/diagrams/architecture.svg)

Built on [kcp](https://github.com/kcp-dev/kcp) for multi-tenant workspace isolation and [Dex](https://github.com/dexidp/dex) for OIDC authentication.

## Components

| Component | Description |
|---|---|
| **Hub** (`kedge-hub`) | Central control plane — hosts the API, authentication, tunnel endpoints, and scheduling |
| **Agent** (`kedge-agent`) | Runs on each site — establishes tunnels, reports status, reconciles workloads |
| **CLI** (`kedge`) | User tool — login, register sites, deploy workloads |

## Key Resources

| Resource | Scope | Description |
|---|---|---|
| `Edge` (`type: kubernetes`) | Cluster | A connected Kubernetes cluster |
| `Edge` (`type: server`) | Cluster | A non-Kubernetes host (bare metal, VM) reachable via SSH through the hub |
| `VirtualWorkload` | Namespace | Workload definition with placement rules |
| `Placement` | Namespace | Binding of a workload to a specific site |

## Quick Start

```bash
# Clone and build
git clone https://github.com/faroshq/kedge.git
cd kedge
make build

# Terminal 1: Run the hub (embedded kcp + static token auth)
make run-hub-embedded-static

# Terminal 2: Login and register an edge
make dev-login-static    # Authenticate with static token
make dev-edge-create     # Register an edge (default: kubernetes type)
make dev-run-edge        # Start the agent on a local kind cluster
```

That's it! The hub runs with embedded kcp and static token authentication — no external dependencies required.

### Deploy a test workload

```bash
make dev-create-workload  # Deploy a sample nginx workload
```

### Other development modes

Run `make help-dev` to see all available modes:

| Mode | Command | Description |
|------|---------|-------------|
| **Standalone** | `make run-hub-embedded-static` | Embedded kcp + static token (no deps) |
| **With OIDC** | `make run-dex` + `make run-hub-embedded` | Embedded kcp + Dex OIDC |
| **External kcp** | `make dev-run-kcp` + `make run-hub-static` | External kcp + static token |

## SSH Server Mode

Kedge can manage **non-Kubernetes hosts** — bare metal machines, VMs, or any systemd-managed host — alongside your clusters. The agent runs in server mode, establishes a reverse tunnel to the hub, and proxies SSH connections through it. No open firewall ports or VPN required.

**Use cases:** home-lab machines, Raspberry Pis, cloud VMs, or any host where running a full k8s agent would be overkill.

### Prerequisites

- A running kedge hub (static token or OIDC)
- `sshd` running on the target host (port 22 by default)
- A bootstrap token or OIDC login for the agent

### Setup

**1. Register the edge** (the agent does this automatically on first start, or create the CRD manually):

```yaml
apiVersion: kedge.faros.sh/v1alpha1
kind: Edge
metadata:
  name: my-server
spec:
  type: server
  displayName: my-server
  hostname: my-server.example.com
  provider: bare-metal   # aws | gcp | onprem | bare-metal
  region: home-lab
```

**2. Run the agent in server mode** on the target host:

```bash
kedge agent join \
  --hub-url https://hub.example.com \
  --token <bootstrap-token> \
  --site-name my-server \
  --type=server
```

The `--type=server` flag skips the downstream Kubernetes config — only the SSH tunnel is started.

### Developer Quick-Start

Use the convenience `make` targets to try SSH server mode against a local dev hub (requires `make dev-login-static` first):

```bash
# Terminal 1 — register a dev Edge (server type) resource and write .env.edge
make dev-edge-create TYPE=server

# Terminal 2 — start the agent in server mode (SSH reverse tunnel to localhost:22)
make dev-run-edge TYPE=server
```

The edge name defaults to `dev-edge-1`; override with `DEV_EDGE_NAME=my-host make dev-edge-create TYPE=server`.

### Usage

```bash
# Interactive shell
kedge ssh my-server

# Run a single command (non-interactive)
kedge ssh my-server -- df -h

# Pass multiple arguments
kedge ssh my-server -- journalctl -u kubelet --no-pager -n 50
```

With OIDC authentication the SSH username is derived from the token automatically (email local-part, e.g. `alice@example.com` → `alice`). With static token auth the username defaults to `root`.

### How It Works

```
kedge ssh  ──WebSocket──▶  Hub (agent-proxy virtual workspace)
                                │
                          revdial tunnel  ◀── kedge agent (server mode)
                                │
                          localhost:22 (sshd on target host)
```

1. `kedge ssh` upgrades to a WebSocket connection against the hub's agent-proxy endpoint.
2. The hub dials the agent over the established revdial reverse tunnel.
3. The agent forwards the raw TCP stream to `localhost:22` on the host.
4. The hub acts as the SSH client — authentication, PTY setup, and I/O multiplexing happen transparently.

The agent never needs an inbound firewall rule; all connectivity is outbound from the agent to the hub.

## Requirements

- Go 1.25+
- Docker
- kind (for local agent cluster)

## Documentation

Full documentation is available at the [docs site](https://faroshq.github.io/kedge/).

- [Getting Started](https://faroshq.github.io/kedge/getting-started.html)
- [Developer Guide](https://faroshq.github.io/kedge/developers.html)
- [Security (tokens & OIDC)](https://faroshq.github.io/kedge/security.html)
- [Ingress Setup](https://faroshq.github.io/kedge/ingress/)
- [Helm Deployment](https://faroshq.github.io/kedge/helm.html)
