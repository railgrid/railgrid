---
layout: default
title: Home
nav_order: 1
description: "faros: an open-source control plane for platform teams"
permalink: /
---

# faros
{: .fs-9 }

An open-source control plane for platform teams.
{: .fs-6 .fw-300 }

[Get started]({% link getting-started.md %}){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 .mr-2 }
[View on GitHub](https://github.com/faroshq/faros){: .btn .fs-5 .mb-4 .mb-md-0 }

---

## What faros is

Providers publish Kubernetes-style APIs, versioned actions and MCP tools into isolated tenant workspaces. Users, teams and organizations reach them through one portal, one CLI, one API and one MCP endpoint, and every call is authorized as the caller by the same RBAC. Edges extend the control plane to clusters and servers behind NAT through outbound tunnels.

faros is alpha software at v0.1.x. There is no hosted service; you run the hub yourself.

## How it works

{% include excalidraw.html file="architecture.excalidraw" alt="faros architecture: hub with workspaces, providers registering with it, and edge agents connecting outward" %}

1. **Run a hub.** One Helm release runs everything; larger installs can split the control-plane store into shards.
2. **Enable providers.** Each provider registers with the hub and serves its APIs, portal and MCP tools inside its own workspace. Tenants enable the ones they want.
3. **Connect edges.** Install the agent on a cluster or server; it dials out to the hub and becomes reachable for `kubectl`, SSH and AI agents.

## Key features

| Feature | Description |
|:--------|:------------|
| **Tenancy** | Organizations, teams and users each get an isolated workspace, with first-party membership and roles |
| **Providers** | Helm-installed extensions: APIs, controllers, a portal micro-frontend, MCP tools and actions; organizations can run their own |
| **Actions and MCP** | Versioned verbs on resources and one MCP endpoint per workspace, every call authorized as the caller |
| **Edges** | Outbound agent tunnels for clusters and Linux servers; `kubectl`, SSH and service proxying through the hub |
| **Auth** | Any OIDC provider, or a static token for a single user |
| **Plain networking** | HTTP/1.1 and WebSockets only, so any reverse proxy, ingress or tunnel in front of the hub works, and `curl` still debugs it |

## Components

| Component | Description |
|:----------|:------------|
| **Hub** (`faros-hub`) | The control plane: authentication, tenancy, provider registry, proxies and the MCP aggregate |
| **Providers** | Out-of-process extensions installed by Helm; see the [repository](https://github.com/faroshq/faros/tree/main/providers) |
| **Agent** (`faros agent`) | The CLI's agent mode, packaged as the agent image and chart; runs on each edge, establishes the tunnel and serves `kubectl`, SSH and service proxying |
| **CLI** (`faros`) | Log in, pick a workspace, manage edges, print MCP endpoints, run a local environment |

## Documentation

| Guide | Description |
|:------|:------------|
| [Getting started]({% link getting-started.md %}) | Run a local hub, connect a cluster, hand a workspace to an AI agent |
| [Helm deployment]({% link helm.md %}) | Production deployment, including the provider hardening values |
| [Single hub]({% link install-embedded-kcp.md %}) | One release behind Gateway API, control-plane store included |
| [Multi-shard]({% link install-external-kcp.md %}) | The control-plane store split into shards for larger installs |
| [Security]({% link security.md %}) | Static tokens, OIDC, and what the hub does with provider and agent credentials |
| [Ingress]({% link ingress/index.md %}) | Expose the hub through nginx, Gateway API or Cloudflare Tunnel |
| [MCP architecture]({% link mcp-architecture.md %}) | How tools from providers and edges become one endpoint |
| [Developer guide]({% link developers.md %}) | The local kind environment and provider development |

Under the hood, workspaces are served by [kcp](https://github.com/kcp-dev/kcp); the [developer guide]({% link developers.md %}) covers what that means for operators and provider authors. Design documents live in the repository: [providers](https://github.com/faroshq/faros/blob/main/docs/providers.md), [organizations](https://github.com/faroshq/faros/blob/main/docs/organizations.md), [provider actions](https://github.com/faroshq/faros/blob/main/docs/provider-actions.md), [BYO providers](https://github.com/faroshq/faros/blob/main/docs/byo-providers.md).
