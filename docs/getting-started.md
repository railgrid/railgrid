---
layout: default
title: Getting Started
nav_order: 2
description: "Run a local faros hub, connect a cluster, and hand a workspace to an AI agent"
---

# Getting Started
{: .no_toc }

Run a local hub, connect a cluster, and hand a workspace to an AI agent.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

This guide uses the CLI's built-in local environment: a kind cluster running the hub, and optionally a second kind cluster that joins it as an edge. Nothing here is exposed to the internet. For a real install, follow [Helm deployment]({% link helm.md %}) instead; the CLI steps from section 3 onward are the same.

## Prerequisites

| Tool | Notes |
|:-----|:------|
| [Docker](https://docs.docker.com/get-docker/) | Runs the kind clusters |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Talks to the clusters |
| [Helm](https://helm.sh/docs/intro/install/) 3 | Installs the hub and agent charts |
| `faros` CLI | A binary from the [releases page](https://github.com/faroshq/faros/releases), or `go install github.com/faroshq/faros/cmd/faros@latest` with Go 1.26+ |

## Step 1: Create a local hub

```bash
faros dev init --worker-count 1
```

This creates two kind clusters on a shared Docker network: `faros-hub`, which runs the hub from the published Helm chart with a self-signed certificate, and `faros-agent`, a plain cluster you will connect as an edge. Leave `--worker-count` off for a hub-only environment. Use `--chart-path deploy/charts/faros-hub` to run the chart from a checkout.

When it finishes, the command prints the remaining steps with the exact names it used. They are the ones below.

The local hub runs in development mode with a static token, `dev-token`. That is fine on a laptop and nowhere else.

## Step 2: Log in and pick a workspace

The hub answers on `https://faros.localhost:9443`. Add the name to `/etc/hosts` once, then log in:

```bash
echo '127.0.0.1 faros.localhost' | sudo tee -a /etc/hosts
faros login --hub-url https://faros.localhost:9443 --insecure-skip-tls-verify --token dev-token
faros use
```

`faros use` lists your organizations and workspaces and makes one active. A static-token user gets a personal organization on first login.

## Step 3: Connect the worker cluster as an edge

Register the edge, then hand its credentials to the agent in the worker cluster. `faros dev init` wrote one kubeconfig per kind cluster into the current directory: `faros-hub.kubeconfig` and `faros-agent.kubeconfig`.

```bash
export KUBECONFIG=faros-hub.kubeconfig
faros edge create local --labels env=dev

# the hub mints a kubeconfig for the edge; extract it
kubectl get secret -n faros-system edge-local-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > edge-kubeconfig

# hand it to the worker cluster and install the agent there
kubectl --kubeconfig faros-agent.kubeconfig create namespace faros-agent
kubectl --kubeconfig faros-agent.kubeconfig -n faros-agent create secret generic edge-kubeconfig \
  --from-file=kubeconfig=edge-kubeconfig

HUB_IP=$(docker inspect faros-hub-control-plane \
  -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
helm install faros-agent oci://ghcr.io/faroshq/charts/faros-agent \
  --kubeconfig faros-agent.kubeconfig -n faros-agent \
  --set agent.edgeName=local \
  --set agent.hub.existingSecret=edge-kubeconfig \
  --set agent.hub.url=https://$HUB_IP:31443 \
  --set agent.hub.insecureSkipTLSVerify=true
```

The hub address override is needed because `faros.localhost` resolves only on your machine; inside the Docker network the hub is its kind node's address on NodePort 31443. When the agent connects:

```bash
faros edge list                              # local shows Ready
faros kubeconfig edge local > local.yaml
kubectl --kubeconfig local.yaml get nodes    # reaches the worker through the hub
```

The kubeconfig points at the hub, which proxies to the edge and authorizes each request as you in the workspace. Against a real hub the same registration is one command: `faros edge join-command <name>` prints a Helm install that carries a one-time token.

## Step 4: Hand the workspace to an AI agent

```bash
faros mcp url --name default
```

The command prints the workspace's MCP endpoint and configuration snippets for Claude Code and Claude Desktop. Add it, then ask the assistant to list nodes on `local`. Tools from every enabled provider appear on the same endpoint, and every call runs under your identity and RBAC.

## Step 5: Enable a provider

Providers are Helm releases that register with the hub. The [quickstart provider](https://github.com/faroshq/faros/tree/main/providers/quickstart) is the smallest one and a good first install; the [developer guide]({% link developers.md %}) covers installing it into the local environment and writing your own. Once a provider is registered, enable it for the workspace in the portal at `https://localhost:9443` and its APIs and tools appear.

## Step 6: Clean up

```bash
faros dev delete --worker-count 1
```

## Troubleshooting

### The edge never becomes Ready

Check the agent's logs on the worker cluster:

```bash
kubectl --kubeconfig faros-agent.kubeconfig -n faros-agent logs deploy/faros-agent
```

A certificate error means `agent.hub.insecureSkipTLSVerify` was not set for the self-signed local hub. A connection refused or timeout means `agent.hub.url` is not the hub node's Docker-network address, or the two kind clusters are not on the same network; recreate with `faros dev init`.

### `faros login` says OIDC is not configured

The local hub uses a static token. Pass `--token dev-token`.

### Port 9443 is already in use

Delete any previous environment with `faros dev delete`, or stop whatever is bound to the port, then run `faros dev init` again.

## Next steps

- [Helm deployment]({% link helm.md %}): a real hub with TLS, OIDC and ingress
- [Security]({% link security.md %}): authentication options and the provider hardening values
- [MCP architecture]({% link mcp-architecture.md %}): how the endpoint is assembled
- [Developer guide]({% link developers.md %}): building providers
