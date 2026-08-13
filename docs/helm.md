---
layout: default
title: Helm Deployment
nav_order: 5
description: "Deploy Faros Hub using Helm charts"
---

# Helm Deployment
{: .no_toc }

Deploy faros-hub into a Kubernetes cluster using Helm.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

The faros-hub Helm chart deploys **kcp + faros-hub**. In the default embedded-kcp mode it runs as a StatefulSet (kcp's embedded etcd is persisted to a PVC). When `kcp.external.enabled=true`, the hub is stateless and runs as a Deployment instead. This guide covers deploying to both local clusters (kind) and production environments.

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
kind create cluster --name faros
```

Verify it's running:

```bash
kubectl cluster-info --context kind-faros
```

### 2. Build and load the hub image

```bash
make docker-build-hub
kind load docker-image ghcr.io/faroshq/faros-hub:$(git describe --tags --always --dirty 2>/dev/null || echo dev) --name faros
```

{: .note }
The kcp image is pulled from its public registry (`ghcr.io/kcp-dev/kcp`).

### 3. Create a values file

Create `values-kind.yaml`:

```yaml
hub:
  hubExternalURL: "https://localhost:9443"
  devMode: true
  staticAuthToken: "<generate-with-openssl-rand-hex-32>"
```

For OIDC authentication instead of static token, see [Security]({% link security.md %}).

### 4. Install the chart

```bash
helm upgrade --install faros deploy/charts/faros-hub/ \
  -f values-kind.yaml \
  --namespace faros-system \
  --create-namespace
```

### 5. Wait for pods to be ready

```bash
kubectl -n faros-system get pods -w
```

Wait until `faros-faros-hub-0` is Running with all containers ready:

```bash
kubectl -n faros-system wait --for=condition=ready pod -l app.kubernetes.io/name=faros-hub --timeout=120s
```

{: .note }
The hub container waits for kcp to generate `admin.kubeconfig` before starting (30-60 seconds).

### 6. Port-forward and log in

```bash
kubectl -n faros-system port-forward svc/faros-faros-hub 9443:9443
```

In another terminal:

```bash
faros login \
  --hub-url https://localhost:9443 \
  --token <your-static-token> \
  --insecure-skip-tls-verify
```

---

## Production Deployment

For production, you need:

1. **Public ingress** — So remote agents can connect (see [Ingress]({% link ingress/index.md %}))
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
  # Optional for split-horizon IdPs:
  # browserAuthURL: "https://login.example.com/oauth2/auth"
  clientID: "faros"
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

## Scaling the hub

The hub runs as a single-replica StatefulSet by default because embedded kcp
keeps its etcd on a per-replica PVC — each replica would be its own control
plane. Scaling requires external kcp:

```yaml
kcp:
  embedded:
    enabled: false
  external:
    enabled: true
    existingSecret: faros-kcp-kubeconfig

replicaCount: 3
```

With `kcp.external.enabled=true` the chart renders a `Deployment` with a
`RollingUpdate` strategy. The chart refuses `replicaCount > 1` alongside
embedded kcp rather than rendering something that cannot work.

Every replica serves the full request surface — API proxy, portal, provider
proxies, GraphQL — and no request is pinned to a pod, so no session affinity is
needed on the ingress. Three pieces of hub state are what make that true:

- **Singleton controllers** (provider provisioning, MCPServer, organization
  bootstrap, soft-delete) run only on the replica holding the
  `faros-hub-controllers` Lease in `root:faros:system:controllers`. The provider
  *catalog* controller is deliberately exempt: it maintains the routing table
  the request path reads, so it runs everywhere.
- **Browser sessions and published-app authorization codes** live in
  kcp-backed Secrets in the same workspace, so a cookie minted by one replica
  resolves — and revokes — on all of them.
- **Provider heartbeats** are recorded on `CatalogEntry.status`, so liveness
  observed by the replica that received the beat reaches the others over the
  catalog watch instead of each replica timing the provider out on its own.

Two things cost more per replica rather than less:

- The per-IP auth rate limiter is per replica, so N replicas admit up to N times
  the configured burst. Enforce a hard global bound at the ingress if you need
  one.
- `hub.embeddedGraphQL` runs a schema listener per replica, so each one watches
  the APIExportEndpointSlice and rebuilds schemas independently. Correct, but N
  times the discovery load on kcp.

---

## Operations

### Checking Logs

```bash
# kcp container
kubectl -n faros-system logs faros-faros-hub-0 -c kcp

# hub container
kubectl -n faros-system logs faros-faros-hub-0 -c hub
```

### Upgrading

```bash
helm upgrade faros deploy/charts/faros-hub/ \
  -f values.yaml \
  --namespace faros-system
```

{: .note }
TLS secrets have `helm.sh/resource-policy: keep` and survive upgrades.

### Uninstalling

```bash
helm uninstall faros --namespace faros-system
```

This preserves PVCs (kcp data) and TLS secrets. To fully clean up:

```bash
kubectl -n faros-system delete pvc --all
kubectl -n faros-system delete secret faros-faros-hub-tls
kubectl delete namespace faros-system
```

To also remove the kind cluster:

```bash
kind delete cluster --name faros
```

---

## Values Reference

### Hub Configuration

| Key | Description | Default |
|:----|:------------|:--------|
| `hub.hubExternalURL` | **(required)** External URL for kubeconfigs and callbacks | `""` |
| `hub.listenAddr` | Hub listen address | `":9443"` |
| `hub.devMode` | Skip TLS verification for OIDC issuer | `false` |
| `hub.staticAuthToken` | Static bearer token (bypasses OIDC) | `""` |

### Identity Provider

| Key | Description | Default |
|:----|:------------|:--------|
| `idp.issuerURL` | OIDC issuer URL | `""` |
| `idp.browserAuthURL` | Public HTTPS browser authorization endpoint; discovery and token operations still use `issuerURL` | `""` |
| `idp.clientID` | OIDC client ID | `"faros"` |
| `idp.clientSecret` | OIDC client secret | `""` |
| `hub.publishedAppsDomain` | DNS zone published apps are served under; enables private published-app sign-in (`/auth/apps/*`) | `""` |
| `hub.disableTokenLogin` | Disable interactive static-token login (endpoint + portal form); bearer tokens still work for APIs | `false` |

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
| `kcp.featureGates` | kcp feature gates | `"WorkspaceMounts=true,CacheAPIs=true"` |
| `kcp.extraArgs` | Additional kcp CLI arguments | `[]` |

### Networking

| Key | Description | Default |
|:----|:------------|:--------|
| `service.type` | Service type | `ClusterIP` |
| `ingress.enabled` | Enable Ingress | `false` |
| `ingress.className` | Ingress class | `""` |
