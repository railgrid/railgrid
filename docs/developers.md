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

The `faros dev` command creates a complete local development environment in
one kind cluster:

- **Hub** — faros-hub with embedded kcp, static token `dev-token`
- **Providers** — the providers named by `--providers` (default `edges`,
  `infrastructure`, `code`, `agents`, `app-studio`), installed from their published
  charts, onboarded on the hub and enabled in the dev user's default
  workspace
- **Edge** — the kind cluster joins itself as the KubernetesCluster edge
  `local`; the faros-agent runs next to the hub

`--worker-count N` adds plain kind clusters on the same Docker network for
connecting more edges by hand.

---

## Prerequisites

| Tool | Description |
|:-----|:------------|
| [Docker](https://docs.docker.com/get-docker/) | Container runtime (must be running) |
| [kind](https://kind.sigs.k8s.io/) | Kubernetes in Docker (installed automatically by the command) |
| [Helm](https://helm.sh/docs/intro/install/) | For deploying the agent chart into extra worker clusters |

---

## Quick Start

### 1. Build the CLI

```bash
make build-faros
```

### 2. Create the development environment

```bash
./bin/faros dev init --chart-path deploy/charts/faros-hub --provider-chart-repo .
```

This creates the `faros-hub` kind cluster with the hub (from the local chart),
the edges provider (from `providers/edges/deploy/chart`, published image) and
the agent joined as edge `local`. Drop the two path flags to use the published
charts instead. Add `--worker-count 1` for an extra empty `faros-agent` kind
cluster when developing agents against a separate cluster.

To run a locally built provider image, load it into kind and point at it:

```bash
docker build -f providers/edges/Dockerfile -t ghcr.io/faroshq/faros-edges-provider:dev .
kind load docker-image ghcr.io/faroshq/faros-edges-provider:dev --name faros-hub
./bin/faros dev init --provider-chart-repo . --provider-image-tag dev
```

`faros dev init` is idempotent: re-running it upgrades the hub and provider
releases and keeps the existing edge.

### 3. Follow the printed instructions

The command outputs step-by-step instructions for:
1. Setting up kubeconfig
2. Logging into the hub
3. Using the edge (`faros edge list`, `faros edge kubeconfig local`)
4. Connecting extra worker clusters, when `--worker-count` was set

---

## Step-by-Step Walkthrough

### Set kubeconfig to access hub cluster

```bash
export KUBECONFIG=faros-hub.kubeconfig
```

### Login to authenticate to the hub

```bash
faros login --hub-url https://console.127.0.0.1.sslip.io:9443 --insecure-skip-tls-verify --token=dev-token
```

### Create an edge in the hub

```bash
faros edge create my-edge --labels env=dev
```

The command prints the join token and every way to connect the agent (Helm,
`faros agent join`, `faros agent run`); `faros edge join-command my-edge`
prints it again.

### Wait for the edge kubeconfig secret and extract it

```bash
kubectl get secret -n faros-system edge-my-edge-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d > edge-kubeconfig
```

The secret is created automatically after the edge is registered.

### Deploy the agent into the agent cluster

First, create a namespace and secret with the edge kubeconfig:

```bash
kubectl --kubeconfig faros-agent.kubeconfig create namespace faros-system

kubectl --kubeconfig faros-agent.kubeconfig create secret generic edge-kubeconfig \
  -n faros-system \
  --from-file=kubeconfig=edge-kubeconfig
```

Then install the agent Helm chart:

```bash
helm install faros-agent deploy/charts/faros-agent \
  --kubeconfig faros-agent.kubeconfig \
  -n faros-system \
  --set agent.edgeName=my-edge \
  --set agent.hub.existingSecret=edge-kubeconfig
```

### Verify the agent is connected

```bash
faros edge list
faros edge get my-edge
faros connect my-edge && kubectl get nodes   # kubectl through the hub
```

The edge should show `Connected: true` and a recent heartbeat.

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
| `--worker-count` | `0` | Number of extra plain worker kind clusters |
| `--providers` | `edges,infrastructure,code,agents,app-studio` | Providers installed into the hub cluster (also `quickstart`; empty for none). Requirements are added: `app-studio` brings `infrastructure` |
| `--enable-providers` | `true` | Enable every installed provider in the dev user's default workspace, accepting its declared claims and hub access |
| `--provider-chart-repo` | `oci://ghcr.io/faroshq/charts` | OCI base for provider charts, or a faros checkout (`providers/<name>/deploy/chart`) |
| `--provider-chart-version` | (latest) | Pin the provider chart version (OCI only) |
| `--provider-image-tag` | (chart appVersion) | Provider image tag override. Charts from a checkout default to the latest published release, since their appVersion is a placeholder |
| `--with-edge` | `true` | Join the hub kind cluster itself as a KubernetesCluster edge |
| `--edge-name` | `local` | Name of that edge |
| `--apps-https-port` | `10443` | Host port published apps are served on. Set when the cluster is created |
| `--chart-path` | `oci://ghcr.io/faroshq/charts/faros-hub` | Hub Helm chart (local path or OCI) |
| `--chart-version` | (auto) | Helm chart version (for OCI charts) |
| `--image` | `ghcr.io/faroshq/faros-hub` | Hub container image |
| `--tag` | (auto) | Hub image tag |
| `--kind-network` | `faros-dev` | Docker network for kind clusters |
| `--wait-for-ready-timeout` | `2m` | Timeout waiting for cluster readiness |

`--with-dex` disables token login, and with it the provider and edge
automation (it signs in with `dev-token`); that mode is hub-only.

`--agent-count` is accepted as a deprecated alias for `--worker-count`.

**Examples:**

```bash
# Hub + edges provider + the cluster joined as edge "local" (default)
faros dev init

# Only edges, plus the quickstart provider
faros dev init --providers edges,quickstart

# Hub only
faros dev init --providers "" --with-edge=false

# Local hub and provider charts + 1 extra worker cluster
faros dev init --worker-count 1 --chart-path deploy/charts/faros-hub --provider-chart-repo .

# Published OCI charts, pinned versions
faros dev init --chart-version 0.1.31 --provider-chart-version 0.1.19
```

### faros dev update

Upgrades the faros-hub Helm release on the existing hub kind cluster (image,
tag, chart version, …) and then the provider releases with the current
`--providers` settings. Kind clusters and the edge are not modified.

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
- Static auth token: `dev-token`, whose user (RBAC identity
  `faros:static:47b9dce0e91570a1`) is on `--admin-users` so the CLI can drive
  the admin API
- `hub.internalURL` set to the in-cluster Service, so minted provider
  kubeconfigs stay inside the cluster
- Dev mode enabled (relaxed security)

### Providers

Each provider from `--providers` is onboarded the way a real install is:
`POST /api/admin/providers` creates the Provider (workspace, ServiceAccount,
kubeconfig Secret), the kubeconfig is fetched with `?server=internal`, and the
provider's chart is installed into namespace `faros-providers` as release
`<name>` with `catalogEntry.enabled=true`, so its init container registers the
CatalogEntry with in-cluster Service URLs. Heartbeats use `dev-token` from the
`faros-provider-hub-token` Secret.

Per provider:

- **infrastructure** runs in operator mode, as production does. The chart
  installs only the operator; it bootstraps the provider workspace through the
  minted kubeconfig, helm-installs kro into the cluster, runs the serve
  Deployment in namespace `faros-infrastructure-provider` and registers the
  CatalogEntry itself. Its templates publish apps through the Gateway below.
- **agents** and **app-studio** each get their own Postgres (`<name>-db` in
  `faros-providers`, on a PVC so data survives a Docker restart).
- **code** gets GitHub sign-in when `GITHUB_OAUTH_CLIENT_ID` and
  `GITHUB_OAUTH_CLIENT_SECRET` are set in the environment of `faros dev init`
  (the names `providers/code/.env` uses). The client secret goes into Secret
  `code-github-oauth`, and the callback is routed through the hub's
  `/services` proxy:
  `https://console.127.0.0.1.sslip.io:9443/services/providers/code/oauth/github/callback`.
  Register that URL on the OAuth App. Without the variables, tenants add a
  GitHub token as a Connection.
- **app-studio** requires infrastructure, which is added automatically. Its
  signed preview bridge is off, since it needs a signing key pair.

With `--enable-providers` (default), every installed provider is then enabled
in the dev user's default workspace in install order, accepting its declared
permission claims and hub access, and the command waits for each to report
Ready. Enabling does not wait for Ready first: kcp publishes an APIExport's
virtual-workspace endpoint only once the export has a consumer, and
infrastructure's readiness check needs that endpoint, so it turns Ready only
after the first workspace enables it.

After enabling, the command touches any provider APIExportEndpointSlice that
still has no endpoint, until kcp publishes one. Current provider charts write
their slice without `spec.export.path`, and kcp does not match such a slice to
a tenant binding that references the export by workspace path, so the first
binding alone never publishes the endpoint and the provider never engages the
workspace. For App Studio that means no search, browser or project instances.
If it happens in an environment created before this step existed, re-run
`faros dev init`.

### Dev CA

cert-manager holds one long-lived CA, `faros-dev-ca` (Secret and
ClusterIssuer), issued by the `faros-selfsigned` ClusterIssuer. It signs the
hub's serving certificate (the chart's `hub.tls.certManager`) and the apps
gateway's wildcard certificate, so trusting one file covers both. The command
exports it as `<hub-cluster-name>-ca.crt` next to the kubeconfig, and
`faros dev delete` removes it.

The chart's own self-signed option would not do: Helm regenerates that CA on
every render, so after a re-run the Secret no longer matches the certificate
the running hub serves. After every hub install or upgrade the command checks
that the hub serves a certificate the dev CA verifies, and restarts the hub pod
when it does not.

Use the file wherever a client has to trust the local hub:

```bash
faros mcp claude --ca-file faros-hub-ca.crt   # prints NODE_EXTRA_CA_CERTS=… claude
faros mcp codex --ca-file faros-hub-ca.crt    # prints CODEX_CA_CERTIFICATE=… codex
```

### Apps gateway

Installed with the infrastructure provider, because every template that
publishes an app (browser, searxng, simple-webapp, application) renders an
HTTPRoute and waits for a Gateway controller to accept it. Without Gateway API
kro rejects those graphs and their instances stay Pending.

- Envoy Gateway (`envoy` release in `envoy-gateway-system`, which also brings
  the Gateway API CRDs) with a GatewayClass whose data plane is a NodePort on
  30443. The kind node maps it to host port `--apps-https-port`.
- Gateway `faros-apps`, HTTPS listener for `*.apps.127.0.0.1.sslip.io`,
  certificate from the `faros-selfsigned` ClusterIssuer.
- The infrastructure operator gets `application.*` and `publishing.*` pointing
  at that zone and Gateway; the hub gets `publishedAppsDomain` and a portal
  frame source for it.
- CoreDNS answers `*.apps.127.0.0.1.sslip.io` with the Envoy Service IP and
  `console.127.0.0.1.sslip.io` with the hub Service IP, so pods reach both
  (public DNS would give them `127.0.0.1`, the pod itself).

Public DNS answers every `*.127.0.0.1.sslip.io` name with `127.0.0.1`, so
neither the hub nor the apps need an `/etc/hosts` entry, and the apps share a
site with the portal, so private-app sign-in cookies work in App Studio's
preview iframe. The Tilt stacks and `make run-hub-*` use the same hub host
(`DEV_HUB_URL` in the Makefile). A cluster created before the apps port
mapping existed serves apps only inside the cluster; recreate it to reach them
from the host.

### Edge

With `--with-edge`, the edges provider is enabled in the dev user's default
workspace (all declared claims accepted), a KubernetesCluster is created, and
the faros-agent chart is installed into namespace `faros-agent` of the hub
cluster with the join token, `agent.cluster` set to the workspace, and a
hostAlias that resolves `console.127.0.0.1.sslip.io` to the hub Service, so the agent
reaches the hub at the same URL the provider bakes into its kubeconfig.

### Worker clusters

Worker clusters are plain kind clusters with no special configuration. An
agent is deployed there via Helm chart and connects to the hub through the
shared Docker network.

### Docker network

All clusters are created on the `faros-dev` Docker network, allowing them to communicate using container IPs. The hub's internal IP is displayed after cluster creation.

---

## Useful Commands

```bash
# List all edges
faros edge list

# Get edge details
faros edge get my-edge

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

2. Verify the edge kubeconfig has the correct hub IP:
   ```bash
   cat edge-kubeconfig | grep server
   ```

   The server URL should use the hub's Docker network IP, not `localhost`.

3. Check agent logs:
   ```bash
   kubectl --kubeconfig faros-agent.kubeconfig logs \
     -n faros-system \
     -l app.kubernetes.io/name=faros-agent
   ```

### Site kubeconfig secret not created

The secret is created by the hub's RBAC controller after the edge is registered. Wait a few seconds and check:

```bash
kubectl get secret -n faros-system edge-my-edge-kubeconfig
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
  console.127.0.0.1.sslip.io:9443
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
faros mcp url --mcpserver-name default
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
