---
layout: default
title: Home
nav_order: 1
description: "Kedge - The ultimate home lab tool for managing distributed Kubernetes clusters"
permalink: /
---

# Kedge
{: .fs-9 }

The ultimate home lab tool for managing distributed Kubernetes clusters.
{: .fs-6 .fw-300 }

[Get Started]({% link getting-started.md %}){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 .mr-2 }
[View on GitHub](https://github.com/faroshq/kedge){: .btn .fs-5 .mb-4 .mb-md-0 }

---

## Why Kedge?

Managing multiple Kubernetes clusters across your home lab, remote locations, or edge sites is painful. You end up juggling kubeconfigs, SSH tunnels, VPNs, and port forwards. Kedge solves this by providing a single control plane that connects all your clusters through secure reverse tunnels.

**Perfect for:**

- **Home labs** — Manage k3s/k0s clusters on Raspberry Pis, NUCs, or old laptops from anywhere
- **Remote sites** — Connect clusters behind NAT, firewalls, or without public IPs
- **Edge deployments** — Deploy workloads to distributed locations with simple placement rules
- **Small teams** — Multi-tenant workspaces with OIDC authentication

## How It Works

{% include excalidraw.html file="architecture.excalidraw" alt="Kedge architecture diagram showing agents connecting to hub via reverse tunnels" %}

1. **Deploy a Hub** — Run the Kedge hub on any reachable server (cloud VM, VPS, or your main home server)
2. **Connect Sites** — Install the agent on each cluster; it establishes outbound tunnels to the hub
3. **Manage Everything** — Use the CLI to deploy workloads, check status, and manage all clusters from one place

## Key Features

| Feature | Description |
|:--------|:------------|
| **Reverse tunnels** | Agents connect outbound — no port forwarding, no VPN, no public IPs needed |
| **Multi-tenant** | Built on [kcp](https://github.com/kcp-dev/kcp) for workspace isolation |
| **Flexible auth** | OIDC via [Dex](https://github.com/dexidp/dex) or simple static tokens for personal use |
| **Placement rules** | Deploy workloads to clusters matching labels (location, arch, resources) |
| **Lightweight** | Works with k3s, k0s, kind, or full Kubernetes |
| **Simple networking** | HTTP/1.1 + WebSockets — works with any proxy, load balancer, or tunnel |

## Why HTTP/1.1?

Kedge intentionally uses HTTP/1.1 with WebSockets for all communication. While HTTP/2 or HTTP/3 offer some benefits, they create significant deployment complexity — especially for home labs and small setups.

With HTTP/1.1:

- **Works everywhere** — Compatible with nginx, Cloudflare, Caddy, HAProxy, and any reverse proxy
- **Easy debugging** — Standard tools like `curl` and browser DevTools work out of the box
- **No special configuration** — No need for gRPC passthrough, HTTP/2 termination, or ALPN setup
- **Tunnel-friendly** — WebSockets work through Cloudflare Tunnel, ngrok, and similar services

This design choice prioritizes ease of deployment over marginal performance gains. For home labs managing a handful of clusters, simplicity wins.

## Components

| Component | Description |
|:----------|:------------|
| **Hub** (`kedge-hub`) | Central control plane — hosts the API, authentication, tunnel endpoints, and scheduling |
| **Agent** (`kedge-agent`) | Runs on each site — establishes tunnels, reports status, reconciles workloads |
| **CLI** (`kedge`) | User tool — login, register sites, deploy workloads |

## Resources

| Resource | Scope | Description |
|:---------|:------|:------------|
| `Site` | Cluster | A connected Kubernetes cluster |
| `VirtualWorkload` | Namespace | Workload definition with placement rules |
| `Placement` | Namespace | Binding of a workload to a specific site |

---

## Documentation

| Guide | Description |
|:------|:------------|
| [Getting Started]({% link getting-started.md %}) | Set up your first hub and connect a site |
| [Security]({% link security.md %}) | Authentication options — static tokens and OIDC |
| [Ingress]({% link ingress/index.md %}) | Expose the hub publicly for remote access |
| [Helm Deployment]({% link helm.md %}) | Production deployment with Helm charts |

---

## Quick Start

```bash
# Clone and build
git clone https://github.com/faroshq/kedge.git
cd kedge
make build

# Run the full dev stack locally
make dev

# In another terminal
make dev-login           # Authenticate
make dev-site-create     # Register a site
make dev-run-agent       # Start the agent
make dev-create-workload # Deploy a sample workload
```

See the [Getting Started guide]({% link getting-started.md %}) for the full walkthrough.
