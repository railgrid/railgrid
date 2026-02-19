---
layout: default
title: Cloudflare Tunnel
parent: Ingress
nav_order: 1
description: "Expose Kedge Hub via Cloudflare Tunnel"
---

# Cloudflare Tunnel
{: .no_toc }

Expose your hub to the internet without a public IP using Cloudflare Tunnel.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) creates a secure outbound connection from your cluster to Cloudflare's edge network. This is the recommended approach for home labs.

### Why Cloudflare Tunnel?

| Challenge | Solution |
|:----------|:---------|
| No public IP | Tunnel connects outbound — no inbound ports needed |
| Dynamic IP | DNS managed automatically by Cloudflare |
| NAT/CGNAT | Works through any NAT configuration |
| TLS certificates | Free certs via Let's Encrypt + DNS validation |
| DDoS protection | Built into Cloudflare's edge |
| Security | No exposed ports on your network |

### How It Works

```
Remote Agent → Cloudflare Edge ← Tunnel Pod (your cluster)
                    ↓
              Your Hub Service
```

1. A tunnel pod runs in your cluster
2. It establishes an **outbound** connection to Cloudflare
3. Cloudflare routes traffic through the tunnel to your services
4. No inbound firewall rules needed

---

## Prerequisites

| Requirement | Description |
|:------------|:------------|
| Cloudflare account | [Free tier](https://dash.cloudflare.com/sign-up) works |
| Domain on Cloudflare | Your domain's DNS must be managed by Cloudflare |
| API token | With `Cloudflare Tunnel:Edit` and `DNS:Edit` permissions |
| Account ID | Found in the Cloudflare dashboard sidebar |

### Create a Cloudflare API Token

1. Go to [Cloudflare Dashboard](https://dash.cloudflare.com) → **My Profile** → **API Tokens**
2. Click **Create Token**
3. Select **Create Custom Token**
4. Configure permissions:
   - `Account` → `Cloudflare Tunnel` → `Edit`
   - `Zone` → `DNS` → `Edit`
5. Set the zone to your domain (or All zones)
6. Click **Continue to summary** → **Create Token**
7. **Copy and save the token** — you won't see it again

### Find Your Account ID

1. Go to [Cloudflare Dashboard](https://dash.cloudflare.com)
2. Select any domain
3. Look in the right sidebar under **API** → **Account ID**
4. Copy the ID

---

## Step 1: Install cert-manager

cert-manager issues TLS certificates for your hub. We use DNS01 validation via Cloudflare, which works even before your service is publicly accessible.

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.0/cert-manager.yaml
```

Wait for it to be ready:

```bash
kubectl -n cert-manager wait --for=condition=ready pod -l app.kubernetes.io/instance=cert-manager --timeout=120s
```

---

## Step 2: Configure DNS Validation

Create a secret with your Cloudflare API token:

```bash
kubectl create secret generic cloudflare-api-token \
  --namespace cert-manager \
  --from-literal=api-token="YOUR_CLOUDFLARE_API_TOKEN"
```

Create a ClusterIssuer for Let's Encrypt:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
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
# NAME               READY   AGE
# letsencrypt-prod   True    30s
```

{: .note }
DNS01 challenges work by creating TXT records via the Cloudflare API. No public access to your service is required during certificate issuance.

---

## Step 3: Install Cloudflare Tunnel Controller

The tunnel ingress controller manages tunnels and DNS records automatically.

```bash
helm repo add strrl.dev https://helm.strrl.dev
helm repo update
```

Install the controller:

```bash
helm upgrade --install --wait \
  -n cloudflare-tunnel-ingress-controller --create-namespace \
  cloudflare-tunnel-ingress-controller \
  strrl.dev/cloudflare-tunnel-ingress-controller \
  --set=cloudflare.apiToken="YOUR_CLOUDFLARE_API_TOKEN" \
  --set=cloudflare.accountId="YOUR_CLOUDFLARE_ACCOUNT_ID" \
  --set=cloudflare.tunnelName="kedge-tunnel"
```

Verify it's running:

```bash
kubectl -n cloudflare-tunnel-ingress-controller get pods
# NAME                                                    READY   STATUS    RESTARTS   AGE
# cloudflare-tunnel-ingress-controller-xxxxxxxxxx-xxxxx   1/1     Running   0          30s
```

Check the tunnel in [Cloudflare Zero Trust](https://one.dash.cloudflare.com):
- Go to **Networks** → **Tunnels**
- You should see `kedge-tunnel` with status **Healthy**

---

## Step 4: Deploy the Hub

Create a values file for your hub (`values-cloudflare.yaml`):

```yaml
hub:
  hubExternalURL: "https://hub.yourdomain.com"
  devMode: false

  # Authentication - choose one:
  staticAuthToken: "YOUR_GENERATED_TOKEN"  # Simple option
  # OR use OIDC (see Security docs)

  tls:
    selfSigned:
      enabled: false
    certManager:
      enabled: true
      issuerRef:
        name: letsencrypt-prod
        kind: ClusterIssuer
      dnsNames:
        - "hub.yourdomain.com"

ingress:
  enabled: true
  className: "cloudflare-tunnel"
  hosts:
    - host: hub.yourdomain.com
      paths:
        - path: /
          pathType: ImplementationSpecific
```

Deploy:

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values-cloudflare.yaml \
  --namespace kedge-system \
  --create-namespace
```

---

## Step 5: Verify

### Check certificate status

```bash
kubectl -n kedge-system get certificate
# NAME                  READY   SECRET                AGE
# kedge-kedge-hub-tls   True    kedge-kedge-hub-tls   2m
```

If not ready, check the certificate request:

```bash
kubectl -n kedge-system describe certificaterequest
```

### Check ingress status

```bash
kubectl get ingress -n kedge-system
# NAME              CLASS               HOSTS                ADDRESS                              PORTS     AGE
# kedge-kedge-hub   cloudflare-tunnel   hub.yourdomain.com   xxxx.cfargotunnel.com               80, 443   5m
```

### Test connectivity

```bash
curl -s https://hub.yourdomain.com/healthz
# ok
```

### Log in

```bash
kedge login --hub-url https://hub.yourdomain.com
```

---

## Exposing Additional Services

### Dex (OIDC Identity Provider)

If using OIDC authentication, Dex also needs to be publicly accessible. Add to your Dex values:

```yaml
ingress:
  enabled: true
  className: "cloudflare-tunnel"
  hosts:
    - host: idp.yourdomain.com
      paths:
        - path: /
          pathType: ImplementationSpecific
```

Both services share the same tunnel — just different hostnames. The tunnel controller automatically creates DNS records for each.

### Multiple Hostnames

You can expose any number of services through the same tunnel:

```yaml
# Service A
ingress:
  className: "cloudflare-tunnel"
  hosts:
    - host: service-a.yourdomain.com

# Service B
ingress:
  className: "cloudflare-tunnel"
  hosts:
    - host: service-b.yourdomain.com
```

---

## Troubleshooting

### Tunnel not connecting

Check the controller logs:

```bash
kubectl -n cloudflare-tunnel-ingress-controller logs -l app.kubernetes.io/name=cloudflare-tunnel-ingress-controller
```

Common issues:
- **Invalid API token** — Regenerate with correct permissions
- **Wrong account ID** — Double-check in dashboard
- **Tunnel name conflict** — Delete stale tunnels from the Cloudflare dashboard

### Certificate not issuing

Check cert-manager logs:

```bash
kubectl -n cert-manager logs -l app=cert-manager
```

Check certificate status:

```bash
kubectl -n kedge-system describe certificate
kubectl -n kedge-system get certificaterequest,order,challenge
```

Common issues:
- **API token missing DNS:Edit** — Recreate with correct permissions
- **Wrong zone** — Token must have access to the domain's zone

### DNS not resolving

The tunnel controller creates CNAME records automatically. Verify:

```bash
dig hub.yourdomain.com CNAME
# Should return: xxxx.cfargotunnel.com
```

If missing, check the controller logs and ensure the ingress has an ADDRESS assigned.

### Slow certificate issuance

DNS propagation can take a few minutes. If stuck:

1. Check if the TXT record exists:
   ```bash
   dig _acme-challenge.hub.yourdomain.com TXT
   ```

2. For kind clusters on macOS, DNS resolution can be slow. Apply this workaround:
   ```bash
   kubectl -n cert-manager patch deployment cert-manager --type=json \
     -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers=1.1.1.1:53,8.8.8.8:53"},{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers-only"}]'
   ```

---

## Reference

### Cloudflare Resources

- [Cloudflare Tunnel Documentation](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
- [Cloudflare Zero Trust Dashboard](https://one.dash.cloudflare.com)
- [Tunnel Ingress Controller](https://github.com/STRRL/cloudflare-tunnel-ingress-controller)

### Related Guides

- [Security]({% link security.md %}) — Configure authentication
- [Helm Deployment]({% link helm.md %}) — Full Helm values reference
