# Kedge

Multi-tenant control plane for deploying and managing workloads across distributed edge sites.

## What it does

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

**Hub** (`kedge-hub`) — Central server that hosts the kcp API, OIDC auth, tunnel endpoints, and controllers for scheduling/status.

**Agent** (`kedge-agent`) — Runs on each site. Establishes a reverse tunnel to the hub, reports site status, and reconciles workloads.

**CLI** (`kedge`) — User-facing tool for login, site registration, and workload management.

## Key resources

| Resource | Scope | Description |
|---|---|---|
| `Site` | Cluster | A connected Kubernetes cluster |
| `VirtualWorkload` | Namespace | Workload definition with placement rules |
| `Placement` | Namespace | Binding of a workload to a specific site |

## Quick start

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
