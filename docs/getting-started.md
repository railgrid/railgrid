---
layout: default
title: Getting Started
nav_order: 2
description: "Run a local railgrid hub, connect a cluster, and hand a workspace to an AI agent"
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

This guide uses the CLI's built-in local environment: one kind cluster that runs the hub, the edges provider, and an agent that joins that same cluster as an edge. Nothing here is exposed to the internet. For a real install, follow [Helm deployment]({% link helm.md %}) instead; the CLI steps from section 3 onward are the same.

## Prerequisites

| Tool | Notes |
|:-----|:------|
| [Docker](https://docs.docker.com/get-docker/) | Runs the kind clusters |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Talks to the clusters |
| [Helm](https://helm.sh/docs/intro/install/) 3 | Only needed to connect extra clusters by hand; `railgrid dev init` installs the hub, provider and agent charts itself |
| `railgrid` CLI | A binary from the [releases page](https://github.com/railgrid/railgrid/releases), or `go install github.com/railgrid/railgrid/cmd/railgrid@latest` with Go 1.26+ |

## Step 1: Create a local hub

```bash
railgrid dev init
```

This creates one kind cluster, `railgrid-hub`, and installs three things into it from the published Helm charts:

- the hub, with a self-signed certificate, reachable at `https://console.127.0.0.1.sslip.io:9443`;
- the edges, infrastructure, code, agents and App Studio providers, each onboarded on the hub through the same admin flow a real install uses and enabled in your default workspace (`--providers` picks the set, `quickstart` is also supported, and App Studio always brings infrastructure along);
- the railgrid agent, which registers the kind cluster itself as a Kubernetes edge named `local` in your default workspace (`--with-edge=false` skips this, `--edge-name` renames it).

Use `--chart-path deploy/charts/railgrid-hub --provider-chart-repo .` to run the charts from a checkout. `--worker-count N` adds plain kind clusters for connecting more edges by hand, as in section 3b.

When it finishes, the command prints the remaining steps with the exact names it used. They are the ones below.

The local hub runs in development mode with a static token, `dev-token`. That is fine on a laptop and nowhere else.

## Step 2: Log in and pick a workspace

The hub answers on `https://console.127.0.0.1.sslip.io:9443`. Public DNS resolves every `*.127.0.0.1.sslip.io` name to `127.0.0.1`, so there is nothing to add to `/etc/hosts`. Log in:

```bash
railgrid login --hub-url https://console.127.0.0.1.sslip.io:9443 --insecure-skip-tls-verify --token dev-token
railgrid use
```

`railgrid use` lists your organizations and workspaces and makes one active. A static-token user gets a personal organization on first login.

## Step 3: Use the edge

The kind cluster already joined itself as the edge `local`, so there is nothing to register:

```bash
railgrid edge list                              # local shows Ready
railgrid edge kubeconfig local > local.yaml
kubectl --kubeconfig local.yaml get nodes    # reaches the kind cluster through the hub
```

The kubeconfig points at the hub, which proxies to the edge and authorizes each request as you in the workspace. If `local` is not Ready yet, the agent is still connecting; its logs are on the hub cluster:

```bash
kubectl --kubeconfig railgrid-hub.kubeconfig -n railgrid-agent logs deploy/railgrid-agent
```

### Step 3b: Connect another cluster by hand

To see the registration flow itself, or to connect a second cluster, create the environment with `--worker-count 1` so a plain `railgrid-agent` kind cluster exists on the same Docker network, then register an edge and hand its credentials to an agent there:

```bash
railgrid edge create worker --labels env=dev

# the hub mints a kubeconfig for the edge; extract it
kubectl get secret -n railgrid-system edge-worker-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > edge-kubeconfig

# hand it to the worker cluster and install the agent there
kubectl --kubeconfig railgrid-agent.kubeconfig create namespace railgrid-agent
kubectl --kubeconfig railgrid-agent.kubeconfig -n railgrid-agent create secret generic edge-kubeconfig \
  --from-file=kubeconfig=edge-kubeconfig

HUB_IP=$(docker inspect railgrid-hub-control-plane \
  -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
helm install railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \
  --kubeconfig railgrid-agent.kubeconfig -n railgrid-agent \
  --set agent.edgeName=worker \
  --set agent.hub.existingSecret=edge-kubeconfig \
  --set agent.hub.url=https://$HUB_IP:31443 \
  --set agent.hub.insecureSkipTLSVerify=true
```

The hub address override is needed because `console.127.0.0.1.sslip.io` resolves to `127.0.0.1`, which inside a pod is the pod itself; inside the Docker network the hub is its kind node's address on NodePort 31443. Once the agent connects, `railgrid edge list` shows `worker` as Ready. Against a real hub the same registration is one command: `railgrid edge join-command <name>` prints a Helm install that carries a one-time token.

## Step 4: Hand the workspace to an AI agent

Register the workspace's MCP server with your AI client:

```bash
railgrid mcp claude --ca-file railgrid-hub-ca.crt   # Claude Code
railgrid mcp codex --ca-file railgrid-hub-ca.crt    # Codex
```

Each command adds the server with the workspace's long-lived MCP token, then prints how to start the client. The local hub's certificate is signed by the dev CA that `railgrid dev init` wrote to `railgrid-hub-ca.crt`, so the client has to trust it:

```bash
NODE_EXTRA_CA_CERTS=$PWD/railgrid-hub-ca.crt claude
```

Codex cannot skip certificate verification. `railgrid mcp codex` writes a bundle of your system roots plus the dev CA and prints the matching `CODEX_CA_CERTIFICATE=… codex` line, which needs Codex 0.129.0 or later. Without `--ca-file`, `railgrid mcp claude` falls back to `NODE_TLS_REJECT_UNAUTHORIZED=0`, which turns verification off for everything that session connects to.

For another client, `railgrid mcp url --mcpserver-name default` prints the endpoint and configuration snippets. Then ask the assistant to list nodes on `local`. Tools from every enabled provider appear on the same endpoint, and every call runs under your identity and RBAC.

## Step 5: Enable a provider

Providers are Helm releases that register with the hub. `railgrid dev init` already installed edges, infrastructure, code, agents and App Studio and enabled them in your default workspace; edges is what made `local` possible. The [quickstart provider](https://github.com/railgrid/railgrid/tree/main/providers/quickstart) is the smallest one and a good next install: add `quickstart` to `--providers` and re-run `railgrid dev init`, and the [developer guide]({% link developers.md %}) covers writing your own.

Apps that infrastructure templates publish are served at `https://<app>.apps.127.0.0.1.sslip.io:10443` through an Envoy Gateway in the cluster. They share a site with the portal, so private-app sign-in works in App Studio's preview, and the certificate is self-signed like the hub's. App Studio's signed preview bridge is off. Model credentials for agents and App Studio are created in the portal under Models, per workspace. The code provider signs in to GitHub only when `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET` are set for `railgrid dev init`; register `https://console.127.0.0.1.sslip.io:9443/services/providers/code/oauth/github/callback` as the OAuth App's callback. Without them, add a GitHub token as a Connection in the portal. Once a provider is registered, enable it for a workspace in the portal at `https://console.127.0.0.1.sslip.io:9443/ui` and its APIs and tools appear.

## Step 6: Clean up

```bash
railgrid dev delete
```

Pass the same `--worker-count` you used at init time so the extra clusters are removed too.

## Troubleshooting

### The edge never becomes Ready

For the built-in edge `local`, the agent and the edges provider both run on the hub cluster:

```bash
kubectl --kubeconfig railgrid-hub.kubeconfig -n railgrid-agent logs deploy/railgrid-agent
kubectl --kubeconfig railgrid-hub.kubeconfig -n railgrid-providers logs deploy/edges
```

For an edge connected by hand, check the agent's logs on the worker cluster:

```bash
kubectl --kubeconfig railgrid-agent.kubeconfig -n railgrid-agent logs deploy/railgrid-agent
```

A certificate error means `agent.hub.insecureSkipTLSVerify` was not set for the self-signed local hub. A connection refused or timeout means `agent.hub.url` is not the hub node's Docker-network address, or the two kind clusters are not on the same network; recreate with `railgrid dev init`.

### `railgrid dev init` fails while onboarding a provider

The provider automation signs in to the hub with `dev-token` and calls the admin API, so it needs token login; `--with-dex` disables that and skips providers and the edge. A timeout waiting for a provider kubeconfig or a join token usually means the hub or provider pod is not healthy: `kubectl --kubeconfig railgrid-hub.kubeconfig get pods -A` shows which.

### `railgrid login` says OIDC is not configured

The local hub uses a static token. Pass `--token dev-token`.

### Port 9443 is already in use

Delete any previous environment with `railgrid dev delete`, or stop whatever is bound to the port, then run `railgrid dev init` again.

## Next steps

- [Helm deployment]({% link helm.md %}): a real hub with TLS, OIDC and ingress
- [Security]({% link security.md %}): authentication options and the provider hardening values
- [MCP architecture]({% link mcp-architecture.md %}): how the endpoint is assembled
- [Developer guide]({% link developers.md %}): building providers
