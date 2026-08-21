# faros-hub Helm Chart

Deploys the faros hub — the central control plane for managing distributed edge clusters and servers.

## Quick Install

```bash
helm install faros-hub oci://ghcr.io/faroshq/charts/faros-hub \
  --namespace faros --create-namespace \
  --set hub.hubExternalURL=https://faros.example.com
```

For a complete production setup (TLS, OIDC, ingress) see the [full docs](https://faroshq.github.io/faros/helm.html).

## Prerequisites

- Kubernetes 1.27+ (k3s / k0s / GKE / EKS / etc.)
- Helm 3.x
- A `StorageClass` that supports `ReadWriteOnce` (for the kcp data PVC)
- **A publicly reachable endpoint** — agents connect to the hub from anywhere; the hub must be accessible on the internet. Set up a TLS-passthrough ingress or LoadBalancer service exposing port 9443. See [Ingress Setup](https://faroshq.github.io/faros/ingress/) for nginx/gateway-api examples.
- An OIDC provider **or** static bearer tokens for auth

## Values Reference

### Image

| Key | Default | Description |
|-----|---------|-------------|
| `image.hub.repository` | `ghcr.io/faroshq/faros-hub` | Hub image repository |
| `image.hub.tag` | `""` (chart `appVersion`) | Image tag override |
| `image.hub.pullPolicy` | `IfNotPresent` | Image pull policy |

### Hub

| Key | Default | Description |
|-----|---------|-------------|
| `hub.hubExternalURL` | `""` | **Required.** External URL used for kubeconfig generation and OIDC callbacks (e.g. `https://faros.example.com`) |
| `hub.internalURL` | `""` | Address in-cluster components use to reach the hub, baked into minted provider kubeconfigs. Defaults to `hub.hubExternalURL`, which routes provider→hub traffic out through the public hostname and back. Set to the in-cluster Service (`https://<release>-faros-hub.<namespace>.svc.cluster.local:9443`) to keep it inside the cluster. Leave empty when providers run outside the cluster. |
| `hub.listenAddr` | `:9443` | Hub TLS listen address |
| `hub.devMode` | `false` | Enable development mode (verbose logging, relaxed security) |
| `hub.staticAuthTokens` | `[]` | Static bearer tokens for access. Each token creates its own user/workspace. Generate with `openssl rand -base64 32` |
| `hub.adminUsers` | `[]` | Platform-admin identities allowed at `/api/admin/*` + the portal `/bonkers` area. Match a User by name, email, or rbacIdentity. Empty disables the admin surface (the `/bonkers` menu item stays hidden). For a static token the identity is `static-<first8chars>@faros.local`. |
| `hub.resources` | see values | CPU/memory requests and limits (includes embedded kcp overhead) |

### Product telemetry

Product telemetry defaults to `off`: the hub creates no telemetry worker and makes no receiver requests. `saas` mode accepts only catalog-declared events from registered first-party provider ServiceAccounts, pseudonymizes identifiers with keyed HMAC, and sends bounded CloudEvents batches. Create the referenced Secret separately so credentials never enter Helm values:

```bash
kubectl -n faros create secret generic faros-telemetry \
  --from-literal=sink-token="$(openssl rand -hex 32)" \
  --from-literal=hmac-secret="$(openssl rand -hex 32)"
```

| Key | Default | Description |
|-----|---------|-------------|
| `telemetry.mode` | `off` | `off` or explicit `saas` opt-in |
| `telemetry.endpoint` | `""` | HTTPS receiver CloudEvents batch endpoint, normally ending in `/v1/events`; credentials, query strings, and fragments are rejected |
| `telemetry.installationID` | `""` | Stable opaque installation identifier; required in SaaS mode |
| `telemetry.existingSecret` | `""` | Existing Secret containing the sink bearer and HMAC key; required in SaaS mode |
| `telemetry.sinkTokenKey` | `sink-token` | Sink bearer key in the existing Secret; minimum 16 characters |
| `telemetry.hmacSecretKey` | `hmac-secret` | Identifier HMAC key in the existing Secret; minimum 32 characters |
| `telemetry.queueSize` | `1024` | Bounded in-memory event queue |
| `telemetry.batchSize` | `100` | Maximum events per CloudEvents batch |
| `telemetry.flushInterval` | `2s` | Maximum batch delay |
| `telemetry.sendTimeout` | `5s` | Per-attempt receiver timeout |
| `telemetry.shutdownTimeout` | `5s` | Bounded shutdown drain |

### TLS (Hub)

| Key | Default | Description |
|-----|---------|-------------|
| `hub.tls.existingSecret` | `""` | Name of an existing Secret with `tls.crt` and `tls.key` |
| `hub.tls.selfSigned.enabled` | `true` | Auto-generate a CA-signed self-signed cert (dev/local only); the Secret also includes the CA as `ca.crt` |
| `hub.tls.certManager.enabled` | `false` | Issue cert via cert-manager (recommended for production) |
| `hub.tls.certManager.issuerRef.name` | `""` | cert-manager `ClusterIssuer` or `Issuer` name |
| `hub.tls.certManager.dnsNames` | `[]` | DNS SANs for the cert (must include `hub.hubExternalURL` hostname) |

### OIDC

| Key | Default | Description |
|-----|---------|-------------|
| `idp.issuerURL` | `""` | Canonical OIDC issuer URL used by discovery, token exchange, JWKS, and issuer validation; must be reachable by the hub |
| `idp.browserAuthURL` | `""` | Optional public HTTPS authorization endpoint for browser redirects when the issuer uses cluster-internal DNS |
| `idp.clientID` | `faros` | OIDC client ID (register as a public client — no client secret needed) |

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
  hubExternalURL: https://faros.example.com
  staticAuthTokens:
    - mysecrettoken
```

### Production (cert-manager + OIDC)

```yaml
hub:
  hubExternalURL: https://faros.example.com
  tls:
    selfSigned:
      enabled: false
    certManager:
      enabled: true
      issuerRef:
        name: letsencrypt-prod
        kind: ClusterIssuer
      dnsNames:
        - faros.example.com

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
          - faros.example.com

idp:
  issuerURL: https://dex.example.com/dex
  # Only needed when issuerURL is not browser-reachable:
  # browserAuthURL: https://login.example.com/dex/auth
  clientID: faros
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
helm upgrade faros oci://ghcr.io/faroshq/charts/faros-hub \
  --namespace faros-system \
  --reuse-values \
  --set image.hub.tag=v0.0.40
```
