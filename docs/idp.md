---
layout: default
title: Identity Provider
nav_order: 4
description: "Configure Dex as an external OIDC identity provider for Kedge"
---

# Identity Provider
{: .no_toc }

Deploy Dex as a standalone OIDC identity provider for Kedge.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

This guide deploys Dex using the upstream [dex Helm chart](https://github.com/dexidp/helm-charts), separate from the kedge-hub chart. This is the recommended approach for production deployments where you want:

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

- A running Kubernetes cluster with ingress configured (see [Ingress Setup]({% link ingress.md %}))
- A GitHub OAuth app (or other identity connector)

---

## Installation

### 1. Add the Dex Helm repository

```bash
helm repo add dex https://charts.dexidp.io
helm repo update
```

### 2. Create the Dex data PVC

The upstream dex Helm chart does not include built-in persistence. Create a PVC manually:

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

### 3. Create a Dex values file

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
   # cert-manager will create and populate this secret
   - secretName: dex-tls
     hosts:
       - auth.faros.sh
```

{: .important }
**Key configuration points:**
- **`volumes` + `volumeMounts`** — The upstream chart has no built-in persistence. We inject our PVC manually.
- **`web.http`** — Dex listens on plain HTTP. TLS is terminated at the ingress.
- **`staticClients`** — The `id` must match `idp.clientID` in kedge-hub, and `redirectURIs` must include the hub's callback endpoint.

### 4. Deploy Dex

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

### 5. Verify the deployment

If Cloudflare Tunnel is set up correctly:

```bash
kubectl get ingress -A
# NAMESPACE   NAME   CLASS               HOSTS          ADDRESS                                                 PORTS     AGE
# dex         dex    cloudflare-tunnel   idp.faros.sh   a1fa66c5-7766-40e7-87fd-9d42391f07da.cfargotunnel.com   80, 443   5m
```

Check the OIDC discovery endpoint:

```bash
curl -s https://idp.faros.sh/.well-known/openid-configuration | head -20
```

---

## Identity Connectors

Dex supports many identity providers. Here's how to configure common ones.

### GitHub OAuth

1. Go to **GitHub > Settings > Developer settings > OAuth Apps > New OAuth App**
2. Configure:
   - **Application name:** Kedge Auth
   - **Homepage URL:** `https://api.example.com`
   - **Authorization callback URL:** `https://auth.example.com/callback`
3. Copy the **Client ID** and generate a **Client Secret**
4. Add to your `dex-values.yaml`:

```yaml
config:
  connectors:
    - type: github
      id: github
      name: GitHub
      config:
        clientID: <github-client-id>
        clientSecret: <github-client-secret>
        redirectURI: https://idp.faros.sh/callback
        # Optional: restrict to an organization
        # org: your-org
```

{: .note }
To restrict access to a GitHub organization, set `org: your-org` in the connector config.

### Google

```yaml
config:
  connectors:
    - type: google
      id: google
      name: Google
      config:
        clientID: <google-client-id>
        clientSecret: <google-client-secret>
        redirectURI: https://idp.faros.sh/callback
        # Optional: restrict to a hosted domain
        # hostedDomains:
        #   - example.com
```

### LDAP

```yaml
config:
  connectors:
    - type: ldap
      id: ldap
      name: LDAP
      config:
        host: ldap.example.com:636
        insecureNoSSL: false
        bindDN: cn=admin,dc=example,dc=com
        bindPW: admin-password
        userSearch:
          baseDN: ou=users,dc=example,dc=com
          filter: "(objectClass=person)"
          username: uid
          idAttr: uid
          emailAttr: mail
          nameAttr: cn
        groupSearch:
          baseDN: ou=groups,dc=example,dc=com
          filter: "(objectClass=groupOfNames)"
          userAttr: DN
          groupAttr: member
          nameAttr: cn
```

---

## Troubleshooting

### Check Dex logs

```bash
kubectl -n dex logs -l app.kubernetes.io/name=dex
```

### Common issues

| Issue | Solution |
|:------|:---------|
| **"invalid issuer"** | The `issuer` URL in Dex config must exactly match `idp.issuerURL` in kedge-hub, including scheme and path |
| **sqlite3 data lost on restart** | Verify the PVC is bound and `volumeMounts` maps `/var/dex` correctly. Check with `kubectl -n dex get pvc dex-data` |
| **Callback URL mismatch** | The `redirectURIs` in `staticClients` must exactly match `<hubExternalURL>/auth/callback` |
| **Certificate errors** | Ensure cert-manager is issuing certificates correctly. Check with `kubectl -n dex get certificate,certificaterequest` |

### Verify PVC is bound

```bash
kubectl -n dex get pvc dex-data
# NAME       STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
# dex-data   Bound    pvc-xxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx      1Gi        RWO            standard       10m
```

### Check OIDC discovery

```bash
curl -s https://idp.faros.sh/.well-known/openid-configuration | jq .
```

This should return a JSON document with the issuer URL, authorization endpoint, token endpoint, and other OIDC metadata.
