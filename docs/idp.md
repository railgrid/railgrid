# External Dex (Identity Provider)

This guide deploys Dex as a standalone identity provider using the upstream [dex Helm chart](https://github.com/dexidp/helm-charts), separate from the kedge-hub chart. This is the recommended approach for production deployments where you want:

- Dex accessible on a public URL (e.g., `auth.example.com`)
- Persistent sqlite3 storage on a PVC
- Independent scaling and lifecycle from the hub

## Architecture

```
Browser --> Cloudflare --> auth.example.com --> Dex (dex namespace)
                       --> api.example.com  --> Hub (kedge-system namespace)
```

The hub is configured with `idp.issuerURL` pointing to the external Dex instance.

## Prerequisites

- A running Kubernetes cluster with ingress configured (see [ingress.md](ingress.md))
- A GitHub OAuth app (or other identity connector)

## 1. Install the Dex Helm chart

```bash
helm repo add dex https://charts.dexidp.io
helm repo update
```

## 2. Create the Dex data PVC

The upstream dex Helm chart does not include built-in persistence. Create a PVC manually before installing:

```bash
kubectl create namespace dex

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: dex-data
  namespace: dex
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
EOF
```

## 3. Create a Dex values file

Create `dex-values.yaml`:

```yaml
config:
  issuer: https://idp.faros.sh

  logger:
    level: "debug"

  storage:
    type: sqlite3
    config:
      file: /var/dex/dex.db

  # Dex listens on plain HTTP inside the cluster.
  # TLS termination is handled by the Ingress / Cloudflare Tunnel.
  web:
    http: 0.0.0.0:5556

  staticClients:
    - id: kedge
      name: Kedge
      secret: "xxxxxxxxxxx=="
      redirectURIs:
        - https://hub.faros.sh/auth/callback

# Mount the PVC so sqlite3 data survives pod restarts
volumes:
  - name: dex-data
    persistentVolumeClaim:
      claimName: dex-data

volumeMounts:
  - name: dex-data
    mountPath: /var/dex

service:
  type: ClusterIP

ingress:
  enabled: true
  className: "cloudflare-tunnel"
  hosts:
    - host: idp.faros.sh
      paths:
        - path: /
          pathType: ImplementationSpecific
  tls:
   # This tells the Ingress to use a certificate for the specified host.
   # Cert-manager will see this, create a Certificate resource,
   # and automatically populate the 'dex-tls' secret with the new cert.
   - secretName: dex-tls
     hosts:
       - auth.faros.sh
```

Key points:
- **`volumes` + `volumeMounts`** — the upstream dex chart has no built-in persistence. We create a PVC separately and inject it via the chart's generic `volumes`/`volumeMounts` values. The sqlite3 database at `/var/dex/dex.db` is persisted across pod restarts.
- **`web.http`** — Dex listens on plain HTTP. TLS is terminated by the ingress (Cloudflare Tunnel handles HTTPS at the edge).
- **`staticClients`** — the `id` must match what kedge-hub is configured with (`idp.clientID`), and the `redirectURIs` must include the hub's `/auth/callback` endpoint.
- **`connectors`** — configure your identity provider. GitHub is shown here; Dex supports LDAP, SAML, OIDC, Google, and many others.

## 4. Deploy Dex

```bash
helm upgrade --install \
  --create-namespace \
  --namespace dex \
  dex dex/dex \
  -f hack/example/values-dex.yaml
```

Verify it's running:

```bash
kubectl -n dex get pods
```

If cloudflare tunnel is set up correctly, you should be able to see it:

```
kubectl get ingress -A                                                                                  17:04:01
NAMESPACE   NAME   CLASS               HOSTS          ADDRESS                                                 PORTS     AGE
dex         dex    cloudflare-tunnel   idp.faros.sh   a1fa66c5-7766-40e7-87fd-9d42391f07da.cfargotunnel.com   80, 443   5m54s
```

Check the OIDC discovery endpoint is reachable:

```bash
curl -s https://idp.faros.sh/.well-known/openid-configuration | head -20
```

## GitHub OAuth app setup

1. Go to **GitHub > Settings > Developer settings > OAuth Apps > New OAuth App**
2. Set:
   - **Application name:** Kedge Auth
   - **Homepage URL:** `https://api.example.com`
   - **Authorization callback URL:** `https://auth.example.com/callback`
3. After creating, copy the **Client ID** and generate a **Client Secret**
4. Use these in the `connectors[0].config` section of `dex-values.yaml`

If you want to restrict access to a GitHub organization, set `org: your-org` in the connector config.

## Troubleshooting

Check Dex logs:

```bash
kubectl -n dex logs -l app.kubernetes.io/name=dex
```

Common issues:
- **"invalid issuer"** — the `issuer` URL in Dex config must exactly match what the hub is configured with, including the scheme and path.
- **sqlite3 data lost on restart** — verify the PVC is bound and the `volumeMounts` map `/var/dex` to the `dex-data` volume. Check with `kubectl -n dex get pvc dex-data`.
- **Callback URL mismatch** — the `redirectURIs` in `staticClients` must exactly match the hub's callback URL (`<hubExternalURL>/auth/callback`).
