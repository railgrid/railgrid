---
layout: default
title: Getting Started
nav_order: 2
description: "Set up your first Kedge hub and connect a site"
---

# Getting Started
{: .no_toc }

Set up Kedge and connect your first cluster in minutes.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

This guide walks you through:

1. Running the hub locally (development mode)
2. Connecting a site (k3s, k0s, kind, or any Kubernetes cluster)
3. Deploying your first workload

For production deployments, see [Helm Deployment]({% link helm.md %}) after completing this guide.

---

## Prerequisites

| Tool | Description |
|:-----|:------------|
| [Go 1.25+](https://go.dev/doc/install) | Required to build from source |
| [Docker](https://docs.docker.com/get-docker/) | Container runtime |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes cluster (for dev mode) |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Kubernetes CLI |

---

## Step 1: Build from Source

Clone the repository and build all binaries:

```bash
git clone https://github.com/faroshq/kedge.git
cd kedge
make build
```

This builds three binaries in `bin/`:

| Binary | Description |
|:-------|:------------|
| `kedge-hub` | The central control plane server |
| `kedge-agent` | The agent that runs on each site |
| `kedge` | The CLI for users |

---

## Step 2: Run the Development Stack

The fastest way to try Kedge is using the development mode, which runs the full stack locally:

```bash
make dev
```

This starts:

- **kcp** — Multi-tenant API server
- **Dex** — OIDC identity provider
- **Hub** — Kedge control plane
- **kind cluster** — A local Kubernetes cluster for the agent

{: .note }
The dev stack uses hot-reload, so code changes are automatically picked up.

Wait until you see the hub is ready:

```
kedge-hub: listening on :8443
```

---

## Step 3: Log In

Open a new terminal and authenticate:

```bash
make dev-login
```

This opens a browser for OIDC login. Use the development credentials:

- **Email:** `admin@example.com`
- **Password:** `password`

After login, your kubeconfig is configured with a `kedge` context.

---

## Step 4: Register a Site

Create a new site (cluster) registration:

```bash
make dev-site-create
```

This creates a `Site` resource in the hub. You can view it:

```bash
kubectl --context=kedge get sites
```

---

## Step 5: Start the Agent

Start the agent on the local kind cluster:

```bash
make dev-run-agent
```

The agent:

1. Connects to the hub via a reverse tunnel
2. Reports the site status
3. Watches for workloads to deploy

Check that the site shows as connected:

```bash
kubectl --context=kedge get sites
```

The `READY` column should show `True`.

---

## Step 6: Deploy a Workload

Deploy a sample workload:

```bash
make dev-create-workload
```

This creates a `VirtualWorkload` resource. The hub schedules it to available sites, creating `Placement` resources:

```bash
# View the workload
kubectl --context=kedge get virtualworkloads

# View the placement (binding to a site)
kubectl --context=kedge get placements
```

The agent picks up the placement and reconciles the actual workload on the kind cluster:

```bash
kubectl --context=kind-kedge-dev get pods
```

---

## What Just Happened?

```
┌──────────────────────────────────────────────────────────────────┐
│                          Hub                                     │
│                                                                  │
│  1. You created a VirtualWorkload                                │
│  2. Hub found matching Sites (via placement rules)               │
│  3. Hub created a Placement binding workload → site              │
│                                                                  │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                           │ reverse tunnel
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                        Agent (kind cluster)                      │
│                                                                  │
│  4. Agent saw the Placement                                      │
│  5. Agent created the actual Pod/Deployment on the cluster       │
│  6. Agent reported status back to the Hub                        │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

---

## Connecting Your Own Cluster

To connect a real cluster (k3s, k0s, etc.) instead of the dev kind cluster:

### 1. Create a site registration

```bash
kedge site create --name my-home-server
```

This outputs a bootstrap token.

### 2. Run the agent

On the target cluster, run the agent with the bootstrap token:

```bash
kedge-agent \
  --hub-url https://your-hub-url:8443 \
  --bootstrap-token <token> \
  --site-name my-home-server
```

Or deploy via Helm (see agent chart documentation).

### 3. Verify connection

```bash
kubectl --context=kedge get sites
```

---

## Next Steps

| Guide | Description |
|:------|:------------|
| [Security]({% link security.md %}) | Configure authentication — static tokens for personal use, OIDC for teams |
| [Ingress]({% link ingress.md %}) | Expose the hub publicly so remote agents can connect |
| [Helm Deployment]({% link helm.md %}) | Deploy the hub to a real Kubernetes cluster |

---

## Troubleshooting

### Agent can't connect to hub

- Check that the hub is reachable from the agent
- For local dev, both run on the same machine so `localhost` works
- For remote agents, see [Ingress]({% link ingress.md %}) to expose the hub

### Login fails

- Check Dex is running: look for "dex: listening on :5556" in the dev output
- Check the browser console for OIDC errors

### Workload not deploying

```bash
# Check site status
kubectl --context=kedge describe site <site-name>

# Check agent logs
# (in dev mode, look at the terminal running make dev-run-agent)

# Check placement status
kubectl --context=kedge describe placement <placement-name>
```

### View hub logs

In dev mode, logs are printed to the terminal. For Helm deployments:

```bash
kubectl -n kedge-system logs -l app.kubernetes.io/name=kedge-hub -c hub
```
