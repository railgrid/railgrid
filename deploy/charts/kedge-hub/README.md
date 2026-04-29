# kedge-hub Helm Chart

Deploys the kedge hub — the central control plane for managing distributed edge clusters and servers.

## Quick Install

```bash
helm install kedge-hub oci://ghcr.io/faroshq/charts/kedge-hub \
  --namespace kedge --create-namespace \
  --set hub.hubExternalURL=https://kedge.example.com
```

For a complete production setup (TLS, OIDC, ingress) see the [full docs](https://faroshq.github.io/kedge/helm.html).

## Prerequisites

- Kubernetes 1.27+ (k3s / k0s / GKE / EKS / etc.)
- Helm 3.x
- A `StorageClass` that supports `ReadWriteOnce` (for the kcp data PVC)
- **A publicly reachable endpoint** — agents connect to the hub from anywhere; the hub must be accessible on the internet. Set up a TLS-passthrough ingress or LoadBalancer service exposing port 9443. See [Ingress Setup](https://faroshq.github.io/kedge/ingress/) for nginx/gateway-api examples.
- An OIDC provider **or** static bearer tokens for auth

## Values Reference

### Image

| Key | Default | Description |
|-----|---------|-------------|
| `image.hub.repository` | `ghcr.io/faroshq/kedge-hub` | Hub image repository |
| `image.hub.tag` | `""` (chart `appVersion`) | Image tag override |
| `image.hub.pullPolicy` | `IfNotPresent` | Image pull policy |

### Hub

| Key | Default | Description |
|-----|---------|-------------|
| `hub.hubExternalURL` | `""` | **Required.** External URL used for kubeconfig generation and OIDC callbacks (e.g. `https://kedge.example.com`) |
| `hub.listenAddr` | `:9443` | Hub TLS listen address |
| `hub.devMode` | `false` | Enable development mode (verbose logging, relaxed security) |
| `hub.staticAuthTokens` | `[]` | Static bearer tokens for access. Each token creates its own user/workspace. Generate with `openssl rand -base64 32` |
| `hub.resources` | see values | CPU/memory requests and limits (includes embedded kcp overhead) |

### TLS (Hub)

| Key | Default | Description |
|-----|---------|-------------|
| `hub.tls.existingSecret` | `""` | Name of an existing Secret with `tls.crt` and `tls.key` |
| `hub.tls.selfSigned.enabled` | `true` | Auto-generate a self-signed cert (dev/local only) |
| `hub.tls.certManager.enabled` | `false` | Issue cert via cert-manager (recommended for production) |
| `hub.tls.certManager.issuerRef.name` | `""` | cert-manager `ClusterIssuer` or `Issuer` name |
| `hub.tls.certManager.dnsNames` | `[]` | DNS SANs for the cert (must include `hub.hubExternalURL` hostname) |

### OIDC

| Key | Default | Description |
|-----|---------|-------------|
| `idp.issuerURL` | `""` | OIDC issuer URL (must be reachable by hub and user browsers) |
| `idp.clientID` | `kedge` | OIDC client ID (register as a public client — no client secret needed) |

### kcp (Embedded)

| Key | Default | Description |
|-----|---------|-------------|
| `kcp.embedded.enabled` | `true` | Run kcp in-process (default) |
| `kcp.embedded.securePort` | `6443` | kcp API server port |
| `kcp.embedded.batteriesInclude` | `admin,user` | kcp batteries to load |
| `kcp.embedded.tls.selfSigned.enabled` | `true` | Self-signed cert for embedded kcp |
| `kcp.embedded.tls.certManager.enabled` | `false` | Use cert-manager for embedded kcp cert |

### kcp (External)

| Key | Default | Description |
|-----|---------|-------------|
| `kcp.external.enabled` | `false` | Connect to an external kcp instance |
| `kcp.external.existingSecret` | `""` | Secret name containing `admin.kubeconfig` |
| `kcp.external.kubeconfig` | `""` | Inline kubeconfig (not recommended for production) |

### Persistence

| Key | Default | Description |
|-----|---------|-------------|
| `persistence.size` | `10Gi` | PVC size for embedded kcp data and hub state |
| `persistence.storageClass` | `""` | Storage class (empty = cluster default) |
| `persistence.accessModes` | `[ReadWriteOnce]` | PVC access modes |

### Service

| Key | Default | Description |
|-----|---------|-------------|
| `service.type` | `ClusterIP` | Kubernetes service type |
| `service.hub.port` | `9443` | Service port |

### Ingress

| Key | Default | Description |
|-----|---------|-------------|
| `ingress.enabled` | `false` | Enable ingress |
| `ingress.className` | `""` | Ingress class name |
| `ingress.hosts` | `[]` | Ingress host rules |

> **Note:** The hub serves TLS directly. Use a **passthrough** ingress (e.g. NGINX `ssl-passthrough` or a `GatewayAPI` TLSRoute) rather than TLS termination at the ingress layer.

## Common Configurations

### Minimal (static token, self-signed TLS)

```yaml
hub:
  hubExternalURL: https://kedge.example.com
  staticAuthTokens:
    - mysecrettoken
```

### Production (cert-manager + OIDC)

```yaml
hub:
  hubExternalURL: https://kedge.example.com
  tls:
    selfSigned:
      enabled: false
    certManager:
      enabled: true
      issuerRef:
        name: letsencrypt-prod
        kind: ClusterIssuer
      dnsNames:
        - kedge.example.com

kcp:
  embedded:
    tls:
      selfSigned:
        enabled: false
      certManager:
        enabled: true
        issuerRef:
          name: letsencrypt-prod
          kind: ClusterIssuer
        dnsNames:
          - kedge.example.com

idp:
  issuerURL: https://dex.example.com/dex
  clientID: kedge
```

### External kcp

```yaml
kcp:
  embedded:
    enabled: false
  external:
    enabled: true
    existingSecret: kcp-admin-kubeconfig   # Secret with admin.kubeconfig key
```

## Ports

| Port | Protocol | Description |
|------|----------|-------------|
| 9443 | HTTPS/TLS | Hub API server + agent tunnel endpoint |
| 6443 | HTTPS/TLS | Embedded kcp API server (cluster-internal only) |

## Upgrading

```bash
helm upgrade kedge oci://ghcr.io/faroshq/charts/kedge-hub \
  --namespace kedge-system \
  --reuse-values \
  --set image.hub.tag=v0.0.33
```
