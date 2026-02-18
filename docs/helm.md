---
layout: default
title: Helm Deployment
nav_order: 3
description: "Deploy Kedge Hub using Helm charts"
---

# Helm Deployment
{: .no_toc }

Deploy kedge-hub into a Kubernetes cluster using Helm.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

The kedge-hub chart deploys **kcp + kedge-hub** as a single StatefulSet. Authentication can be configured in two ways:

- **OIDC** (production) — Deploy an identity provider (e.g., Dex) separately. See [Identity Provider]({% link idp.md %}).
- **Static token** (dev/CI) — Set `hub.staticAuthToken` to bypass OIDC entirely. See [Static token authentication](#static-token-authentication-no-oidc) below.

## Prerequisites

| Tool | Description |
|:-----|:------------|
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes cluster |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Kubernetes CLI |
| [Helm](https://helm.sh/docs/intro/install/) v3+ | Package manager |
| [Docker](https://docs.docker.com/get-docker/) | Container runtime |

You'll also need either:
- A running OIDC identity provider (see [Identity Provider]({% link idp.md %})), or
- A static auth token for dev/minimal setups

---

## Deploying to kind

### 1. Create a kind cluster

```bash
kind create cluster --name kedge
```

Verify it's running:

```bash
kubectl cluster-info --context kind-kedge
```

### 2. Build and load the hub image

The hub image needs to be built locally and loaded into kind:

```bash
make docker-build-hub
kind load docker-image ghcr.io/faroshq/kedge-hub:$(git describe --tags --always --dirty 2>/dev/null || echo dev) --name kedge
```

{: .note }
The kcp image is pulled from its public registry (`ghcr.io/kcp-dev/kcp`), so no extra loading is needed.

### 3. Create a values file

Create a `values-kind.yaml` with the required settings:

```yaml
hub:
  hubExternalURL: "https://hub.faros.sh"
  devMode: false

idp:
  issuerURL: "https://idp.faros.sh"
  clientID: "kedge"
  clientSecret: "<your-idp-client-secret>"
```

{: .note }
`hub.devMode: true` disables TLS verification for the OIDC issuer, which is necessary when the identity provider uses a self-signed certificate.

### 4. Install the chart

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f hack/example/values-kind.yaml \
  --namespace kedge-system \
  --create-namespace
```

{: .warning }
If TLS certificate loading is slow via cert-manager (common on kind + macOS), apply this workaround:

```bash
kubectl -n cert-manager patch deployment cert-manager --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers=1.1.1.1:53,8.8.8.8:53"},{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers-only"}]'
```

### 5. Wait for pods to be ready

```bash
kubectl -n kedge-system get pods -w
```

You should see one workload:
- `kedge-kedge-hub-0` — StatefulSet pod running kcp + hub containers

Wait until it's `Running` and all containers are ready:

```bash
kubectl -n kedge-system wait --for=condition=ready pod -l app.kubernetes.io/name=kedge-hub --timeout=120s
```

{: .note }
The hub container waits for kcp to generate `admin.kubeconfig` before starting, so it may take 30-60 seconds.

### 6. Port-forward the hub

```bash
kubectl -n kedge-system port-forward svc/kedge-kedge-hub 8443:8443
```

### 7. Log in

```bash
kedge login --hub-url https://localhost:8443 --insecure-skip-tls-verify
```

This opens a browser for the OIDC login flow.

---

## Static Token Authentication (No OIDC)

For dev, CI, or minimal setups you can skip the OIDC provider entirely and use a pre-shared static bearer token. The static token grants full kcp admin access through the hub proxy.

### 1. Generate a token

```bash
openssl rand -hex 32
```

### 2. Deploy with the static token

Create a `values-static.yaml`:

```yaml
hub:
  hubExternalURL: "https://localhost:8443"
  devMode: true
  staticAuthToken: "<generated-token>"

# No idp section needed
```

Install:

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values-static.yaml \
  --namespace kedge-system \
  --create-namespace
```

### 3. Log in

```bash
kedge login \
  --hub-url https://localhost:8443 \
  --token <generated-token> \
  --insecure-skip-tls-verify
```

This writes a kubeconfig context named `kedge` with the token embedded directly (no exec plugin, no browser flow).

### 4. Verify

```bash
kubectl --context=kedge get namespaces
```

{: .warning }
**Security note:** The static token grants unrestricted kcp admin access. Do not use this in production — use OIDC authentication with a proper identity provider instead.

---

## Local Development (Without Helm)

You can also run the hub binary directly with `make`:

```bash
make run-hub-static STATIC_AUTH_TOKEN=<generated-token>
```

Or manually:

```bash
kedge-hub \
  --static-auth-token=<generated-token> \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=https://localhost:8443 \
  --external-kcp-kubeconfig=.kcp/admin.kubeconfig \
  --dev-mode
```

---

## Operations

### Checking Logs

```bash
# kcp container
kubectl -n kedge-system logs kedge-kedge-hub-0 -c kcp

# hub container
kubectl -n kedge-system logs kedge-kedge-hub-0 -c hub
```

### Upgrading

After making changes to values:

```bash
helm upgrade kedge deploy/charts/kedge-hub/ \
  -f values-kind.yaml \
  --namespace kedge-system
```

{: .note }
The TLS secret has `helm.sh/resource-policy: keep`, so it survives upgrades without being regenerated.

### Uninstalling

```bash
helm uninstall kedge --namespace kedge-system
```

This does **not** delete the PVCs (kcp data) or the kept TLS secret. To fully clean up:

```bash
kubectl -n kedge-system delete pvc --all
kubectl -n kedge-system delete secret kedge-kedge-hub-tls
kubectl delete namespace kedge-system
```

To also remove the kind cluster:

```bash
kind delete cluster --name kedge
```

---

## Production Deployment

The biggest challenge when moving beyond local development is making the hub and identity provider reachable over the public internet. Both need stable, publicly accessible URLs because:

- The OIDC login flow redirects the user's browser to the identity provider
- The hub's external URL is embedded in generated kubeconfigs
- Agents running on remote edge clusters need to reach the hub's tunnel endpoint

See the companion guides for production-oriented setups:

- [Ingress with Cloudflare Tunnel]({% link ingress.md %}) — Expose the hub via a Cloudflare Tunnel without needing a public IP
- [Identity Provider (Dex)]({% link idp.md %}) — Deploy Dex with persistent storage and a public ingress

---

## Values Reference

### Hub Configuration

| Key | Description | Default |
|:----|:------------|:--------|
| `hub.hubExternalURL` | **(required)** External URL for kubeconfig generation and OIDC callbacks | `""` |
| `hub.listenAddr` | Hub listen address | `":8443"` |
| `hub.devMode` | Skip TLS verification for OIDC issuer | `false` |
| `hub.staticAuthToken` | Static bearer token for admin access (bypasses OIDC) | `""` |

### Identity Provider

| Key | Description | Default |
|:----|:------------|:--------|
| `idp.issuerURL` | OIDC issuer URL (required unless `hub.staticAuthToken` is set) | `""` |
| `idp.clientID` | OIDC client ID | `"kedge"` |
| `idp.clientSecret` | **(required)** OIDC client secret | `""` |

### TLS Configuration

| Key | Description | Default |
|:----|:------------|:--------|
| `hub.tls.existingSecret` | Name of existing TLS Secret | `""` |
| `hub.tls.selfSigned.enabled` | Generate a self-signed TLS certificate | `true` |
| `hub.tls.selfSigned.dnsNames` | Extra DNS SANs for the self-signed cert | `[]` |
| `hub.tls.selfSigned.ipAddresses` | IP SANs for the self-signed cert | `["127.0.0.1"]` |
| `hub.tls.certManager.enabled` | Use cert-manager to issue TLS certificate | `false` |
| `hub.tls.certManager.issuerRef.name` | cert-manager Issuer/ClusterIssuer name | `""` |
| `hub.tls.certManager.issuerRef.kind` | Issuer kind | `"ClusterIssuer"` |
| `hub.tls.certManager.dnsNames` | Additional DNS SANs | `[]` |

### Storage and kcp

| Key | Description | Default |
|:----|:------------|:--------|
| `persistence.size` | kcp data PVC size | `10Gi` |
| `persistence.storageClass` | Storage class for kcp PVC | `""` |
| `kcp.featureGates` | kcp feature gates | `"WorkspaceMounts=true"` |
| `kcp.extraArgs` | Additional kcp CLI arguments | `[]` |

### Networking

| Key | Description | Default |
|:----|:------------|:--------|
| `service.type` | Service type for hub | `ClusterIP` |
| `ingress.enabled` | Enable Ingress for hub | `false` |
