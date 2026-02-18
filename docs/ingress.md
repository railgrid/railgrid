# Ingress with Cloudflare Tunnel

This guide sets up public ingress for kedge-hub using [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/), which exposes cluster services to the internet without requiring a public IP or cloud LoadBalancer. This is particularly useful for home labs, edge deployments, and kind clusters.
In this case we will expose hub and idp (Dex) through the tunnel.

## How it works

The Cloudflare Tunnel Ingress Controller runs inside the cluster and establishes an outbound connection to Cloudflare's edge network. The tunnel operates in **passthrough** mode — TLS is **not** terminated at Cloudflare's edge. Traffic flows encrypted end-to-end from the client to the hub, which serves TLS directly using a cert-manager certificate.

```
Browser/CLI --TLS--> Cloudflare Edge --passthrough--> Tunnel --> k8s Service --> Hub (TLS on :8443)
```

## Prerequisites

- A Cloudflare account with a domain configured
- A Cloudflare API token with tunnel permissions (`Account:Cloudflare Tunnel:Edit`, `Zone:DNS:Edit`)
- Your Cloudflare account ID (found in the dashboard URL)
- A tunnel name (will be auto-created if it doesn't exist)

## 1. Install cert-manager

The hub serves TLS directly (no TLS termination at the tunnel). cert-manager issues certificates via Cloudflare DNS01 challenges, which work without the app being reachable — cert-manager validates domain ownership by creating DNS TXT records through the Cloudflare API.

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.0/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=ready pod -l app.kubernetes.io/instance=cert-manager --timeout=120s
```

Create a Secret with your Cloudflare API token (the same token used for the tunnel works if it has `Zone:DNS:Edit`):

```bash
kubectl create secret generic cloudflare-api-token \
  --namespace cert-manager \
  --from-literal=api-token="xxxxxxxxxx"
```

Create a ClusterIssuer that uses Cloudflare DNS01:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - dns01:
          cloudflare:
            apiTokenSecretRef:
              name: cloudflare-api-token
              key: api-token
EOF
```

Verify the issuer is ready:

```bash
kubectl get clusterissuer letsencrypt-prod
```

## 2. Install the Cloudflare Tunnel Ingress Controller

```bash
helm repo add strrl.dev https://helm.strrl.dev
helm repo update

helm upgrade --install --wait \
  -n cloudflare-tunnel-ingress-controller --create-namespace \
  cloudflare-tunnel-ingress-controller \
  strrl.dev/cloudflare-tunnel-ingress-controller \
  --set=cloudflare.apiToken="xxxxxxxxxx" \
  --set=cloudflare.accountId="xxxxxxxxxx" \
  --set=cloudflare.tunnelName="kedge-tunnel"
```

Verify the controller is running:

```bash
kubectl -n cloudflare-tunnel-ingress-controller get pods
```

## 3. Configure kedge-hub with Cloudflare ingress

When using Cloudflare Tunnel, use an external identity provider (see [idp.md](idp.md)) and enable ingress with the `cloudflare-tunnel` ingress class. Use cert-manager for TLS instead of self-signed certs.

Add the ingress section to your values file:

```yaml
hub:
  hubExternalURL: "https://hub.faros.sh"
  devMode: false
  tls:
    selfSigned:
      enabled: false
    certManager:
      enabled: true
      issuerRef:
        name: letsencrypt-prod
        kind: ClusterIssuer
      dnsNames:
        - "hub.faros.sh"

idp:
  issuerURL: "https://idp.faros.sh"
  clientID: "kedge"
  clientSecret: "<your-idp-client-secret>"

ingress:
  enabled: true
  className: "cloudflare-tunnel"
  hosts:
    - host: hub.faros.sh
      paths:
        - path: /
          pathType: ImplementationSpecific
```

## 4. Install kedge-hub

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values-production.yaml \
  --namespace kedge-system \
  --create-namespace
```

## 5. Verify the tunnel

Once the pods are running, Cloudflare should automatically create DNS records pointing to the tunnel:

```bash
# Check the ingress has an address
kubectl get ingress -A

# Expected output:
# NAMESPACE      NAME              CLASS               HOSTS           ADDRESS                                                 PORTS     AGE
# kedge-system   kedge-kedge-hub   cloudflare-tunnel   hub.faros.sh    a1fa66c5-7766-40e7-87fd-9d42391f07da.cfargotunnel.com   80, 443   5m

# Test connectivity
curl -s https://hub.faros.sh/healthz
```

## DNS configuration

The Cloudflare Tunnel Ingress Controller automatically manages CNAME records pointing to the tunnel. You don't need to manually create DNS records. When the Ingress resource is created, the controller:

1. Creates a route in the tunnel for the specified hostname
2. Creates a CNAME record `hub.faros.sh -> <tunnel-id>.cfargotunnel.com`

## Multiple services

You can expose both the hub and Dex through the same tunnel by creating separate Ingress resources. If running Dex in the same cluster (see [idp.md](idp.md)), its Ingress uses the same `cloudflare-tunnel` class with a different hostname (e.g., `idp.faros.sh`).

## Troubleshooting

Check the tunnel controller logs:

```bash
kubectl -n cloudflare-tunnel-ingress-controller logs -l app.kubernetes.io/name=cloudflare-tunnel-ingress-controller
```

Verify the tunnel is connected in the Cloudflare dashboard under **Zero Trust > Networks > Tunnels**.

Check cert-manager certificate status:

```bash
kubectl -n kedge-system get certificate,certificaterequest,order,challenge
```

Common issues:
- **API token permissions** — the token needs `Account:Cloudflare Tunnel:Edit` and `Zone:DNS:Edit` permissions. The same token is used by both the tunnel controller and cert-manager.
- **Certificate not issuing** — check `kubectl describe certificate -n kedge-system` and cert-manager logs (`kubectl -n cert-manager logs -l app=cert-manager`). DNS01 challenges create TXT records under `_acme-challenge.<domain>`.
- **Tunnel name conflicts** — if a tunnel with the same name already exists, the controller will reuse it. Delete stale tunnels from the dashboard if needed.
- **DNS propagation** — new CNAME records may take a few minutes to propagate.
