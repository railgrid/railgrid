---
layout: default
title: Ingress Setup
nav_order: 5
description: "Expose Kedge Hub via Cloudflare Tunnel"
---

# Ingress Setup
{: .no_toc }

Set up public ingress for Kedge Hub using Cloudflare Tunnel.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) exposes cluster services to the internet without requiring a public IP or cloud LoadBalancer. This is particularly useful for:

- Home labs
- Edge deployments
- kind clusters
- Environments without public IPs

## How It Works

The Cloudflare Tunnel Ingress Controller runs inside the cluster and establishes an outbound connection to Cloudflare's edge network. The tunnel operates in **passthrough** mode — TLS is **not** terminated at Cloudflare's edge. Traffic flows encrypted end-to-end:

```
Browser/CLI --TLS--> Cloudflare Edge --passthrough--> Tunnel --> k8s Service --> Hub (TLS on :8443)
```

## Prerequisites

| Requirement | Description |
|:------------|:------------|
| Cloudflare account | With a domain configured |
| Cloudflare API token | With `Account:Cloudflare Tunnel:Edit` and `Zone:DNS:Edit` permissions |
| Account ID | Found in the Cloudflare dashboard URL |
| Tunnel name | Will be auto-created if it doesn't exist |

---

## Installation

### 1. Install cert-manager

The hub serves TLS directly (no TLS termination at the tunnel). cert-manager issues certificates via Cloudflare DNS01 challenges:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.0/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=ready pod -l app.kubernetes.io/instance=cert-manager --timeout=120s
```

{: .note }
DNS01 challenges work without the app being publicly reachable — cert-manager validates domain ownership by creating DNS TXT records through the Cloudflare API.

### 2. Configure Cloudflare API token

Create a Secret with your Cloudflare API token:

```bash
kubectl create secret generic cloudflare-api-token \
  --namespace cert-manager \
  --from-literal=api-token="<your-cloudflare-api-token>"
```

### 3. Create a ClusterIssuer

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

### 4. Install the Cloudflare Tunnel Ingress Controller

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

Verify the controller is running:

```bash
kubectl -n cloudflare-tunnel-ingress-controller get pods
```

---

## Configure Kedge Hub

Update your kedge-hub values file to use the Cloudflare Tunnel ingress and cert-manager for TLS:

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

### Install kedge-hub

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values-production.yaml \
  --namespace kedge-system \
  --create-namespace
```

---

## Verify the Setup

### Check Ingress status

```bash
kubectl get ingress -A
```

Expected output:

```
NAMESPACE      NAME              CLASS               HOSTS           ADDRESS                                                 PORTS     AGE
kedge-system   kedge-kedge-hub   cloudflare-tunnel   hub.faros.sh    a1fa66c5-7766-40e7-87fd-9d42391f07da.cfargotunnel.com   80, 443   5m
```

### Test connectivity

```bash
curl -s https://hub.faros.sh/healthz
```

---

## DNS Configuration

The Cloudflare Tunnel Ingress Controller automatically manages DNS records. You don't need to manually create them.

When an Ingress resource is created, the controller:

1. Creates a route in the tunnel for the specified hostname
2. Creates a CNAME record: `hub.faros.sh -> <tunnel-id>.cfargotunnel.com`

---

## Multiple Services

You can expose both the hub and Dex through the same tunnel by creating separate Ingress resources.

If running Dex in the same cluster (see [Identity Provider]({% link idp.md %})), its Ingress uses the same `cloudflare-tunnel` class with a different hostname:

```yaml
# Dex ingress
ingress:
  enabled: true
  className: "cloudflare-tunnel"
  hosts:
    - host: idp.faros.sh
      paths:
        - path: /
          pathType: ImplementationSpecific
```

---

## Troubleshooting

### Check tunnel controller logs

```bash
kubectl -n cloudflare-tunnel-ingress-controller logs -l app.kubernetes.io/name=cloudflare-tunnel-ingress-controller
```

### Verify tunnel in Cloudflare dashboard

Go to **Zero Trust > Networks > Tunnels** to see if the tunnel is connected.

### Check cert-manager certificates

```bash
kubectl -n kedge-system get certificate,certificaterequest,order,challenge
```

For detailed certificate status:

```bash
kubectl describe certificate -n kedge-system
```

### Common issues

| Issue | Solution |
|:------|:---------|
| **API token permissions** | Token needs `Account:Cloudflare Tunnel:Edit` and `Zone:DNS:Edit` permissions |
| **Certificate not issuing** | Check cert-manager logs: `kubectl -n cert-manager logs -l app=cert-manager`. DNS01 challenges create TXT records under `_acme-challenge.<domain>` |
| **Tunnel name conflicts** | If a tunnel with the same name exists, the controller reuses it. Delete stale tunnels from the dashboard |
| **DNS propagation** | New CNAME records may take a few minutes to propagate |

### Check cert-manager logs

```bash
kubectl -n cert-manager logs -l app=cert-manager
```

### Verify DNS records

```bash
dig hub.faros.sh CNAME
```

Should return a CNAME pointing to `<tunnel-id>.cfargotunnel.com`.
