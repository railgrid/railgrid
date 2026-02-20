---
layout: default
title: Developer Guide
nav_order: 6
description: "Local development environment with kedge dev command"
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

The `kedge dev` command creates a complete local development environment with:

- **Hub cluster** — A kind cluster running kedge-hub with embedded kcp
- **Agent cluster** — A second kind cluster for deploying the kedge-agent

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
make build-kedge
```

### 2. Create the development environment

```bash
./bin/kedge dev create --chart-path deploy/charts/kedge-hub
```

This creates two kind clusters:
- `kedge-hub` — Hub cluster with kedge-hub installed
- `kedge-agent` — Agent cluster (empty, ready for agent deployment)

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
export KUBECONFIG=kedge-hub.kubeconfig
```

### Login to authenticate to the hub

```bash
kedge login --hub-url https://kedge.localhost:8443 --insecure-skip-tls-verify --token=dev-token
```

### Create a site in the hub

```bash
kedge site create my-site --labels env=dev
```

### Wait for the site kubeconfig secret and extract it

```bash
kubectl get secret -n kedge-system site-my-site-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > site-kubeconfig
```

The secret is created automatically after the site is registered.

### Deploy the agent into the agent cluster

First, create a namespace and secret with the site kubeconfig:

```bash
kubectl --kubeconfig kedge-agent.kubeconfig create namespace kedge-system

kubectl --kubeconfig kedge-agent.kubeconfig create secret generic site-kubeconfig \
  -n kedge-system \
  --from-file=kubeconfig=site-kubeconfig
```

Then install the agent Helm chart:

```bash
helm install kedge-agent deploy/charts/kedge-agent \
  --kubeconfig kedge-agent.kubeconfig \
  -n kedge-system \
  --set agent.siteName=my-site \
  --set agent.hub.existingSecret=site-kubeconfig
```

### Verify the agent is connected

```bash
kedge site list
kedge site get my-site
```

The site should show `tunnelConnected: true` and have a recent heartbeat.

---

## Command Reference

### kedge dev create

Creates the development environment.

```bash
kedge dev create [flags]
```

**Flags:**

| Flag | Default | Description |
|:-----|:--------|:------------|
| `--hub-cluster-name` | `kedge-hub` | Name of the hub kind cluster |
| `--agent-cluster-name` | `kedge-agent` | Name of the agent kind cluster |
| `--chart-path` | `deploy/charts/kedge-hub` | Path to hub Helm chart (local or OCI) |
| `--chart-version` | `0.1.0` | Helm chart version (for OCI charts) |
| `--image` | `ghcr.io/faroshq/kedge-hub` | Hub container image |
| `--tag` | (auto) | Hub image tag |
| `--kind-network` | `kedge-dev` | Docker network for kind clusters |
| `--wait-for-ready-timeout` | `2m` | Timeout waiting for cluster readiness |

**Examples:**

```bash
# Use local charts (development)
kedge dev create --chart-path deploy/charts/kedge-hub

# Use published OCI chart
kedge dev create --chart-path oci://ghcr.io/faroshq/charts/kedge-hub --chart-version 0.1.0

# Custom cluster names
kedge dev create --hub-cluster-name my-hub --agent-cluster-name my-agent
```

### kedge dev delete

Deletes the development environment.

```bash
kedge dev delete [flags]
```

This removes both kind clusters and cleans up kubeconfig files.

---

## Configuration

### Hub cluster

The hub cluster is configured with:
- Port mappings: `localhost:8443` -> hub service
- NodePort service on port 31443
- Self-signed TLS certificate
- Static auth token: `dev-token`
- Dev mode enabled (relaxed security)

### Agent cluster

The agent cluster is a plain kind cluster with no special configuration. The agent is deployed via Helm chart and connects to the hub through the shared Docker network.

### Docker network

Both clusters are created on the `kedge-dev` Docker network, allowing them to communicate using container IPs. The hub's internal IP is displayed after cluster creation.

---

## Useful Commands

```bash
# List all sites
kedge site list

# Get site details
kedge site get my-site

# Check agent logs
kubectl --kubeconfig kedge-agent.kubeconfig logs \
  -n kedge-system \
  -l app.kubernetes.io/name=kedge-agent -f

# Check hub logs
kubectl --kubeconfig kedge-hub.kubeconfig logs \
  -n kedge-system \
  -l app.kubernetes.io/name=kedge-hub -f

# Delete the dev environment
kedge dev delete
```

---

## Troubleshooting

### Hub chart not found

If you see:
```
Error: failed to locate OCI chart: ghcr.io/faroshq/charts/kedge-hub:0.1.0: not found
```

Use the local chart path instead:
```bash
kedge dev create --chart-path deploy/charts/kedge-hub
```

### Agent can't connect to hub

1. Check the hub is running:
   ```bash
   kubectl --kubeconfig kedge-hub.kubeconfig get pods -n kedge-system
   ```

2. Verify the site kubeconfig has the correct hub IP:
   ```bash
   cat site-kubeconfig | grep server
   ```

   The server URL should use the hub's Docker network IP, not `localhost`.

3. Check agent logs:
   ```bash
   kubectl --kubeconfig kedge-agent.kubeconfig logs \
     -n kedge-system \
     -l app.kubernetes.io/name=kedge-agent
   ```

### Site kubeconfig secret not created

The secret is created by the hub's RBAC controller after the site is registered. Wait a few seconds and check:

```bash
kubectl get secret -n kedge-system site-my-site-kubeconfig
```

If it doesn't appear, check hub logs for errors.

### Cluster already exists

If the clusters already exist, the command will skip creation and reuse them. To start fresh:

```bash
kedge dev delete
kedge dev create --chart-path deploy/charts/kedge-hub
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Docker Network (kedge-dev)                  │
│                                                                 │
│  ┌─────────────────────────┐    ┌─────────────────────────┐   │
│  │   kedge-hub cluster     │    │   kedge-agent cluster   │   │
│  │                         │    │                         │   │
│  │  ┌───────────────────┐  │    │  ┌───────────────────┐  │   │
│  │  │    kedge-hub      │  │◄───┼──│   kedge-agent     │  │   │
│  │  │  (StatefulSet)    │  │    │  │   (Deployment)    │  │   │
│  │  └───────────────────┘  │    │  └───────────────────┘  │   │
│  │                         │    │                         │   │
│  │  Port: 31443 (NodePort) │    │                         │   │
│  └───────────┬─────────────┘    └─────────────────────────┘   │
│              │                                                  │
└──────────────┼──────────────────────────────────────────────────┘
               │
               ▼
        localhost:8443
        (for CLI access)
```

The agent establishes a reverse WebSocket tunnel to the hub, allowing the hub to proxy API requests to the agent's cluster.
