---
layout: default
title: Helm Deployment
nav_order: 5
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

The kedge-hub Helm chart deploys **kcp + kedge-hub** as a single StatefulSet. This guide covers deploying to both local clusters (kind) and production environments.

For authentication configuration, see [Security]({% link security.md %}).

---

## Prerequisites

| Tool | Description |
|:-----|:------------|
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | Local Kubernetes cluster |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | Kubernetes CLI |
| [Helm](https://helm.sh/docs/intro/install/) v3+ | Package manager |
| [Docker](https://docs.docker.com/get-docker/) | Container runtime |

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

```bash
make docker-build-hub
kind load docker-image ghcr.io/faroshq/kedge-hub:$(git describe --tags --always --dirty 2>/dev/null || echo dev) --name kedge
```

{: .note }
The kcp image is pulled from its public registry (`ghcr.io/kcp-dev/kcp`).

### 3. Create a values file

Create `values-kind.yaml`:

```yaml
hub:
  hubExternalURL: "https://localhost:8443"
  devMode: true
  staticAuthToken: "<generate-with-openssl-rand-hex-32>"
```

For OIDC authentication instead of static token, see [Security]({% link security.md %}).

### 4. Install the chart

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values-kind.yaml \
  --namespace kedge-system \
  --create-namespace
```

### 5. Wait for pods to be ready

```bash
kubectl -n kedge-system get pods -w
```

Wait until `kedge-kedge-hub-0` is Running with all containers ready:

```bash
kubectl -n kedge-system wait --for=condition=ready pod -l app.kubernetes.io/name=kedge-hub --timeout=120s
```

{: .note }
The hub container waits for kcp to generate `admin.kubeconfig` before starting (30-60 seconds).

### 6. Port-forward and log in

```bash
kubectl -n kedge-system port-forward svc/kedge-kedge-hub 8443:8443
```

In another terminal:

```bash
kedge login \
  --hub-url https://localhost:8443 \
  --token <your-static-token> \
  --insecure-skip-tls-verify
```

---

## Production Deployment

For production, you need:

1. **Public ingress** — So remote agents can connect (see [Ingress]({% link ingress.md %}))
2. **Proper TLS** — Via cert-manager or your own certificates
3. **Authentication** — Static token or OIDC (see [Security]({% link security.md %}))

### Example production values

```yaml
hub:
  hubExternalURL: "https://hub.example.com"
  devMode: false

  # Choose one authentication method:
  staticAuthToken: "<token>"  # Simple
  # OR use idp section for OIDC

  tls:
    selfSigned:
      enabled: false
    certManager:
      enabled: true
      issuerRef:
        name: letsencrypt-prod
        kind: ClusterIssuer
      dnsNames:
        - "hub.example.com"

# For OIDC (optional)
idp:
  issuerURL: "https://idp.example.com"
  clientID: "kedge"
  clientSecret: "<secret>"

ingress:
  enabled: true
  className: "cloudflare-tunnel"  # or nginx, traefik, etc.
  hosts:
    - host: hub.example.com
      paths:
        - path: /
          pathType: ImplementationSpecific
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

```bash
helm upgrade kedge deploy/charts/kedge-hub/ \
  -f values.yaml \
  --namespace kedge-system
```

{: .note }
TLS secrets have `helm.sh/resource-policy: keep` and survive upgrades.

### Uninstalling

```bash
helm uninstall kedge --namespace kedge-system
```

This preserves PVCs (kcp data) and TLS secrets. To fully clean up:

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

## Values Reference

### Hub Configuration

| Key | Description | Default |
|:----|:------------|:--------|
| `hub.hubExternalURL` | **(required)** External URL for kubeconfigs and callbacks | `""` |
| `hub.listenAddr` | Hub listen address | `":8443"` |
| `hub.devMode` | Skip TLS verification for OIDC issuer | `false` |
| `hub.staticAuthToken` | Static bearer token (bypasses OIDC) | `""` |

### Identity Provider

| Key | Description | Default |
|:----|:------------|:--------|
| `idp.issuerURL` | OIDC issuer URL | `""` |
| `idp.clientID` | OIDC client ID | `"kedge"` |
| `idp.clientSecret` | OIDC client secret | `""` |

### TLS Configuration

| Key | Description | Default |
|:----|:------------|:--------|
| `hub.tls.existingSecret` | Name of existing TLS Secret | `""` |
| `hub.tls.selfSigned.enabled` | Generate self-signed cert | `true` |
| `hub.tls.selfSigned.dnsNames` | Extra DNS SANs | `[]` |
| `hub.tls.selfSigned.ipAddresses` | IP SANs | `["127.0.0.1"]` |
| `hub.tls.certManager.enabled` | Use cert-manager | `false` |
| `hub.tls.certManager.issuerRef.name` | Issuer name | `""` |
| `hub.tls.certManager.issuerRef.kind` | Issuer kind | `"ClusterIssuer"` |
| `hub.tls.certManager.dnsNames` | Additional DNS SANs | `[]` |

### Storage and kcp

| Key | Description | Default |
|:----|:------------|:--------|
| `persistence.size` | kcp data PVC size | `10Gi` |
| `persistence.storageClass` | Storage class | `""` |
| `kcp.featureGates` | kcp feature gates | `"WorkspaceMounts=true"` |
| `kcp.extraArgs` | Additional kcp CLI arguments | `[]` |

### Networking

| Key | Description | Default |
|:----|:------------|:--------|
| `service.type` | Service type | `ClusterIP` |
| `ingress.enabled` | Enable Ingress | `false` |
| `ingress.className` | Ingress class | `""` |
