---
layout: default
title: Getting Started
nav_order: 2
description: "Quick start guide for Kedge"
---

# Getting Started
{: .no_toc }

Get up and running with Kedge in minutes.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Prerequisites

Before you begin, ensure you have the following installed:

- **Go 1.25+** - [Installation guide](https://go.dev/doc/install)
- **kind** - [Installation guide](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- **kubectl** - [Installation guide](https://kubernetes.io/docs/tasks/tools/)
- **Docker** - [Installation guide](https://docs.docker.com/get-docker/)

## Building from Source

Clone the repository and build all binaries:

```bash
git clone https://github.com/faroshq/faros-kedge.git
cd faros-kedge
make build
```

This builds three binaries:
- `kedge-hub` - The central control plane server
- `kedge-agent` - The agent that runs on each site
- `kedge` - The CLI tool for users

## Running the Development Stack

The fastest way to try Kedge is using the development mode, which runs the full stack locally with hot-reload:

```bash
make dev
```

This starts:
- **kcp** - The multi-tenant API server
- **Dex** - OIDC identity provider
- **Hub** - The Kedge control plane
- **Agent** - A local agent connected to the hub

## First Steps

In a separate terminal, run through the basic workflow:

### 1. Login to the Hub

```bash
make dev-login
```

This opens a browser for OIDC authentication and configures your kubeconfig.

### 2. Create a Site

```bash
make dev-site-create
```

This registers a new site (cluster) with the hub.

### 3. Start the Agent

```bash
make dev-run-agent
```

This starts the agent which connects to the hub via a reverse tunnel.

### 4. Deploy a Workload

```bash
make dev-create-workload
```

This creates a sample VirtualWorkload that gets scheduled to your site.

## Verifying the Setup

Check that everything is working:

```bash
# View registered sites
kubectl --context=kedge get sites

# View workloads
kubectl --context=kedge get virtualworkloads

# View placements
kubectl --context=kedge get placements
```

## Next Steps

Now that you have a working development environment:

- [Deploy to Kubernetes with Helm]({% link helm.md %}) - Production-ready deployment
- [Configure Identity Provider]({% link idp.md %}) - Set up external OIDC authentication
- [Set up Ingress]({% link ingress.md %}) - Expose the hub publicly via Cloudflare Tunnel
