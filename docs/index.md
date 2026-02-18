---
layout: default
title: Home
nav_order: 1
description: "Kedge - Multi-tenant control plane for deploying and managing workloads across distributed edge sites"
permalink: /
---

# Kedge
{: .fs-9 }

Multi-tenant control plane for deploying and managing workloads across distributed edge sites.
{: .fs-6 .fw-300 }

[Get Started]({% link getting-started.md %}){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 .mr-2 }
[View on GitHub](https://github.com/faroshq/faros-kedge){: .btn .fs-5 .mb-4 .mb-md-0 }

---

## What is Kedge?

Kedge connects remote Kubernetes clusters ("sites") to a central hub via reverse tunnels. Users define `VirtualWorkloads` with placement rules, and the hub schedules them onto matching sites. Agents running on each site pull workload specs through the tunnel and reconcile them locally.

Built on [kcp](https://github.com/kcp-dev/kcp) for multi-tenant workspace isolation and [Dex](https://github.com/dexidp/dex) for OIDC authentication.

## Architecture

```
┌──────────┐        reverse tunnel        ┌──────────────┐
│  Agent   │ ─────────────────────────▶   │   Hub        │
│  (site)  │                              │  (kcp+Dex)   │
└──────────┘                              └──────┬───────┘
                                                 │
┌──────────┐        reverse tunnel               │
│  Agent   │ ──────────────────────────▶─────────┘
│  (site)  │
└──────────┘
```

### Components

| Component | Description |
|:----------|:------------|
| **Hub** (`kedge-hub`) | Central server hosting the kcp API, OIDC auth, tunnel endpoints, and scheduling controllers |
| **Agent** (`kedge-agent`) | Runs on each site, establishes reverse tunnels, reports status, and reconciles workloads |
| **CLI** (`kedge`) | User-facing tool for login, site registration, and workload management |

### Key Resources

| Resource | Scope | Description |
|:---------|:------|:------------|
| `Site` | Cluster | A connected Kubernetes cluster |
| `VirtualWorkload` | Namespace | Workload definition with placement rules |
| `Placement` | Namespace | Binding of a workload to a specific site |

## Quick Start

```bash
# Build all binaries
make build

# Run full dev stack (kcp + Dex + Hub + Agent with hot-reload)
make dev

# In another terminal: login, create a site, deploy a workload
make dev-login
make dev-site-create
make dev-run-agent
make dev-create-workload
```

## Requirements

- Go 1.25+
- kind (for local agent cluster in dev mode)
- Docker

---

## Documentation

{: .note }
This documentation covers deploying Kedge in various environments, from local development to production.

### Deployment Guides

| Guide | Description |
|:------|:------------|
| [Helm Deployment]({% link helm.md %}) | Deploy kedge-hub using Helm charts |
| [Identity Provider]({% link idp.md %}) | Configure Dex as an external OIDC identity provider |
| [Ingress Setup]({% link ingress.md %}) | Expose the hub via Cloudflare Tunnel |

### Authentication Options

Kedge supports two authentication methods:

1. **OIDC** (production) - Deploy an identity provider (e.g., Dex) for full authentication flows
2. **Static token** (dev/CI) - Use a pre-shared token to bypass OIDC for development

See the [Helm Deployment guide]({% link helm.md %}#static-token-authentication-no-oidc) for static token configuration.
