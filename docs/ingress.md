---
layout: default
title: Ingress
nav_order: 4
description: "Expose Kedge Hub publicly for remote agents"
---

# Ingress
{: .no_toc }

Make your hub reachable from anywhere so remote agents can connect.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

Remote agents need to reach your hub over the internet. This guide covers the most common approach for home labs: **Cloudflare Tunnel**.

### Why Cloudflare Tunnel?

| Challenge | Cloudflare Tunnel Solution |
|:----------|:---------------------------|
| No public IP | Tunnel connects outbound — no inbound ports needed |
| Dynamic IP | DNS managed automatically |
| NAT/CGNAT | Works through any NAT |
| TLS certificates | Free certs via Let's Encrypt + DNS validation |
| DDoS protection | Built-in |

This is the recommended approach for home labs where you don't have a static public IP or don't want to expose ports.

---

## How It Works

```
Remote Agent                 Cloudflare Edge              Your Cluster
     │                             │                           │
     │ ──── TLS ─────────────────▶ │                           │
     │                             │ ◀── tunnel (outbound) ─── │
     │                             │                           │
     └─────────────────────────────┴───────────────────────────┘
         Traffic flows through Cloudflare to your hub
```

The tunnel runs inside your cluster and connects **outbound** to Cloudflare. No port forwarding or firewall rules needed.

---

## Prerequisites

| Requirement | Description |
|:------------|:------------|
| Cloudflare account | Free tier works |
| Domain on Cloudflare | Your domain's DNS must be managed by Cloudflare |
| API token | With `Cloudflare Tunnel:Edit` and `DNS:Edit` permissions |
| Account ID | Found in the Cloudflare dashboard sidebar |

### Create a Cloudflare API Token

1. Go to [Cloudflare Dashboard](https://dash.cloudflare.com) → **My Profile** → **API Tokens**
2. Click **Create Token**
3. Use **Custom token** with permissions:
   - `Account` → `Cloudflare Tunnel` → `Edit`
   - `Zone` → `DNS` → `Edit`
4. Copy the token

### Find Your Account ID

1. Go to [Cloudflare Dashboard](https://dash.cloudflare.com)
2. Select your domain
3. Account ID is in the right sidebar

---

## Step 1: Install cert-manager

cert-manager issues TLS certificates via Cloudflare DNS validation:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.0/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=ready pod -l app.kubernetes.io/instance=cert-manager --timeout=120s
```

---

## Step 2: Configure Cloudflare DNS Validation

Create a secret with your API token:

```bash
kubectl create secret generic cloudflare-api-token \
  --namespace cert-manager \
  --from-literal=api-token="<your-cloudflare-api-token>"
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

Verify it's ready:

```bash
kubectl get clusterissuer letsencrypt-prod
```

---

## Step 3: Install Cloudflare Tunnel Controller

```bash
helm repo add strrl.dev https://helm.strrl.dev
helm repo update

helm upgrade --install --wait \
  -n cloudflare-tunnel-ingress-controller --create-namespace \
  cloudflare-tunnel-ingress-controller \
  strrl.dev/cloudflare-tunnel-ingress-controller \
  --set=cloudflare.apiToken="<your-cloudflare-api-token>" \
  --set=cloudflare.accountId="<your-cloudflare-account-id>" \
  --set=cloudflare.tunnelName="kedge-tunnel"
```

Verify:

```bash
kubectl -n cloudflare-tunnel-ingress-controller get pods
```

Check in [Cloudflare Zero Trust](https://one.dash.cloudflare.com) → **Networks** → **Tunnels** — you should see `kedge-tunnel` as healthy.

---

## Step 4: Deploy the Hub with Ingress

Update your Helm values:

```yaml
hub:
  hubExternalURL: "https://hub.yourdomain.com"
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
        - "hub.yourdomain.com"

# Authentication (choose one)
hub:
  staticAuthToken: "<your-token>"  # For simple setup
# OR
idp:
  issuerURL: "https://idp.yourdomain.com"
  clientID: "kedge"
  clientSecret: "<secret>"

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
  -f values.yaml \
  --namespace kedge-system \
  --create-namespace
```

---

## Step 5: Verify

Check ingress status:

```bash
kubectl get ingress -n kedge-system
```

Expected output:

```
NAME              CLASS               HOSTS                ADDRESS                              PORTS     AGE
kedge-kedge-hub   cloudflare-tunnel   hub.yourdomain.com   xxxx.cfargotunnel.com               80, 443   5m
```

Test connectivity:

```bash
curl -s https://hub.yourdomain.com/healthz
```

Try logging in:

```bash
kedge login --hub-url https://hub.yourdomain.com
```

---

## Exposing Dex (OIDC)

If you're using OIDC authentication, Dex also needs to be publicly reachable. Add an ingress to Dex in its values:

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

Both services share the same tunnel — just different hostnames.

---

## Alternative: Port Forwarding

If you have a static IP and can forward ports, you can skip Cloudflare Tunnel:

1. Forward port 443 to your cluster's ingress controller
2. Use a standard ingress controller (nginx, traefik)
3. Point DNS to your public IP

This is simpler but requires:
- A static public IP (or dynamic DNS)
- Router access for port forwarding
- No CGNAT from your ISP

---

## Troubleshooting

### Tunnel not connecting

Check controller logs:

```bash
kubectl -n cloudflare-tunnel-ingress-controller logs -l app.kubernetes.io/name=cloudflare-tunnel-ingress-controller
```

Verify the tunnel in [Cloudflare Zero Trust](https://one.dash.cloudflare.com) → **Networks** → **Tunnels**.

### Certificate not issuing

Check cert-manager:

```bash
kubectl -n kedge-system get certificate,certificaterequest,order,challenge

# Detailed status
kubectl describe certificate -n kedge-system

# cert-manager logs
kubectl -n cert-manager logs -l app=cert-manager
```

DNS01 challenges create TXT records at `_acme-challenge.hub.yourdomain.com`. Verify:

```bash
dig _acme-challenge.hub.yourdomain.com TXT
```

### DNS not resolving

The tunnel controller auto-creates CNAME records. Verify:

```bash
dig hub.yourdomain.com CNAME
```

Should return something like `xxxx.cfargotunnel.com`.

### Common issues

| Issue | Solution |
|:------|:---------|
| API token permissions | Needs `Cloudflare Tunnel:Edit` and `DNS:Edit` |
| Wrong account ID | Double-check in the Cloudflare dashboard |
| Tunnel name conflict | Delete stale tunnels from the dashboard |
| Certificate stuck | Check cert-manager logs, ensure DNS is on Cloudflare |
| Slow certificate | DNS propagation can take a few minutes |

---

## Next Steps

Once your hub is publicly accessible:

1. Connect remote agents using the public URL
2. Set up [Security]({% link security.md %}) (static token or OIDC)
3. Deploy workloads to your distributed clusters
