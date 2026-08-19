---
layout: default
title: Developer Guide
nav_order: 8
description: "Local development environment with faros dev command"
---

# Developer Guide
{: .no_toc }

Set up a complete local development environment with a single command.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

The `faros dev` command creates a complete local development environment with:

- **Hub cluster** — A kind cluster running faros-hub with embedded kcp
- **Agent cluster** — A second kind cluster for deploying the faros-agent

Both clusters share a Docker network, allowing the agent to connect to the hub.

---

## Prerequisites

| Tool | Description |
|:-----|:------------|
| [Docker](https://docs.docker.com/get-docker/) | Container runtime (must be running) |
| [kind](https://kind.sigs.k8s.io/) | Kubernetes in Docker (installed automatically by the command) |
| [Helm](https://helm.sh/docs/intro/install/) | For deploying the agent chart |

---

## Quick Start

### 1. Build the CLI

```bash
make build-faros
```

### 2. Create the development environment

```bash
./bin/faros dev init --worker-count 1 --chart-path deploy/charts/faros-hub
```

This creates two kind clusters:
- `faros-hub` — Hub cluster with faros-hub installed
- `faros-agent` — Worker cluster (empty, ready for agent deployment)

The default `--worker-count` is `0` (hub only). Use `--worker-count N` to
spin up additional worker kind clusters when developing agents.

### 3. Follow the printed instructions

The command outputs step-by-step instructions for:
1. Setting up kubeconfig
2. Logging into the hub
3. Creating a site
4. Deploying the agent

---

## Step-by-Step Walkthrough

### Set kubeconfig to access hub cluster

```bash
export KUBECONFIG=faros-hub.kubeconfig
```

### Login to authenticate to the hub

```bash
faros login --hub-url https://faros.localhost:9443 --insecure-skip-tls-verify --token=dev-token
```

### Create a site in the hub

```bash
faros site create my-site --labels env=dev
```

### Wait for the site kubeconfig secret and extract it

```bash
kubectl get secret -n faros-system site-my-site-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > site-kubeconfig
```

The secret is created automatically after the site is registered.

### Deploy the agent into the agent cluster

First, create a namespace and secret with the site kubeconfig:

```bash
kubectl --kubeconfig faros-agent.kubeconfig create namespace faros-system

kubectl --kubeconfig faros-agent.kubeconfig create secret generic site-kubeconfig \
  -n faros-system \
  --from-file=kubeconfig=site-kubeconfig
```

Then install the agent Helm chart:

```bash
helm install faros-agent deploy/charts/faros-agent \
  --kubeconfig faros-agent.kubeconfig \
  -n faros-system \
  --set agent.edgeName=my-edge \
  --set agent.hub.existingSecret=site-kubeconfig
```

### Verify the agent is connected

```bash
faros site list
faros site get my-site
```

The site should show `tunnelConnected: true` and have a recent heartbeat.

---

## Command Reference

### faros dev init

Initializes a local faros environment.

```bash
faros dev init [flags]
```

**Flags:**

| Flag | Default | Description |
|:-----|:--------|:------------|
| `--hub-cluster-name` | `faros-hub` | Name of the hub kind cluster |
| `--agent-cluster-name` | `faros-agent` | Name of the worker (agent) kind cluster(s) |
| `--worker-count` | `0` | Number of worker kind clusters (0 = hub only) |
| `--chart-path` | `oci://ghcr.io/faroshq/charts/faros-hub` | Hub Helm chart (local path or OCI) |
| `--chart-version` | (auto) | Helm chart version (for OCI charts) |
| `--image` | `ghcr.io/faroshq/faros-hub` | Hub container image |
| `--tag` | (auto) | Hub image tag |
| `--kind-network` | `faros-dev` | Docker network for kind clusters |
| `--wait-for-ready-timeout` | `2m` | Timeout waiting for cluster readiness |

`--agent-count` is accepted as a deprecated alias for `--worker-count`.

**Examples:**

```bash
# Hub-only local environment (end users)
faros dev init

# Hub + 1 worker (typical developer setup)
faros dev init --worker-count 1 --chart-path deploy/charts/faros-hub

# Hub + 3 workers
faros dev init --worker-count 3

# Published OCI chart, pinned version
faros dev init --chart-path oci://ghcr.io/faroshq/charts/faros-hub --chart-version 0.1.0
```

### faros dev update

Upgrades the faros-hub Helm release on the existing hub kind cluster (image,
tag, chart version, …). Kind clusters themselves are not modified.

```bash
faros dev update [flags]
```

### faros dev delete

Deletes the local faros environment.

```bash
faros dev delete [flags]
```

This removes the hub kind cluster, any worker kind clusters that were
created (pass the same `--worker-count` you used at init time), and cleans
up kubeconfig files.

---

## Configuration

### Hub cluster

The hub cluster is configured with:
- Port mappings: `localhost:9443` -> hub service
- NodePort service on port 31443
- Self-signed TLS certificate
- Static auth token: `dev-token`
- Dev mode enabled (relaxed security)

### Agent cluster

The agent cluster is a plain kind cluster with no special configuration. The agent is deployed via Helm chart and connects to the hub through the shared Docker network.

### Docker network

Both clusters are created on the `faros-dev` Docker network, allowing them to communicate using container IPs. The hub's internal IP is displayed after cluster creation.

---

## Useful Commands

```bash
# List all sites
faros site list

# Get site details
faros site get my-site

# Check agent logs
kubectl --kubeconfig faros-agent.kubeconfig logs \
  -n faros-system \
  -l app.kubernetes.io/name=faros-agent -f

# Check hub logs
kubectl --kubeconfig faros-hub.kubeconfig logs \
  -n faros-system \
  -l app.kubernetes.io/name=faros-hub -f

# Delete the dev environment
faros dev delete
```

---

## Troubleshooting

### Tilt-cluster E2E: a resource never becomes Ready

The `Tilt E2E` workflow waits for each Tilt resource (e.g. `faros-hub`) to reach
`update=ok runtime=ok`. When one times out, the wait helper now prints a
collapsible **`diagnostics for stuck resource '<name>'`** group in the job log
containing: the resource's own Tilt logs (e.g. the hub's klog stdout, which
shows where bootstrap stalled), a cluster-wide `kubectl get pods -A`, a
describe + log tail for every non-Running pod, and the recent cluster events.
Open that group first when a run times out — it usually points straight at the
stuck step (slow kcp bootstrap, a crashlooping pod, an image pull, …).

### Hub chart not found

If you see:
```
Error: failed to locate OCI chart: ghcr.io/faroshq/charts/faros-hub:0.1.0: not found
```

Use the local chart path instead:
```bash
faros dev init --chart-path deploy/charts/faros-hub
```

### Agent can't connect to hub

1. Check the hub is running:
   ```bash
   kubectl --kubeconfig faros-hub.kubeconfig get pods -n faros-system
   ```

2. Verify the site kubeconfig has the correct hub IP:
   ```bash
   cat site-kubeconfig | grep server
   ```

   The server URL should use the hub's Docker network IP, not `localhost`.

3. Check agent logs:
   ```bash
   kubectl --kubeconfig faros-agent.kubeconfig logs \
     -n faros-system \
     -l app.kubernetes.io/name=faros-agent
   ```

### Site kubeconfig secret not created

The secret is created by the hub's RBAC controller after the site is registered. Wait a few seconds and check:

```bash
kubectl get secret -n faros-system site-my-site-kubeconfig
```

If it doesn't appear, check hub logs for errors.

### Cluster already exists

If the clusters already exist, the command will skip creation and reuse them. To start fresh:

```bash
faros dev delete
faros dev init --chart-path deploy/charts/faros-hub
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Docker Network (faros-dev)                  │
│                                                                 │
│  ┌─────────────────────────┐    ┌─────────────────────────┐   │
│  │   faros-hub cluster     │    │   faros-agent cluster   │   │
│  │                         │    │                         │   │
│  │  ┌───────────────────┐  │    │  ┌───────────────────┐  │   │
│  │  │    faros-hub      │  │◄───┼──│   faros-agent     │  │   │
│  │  │  (StatefulSet)    │  │    │  │   (Deployment)    │  │   │
│  │  └───────────────────┘  │    │  └───────────────────┘  │   │
│  │                         │    │                         │   │
│  │  Port: 31443 (NodePort) │    │                         │   │
│  └───────────┬─────────────┘    └─────────────────────────┘   │
│              │                                                  │
└──────────────┼──────────────────────────────────────────────────┘
               │
               ▼
        localhost:9443
        (for CLI access)
```

The agent establishes a reverse WebSocket tunnel to the hub, allowing the hub to proxy API requests to the agent's cluster.

---

## MCP Integration

faros exposes all connected Kubernetes clusters as a single [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server.

### URL format

```
https://<hub>/services/mcp/<workspace-cluster-id>/apis/faros.sh/v1alpha1/kubernetesmcps/<name>/mcp
```

### Getting the URL

```bash
faros mcp url --name default
```

This prints the URL and a ready-to-use `claude mcp add` command with your bearer token.

### Kubernetes resource

A `default` `Kubernetes` object is auto-created in every tenant workspace. It selects which kubernetes-type edges are included via `spec.edgeSelector` (empty = all connected kubernetes edges).

### How it works

1. The hub's MCP virtual workspace handler validates the bearer token.
2. Lists all `Edge` objects in the workspace, filters to `spec.type: kubernetes` + connected + label selector.
3. Builds a `MultiEdgeFarosEdgeProvider` that dials each edge over its revdial tunnel.
4. Passes control to `kubernetes-mcp-server` which implements the MCP protocol.

See [DEVELOPERS.md](https://github.com/faroshq/faros/blob/main/DEVELOPERS.md#mcp-integration) for the full internals reference.
