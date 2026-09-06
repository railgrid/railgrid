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
| `hub.extraArgs` | `[]` | Extra hub command-line flags appended after the flags the chart renders, for flags the chart does not model yet (e.g. `--providers=edges,infrastructure`). The chart refuses an entry that repeats a flag it renders from a value — set the value instead. |

### Provider hardening

Every key is unset by default, which leaves the binary's own default for this
release (the staged, soft posture) and renders nothing into the hub args. The
next release flips the defaults to `enforce` / `platform` / `false`; set them
here to move early. See the [docs](https://faroshq.github.io/faros/helm.html#turning-on-the-hardened-defaults-early)
for the recommended production settings.

| Key | Default | Description |
|-----|---------|-------------|
| `hub.security.providerHeartbeatAuth` | `""` (binary: `warn`) | What the hub does with a provider heartbeat whose bearer token does not verify as that provider's own service account: `warn` logs and accepts it, `enforce` rejects it. Next release defaults to `enforce`. |
| `hub.security.providerDelegatedTokens` | `""` (binary: `off`) | Which platform providers receive a short-lived workspace-scoped ServiceAccount token instead of the caller's own bearer on `/services/providers/*`: `off`, `platform` (all except `providerDelegatedTokensExclude`), or `all`. Org-owned providers are always delegated. Next release defaults to `platform`. |
| `hub.security.providerDelegatedTokensExclude` | `[]` (binary: `[edges]`) | Platform providers that keep receiving the caller's bearer under `platform`. Setting it replaces the built-in list, so include `edges` if you still need it (its SSH data plane resolves the caller with a TokenReview on the bearer). |
| `hub.security.providerWorkspaceClusterAdmin` | `null` (binary: `true`) | Role bound to each provider's ServiceAccount inside its own provider workspace: `true` keeps cluster-admin, `false` binds the narrower generated `faros:provider` ClusterRole. Opt-in this release: the infrastructure provider serves its own CRDs from its provider workspace, which the narrow role does not grant, so confirm your providers first. Next release defaults to `false`. Flipping replaces the existing binding. |

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
