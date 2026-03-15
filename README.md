# kedge

kedge connects your distributed Kubernetes clusters and servers through a single control plane — no VPNs, no open firewall ports, no kubeconfig juggling. Agents running on each edge establish outbound reverse tunnels to the hub, so clusters behind NAT, home-lab Raspberry Pis, and bare-metal machines in remote sites all become reachable through one authenticated endpoint.

## Features

- **Reverse tunnel connectivity** — agents dial out; no inbound firewall rules needed
- **Kubernetes edge support** — proxy `kubectl` to any registered cluster via the hub
- **SSH server mode** — manage non-Kubernetes hosts (VMs, bare metal) through the same hub
- **MCP integration** — expose all connected clusters as a single [Model Context Protocol](https://modelcontextprotocol.io) server for AI agents (Claude, Cursor, etc.)
- **OIDC authentication** — plug in any OIDC provider (Dex, Auth0, Okta, …)
- **Static token auth** — quick setup for home labs and dev environments
- **Multi-tenant workspaces** — per-user/team kcp workspace isolation
- **CLI-first** — register edges, get kubeconfigs, and SSH into servers with one command

## Architecture

```
   [ your laptop ]
        │  kedge CLI
        ▼
   ┌─────────────┐
   │  kedge hub  │  ◄── central control plane (Kubernetes + kcp + OIDC)
   └──────┬──────┘
          │  reverse tunnels (outbound from agents)
    ┌─────┴──────────────────┐
    │                        │
┌───▼────┐             ┌─────▼──────┐
│ agent  │             │   agent    │
│ (k8s)  │             │  (server)  │
│cluster │             │  bare metal│
└────────┘             └────────────┘
```

The hub is the only component that needs to be publicly reachable. Agents connect outward — NAT and firewalls are not a problem.

## Installation

### Hub

The hub is the only component that needs a public endpoint. Any device you're comfortable exposing works — a VPS, cloud VM, home server, or anything behind a Cloudflare Tunnel or ingress controller.

→ **[Installation](https://faroshq.github.io/kedge/helm.html)**

### CLI

Install the `kedge` CLI on any machine you want to interact with the hub from:

**Binary (recommended)** — download from the [releases page](https://github.com/faroshq/kedge/releases) and put the binary in your `$PATH`.

**krew:**

```bash
kubectl krew index add faros https://github.com/faroshq/krew-index.git
kubectl krew install faros/kedge
```

**From source:**

```bash
go install github.com/faroshq/kedge/cmd/kedge@latest
```

## Quickstart

### 1. Log in

```bash
kedge login --hub-url https://kedge.example.com
```

### 2. Connect a Kubernetes cluster

```bash
# Register the edge on the hub
kedge edge create my-cluster --type kubernetes

# Print the agent install command (includes the one-time join token)
kedge edge join-command my-cluster
```

Copy the printed command and run it on the target cluster. Once the agent connects:

```bash
kedge edge list                                # should show my-cluster as Ready
kedge kubeconfig edge my-cluster > kc.yaml    # get a kubeconfig for the edge
kubectl --kubeconfig kc.yaml get nodes
```

### 3. Use MCP with AI agents (Claude, Cursor, …)

kedge exposes all your connected Kubernetes clusters as a single MCP server, letting AI coding assistants interact with your clusters directly.

```bash
# Print the MCP endpoint URL + ready-to-use setup commands
kedge mcp url --name default
```

Example output:
```
https://hub.example.com/services/mcp/root:kedge:user-default/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/default/mcp

To add this MCP server to Claude Code:
  claude mcp add --transport http kedge "https://hub.example.com/..." -H "Authorization: Bearer <token>"

To add to Claude Desktop (claude_desktop_config.json):
  {
    "mcpServers": {
      "kedge": {
        "url": "https://hub.example.com/...",
        "headers": { "Authorization": "Bearer <token>" }
      }
    }
  }
```

The MCP server aggregates **all connected kubernetes-type clusters** into one endpoint. AI agents can list pods, describe deployments, apply manifests, and more — across all your clusters at once.

### 4. Connect a server (SSH mode)

```bash
kedge edge create my-server --type server
kedge edge join-command my-server
```

Run the printed command on the target host, then:

```bash
kedge ssh my-server              # interactive shell
kedge ssh my-server -- df -h     # single command
```

## CLI Reference

| Command | Description |
|---|---|
| `kedge login` | Authenticate with the hub (OIDC or static token) |
| `kedge edge create <name>` | Register a new edge |
| `kedge edge join-command <name>` | Print the agent run command with join token |
| `kedge edge list` | List all edges and their connection status |
| `kedge edge get <name>` | Show details for a specific edge |
| `kedge edge delete <name>` | Remove an edge |
| `kedge kubeconfig edge <name>` | Generate a kubeconfig for a Kubernetes-type edge |
| `kedge ssh <name>` | Open an SSH session to a server-mode edge |
| `kedge ssh <name> -- <cmd>` | Run a single command on a server-mode edge |
| `kedge agent run` | Start the agent as a foreground process |
| `kedge agent join` | Install the agent as a persistent service (systemd / Deployment) |
| `kedge mcp url --name <name>` | Print the Kubernetes multi-cluster MCP endpoint URL |
| `kedge mcp url --edge <name>` | Print the per-edge MCP endpoint URL |

## Documentation

- [Getting Started](https://faroshq.github.io/kedge/getting-started.html)
- [Helm Deployment](https://faroshq.github.io/kedge/helm.html)
- [Security & Auth](https://faroshq.github.io/kedge/security.html)
- [Ingress Setup](https://faroshq.github.io/kedge/ingress/)
- [Developer Guide](https://faroshq.github.io/kedge/developers.html)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build setup, running tests, and PR guidelines.

## License

Apache 2.0 — see [LICENSE](LICENSE)
