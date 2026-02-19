---
layout: default
title: Security
nav_order: 3
description: "Authentication options for Kedge — static tokens and OIDC"
---

# Security
{: .no_toc }

Configure authentication for your Kedge hub.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

Kedge supports two authentication methods:

| Method | Use Case | Complexity |
|:-------|:---------|:-----------|
| **Static Token** | Personal home labs, development, CI/CD | Simple |
| **OIDC (Dex)** | Teams, production, audit requirements | More setup |

For a single-user home lab, static tokens are the easiest option. For teams or when you need proper user management, use OIDC.

---

## Static Token Authentication

Static tokens are the simplest way to secure your hub. A pre-shared token grants full admin access — no identity provider needed.

### When to Use

- Personal home lab with a single user
- Development and testing
- CI/CD pipelines
- Quick deployments without OIDC infrastructure

### Setup

#### 1. Generate a token

```bash
openssl rand -hex 32
```

Save this token securely.

#### 2. Configure the hub

Add the token to your Helm values:

```yaml
hub:
  hubExternalURL: "https://hub.example.com"
  devMode: true  # Skip OIDC TLS verification
  staticAuthToken: "<your-generated-token>"

# No idp section needed
```

Or pass it directly when running the binary:

```bash
kedge-hub \
  --static-auth-token=<your-generated-token> \
  --hub-external-url=https://localhost:8443 \
  --dev-mode
```

#### 3. Log in with the token

```bash
kedge login \
  --hub-url https://hub.example.com \
  --token <your-generated-token> \
  --insecure-skip-tls-verify  # Only if using self-signed certs
```

This writes a kubeconfig context named `kedge` with the token embedded.

#### 4. Verify

```bash
kubectl --context=kedge get namespaces
```

{: .warning }
**Security notes:**
- The static token grants full admin access to the hub
- Store the token securely (password manager, encrypted file)
- Rotate the token periodically by updating the hub config and re-authenticating
- Use OIDC for production or multi-user scenarios

---

## OIDC Authentication (Dex)

For teams or production deployments, use OIDC authentication with [Dex](https://dexidp.io/) as the identity provider. Dex supports many backends:

- GitHub / GitLab
- Google Workspace
- LDAP / Active Directory
- SAML providers

### Architecture

```
Browser --> Hub --> Dex --> Identity Backend (GitHub, Google, LDAP, etc.)
   │                           │
   └──── OIDC redirect ────────┘
```

### Prerequisites

- A publicly reachable URL for Dex (or same-cluster access)
- An identity backend (GitHub OAuth app, Google credentials, etc.)

### 1. Deploy Dex

Add the Dex Helm repository:

```bash
helm repo add dex https://charts.dexidp.io
helm repo update
```

Create a PVC for persistent storage:

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

Create a values file (`dex-values.yaml`):

```yaml
config:
  issuer: https://idp.example.com

  storage:
    type: sqlite3
    config:
      file: /var/dex/dex.db

  web:
    http: 0.0.0.0:5556

  staticClients:
    - id: kedge
      name: Kedge
      secret: "<generate-a-secret>"
      redirectURIs:
        - https://hub.example.com/auth/callback

  connectors:
    - type: github
      id: github
      name: GitHub
      config:
        clientID: <github-client-id>
        clientSecret: <github-client-secret>
        redirectURI: https://idp.example.com/callback
        # Optional: restrict to an organization
        # org: your-org

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
  className: "your-ingress-class"
  hosts:
    - host: idp.example.com
      paths:
        - path: /
          pathType: Prefix
```

Deploy Dex:

```bash
helm upgrade --install dex dex/dex \
  --namespace dex \
  --create-namespace \
  -f dex-values.yaml
```

Verify:

```bash
curl -s https://idp.example.com/.well-known/openid-configuration | head -5
```

### 2. Configure the Hub

Update your Helm values to use OIDC:

```yaml
hub:
  hubExternalURL: "https://hub.example.com"
  devMode: false

idp:
  issuerURL: "https://idp.example.com"
  clientID: "kedge"
  clientSecret: "<same-secret-as-in-dex>"
```

Deploy or upgrade the hub:

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f values.yaml \
  --namespace kedge-system \
  --create-namespace
```

### 3. Log in

```bash
kedge login --hub-url https://hub.example.com
```

This opens a browser for the OIDC flow. After authenticating with your identity provider, you're redirected back and your kubeconfig is configured.

---

## Identity Connectors

Dex supports many identity providers. Here are common configurations.

### GitHub

1. Create an OAuth App: **GitHub > Settings > Developer settings > OAuth Apps > New OAuth App**
2. Set the callback URL to `https://idp.example.com/callback`
3. Add to Dex config:

```yaml
connectors:
  - type: github
    id: github
    name: GitHub
    config:
      clientID: <github-client-id>
      clientSecret: <github-client-secret>
      redirectURI: https://idp.example.com/callback
      # Restrict to organization members
      org: your-org
```

### Google

1. Create credentials in the [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Add to Dex config:

```yaml
connectors:
  - type: google
    id: google
    name: Google
    config:
      clientID: <google-client-id>
      clientSecret: <google-client-secret>
      redirectURI: https://idp.example.com/callback
      # Restrict to a domain
      hostedDomains:
        - example.com
```

### LDAP

```yaml
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
```

---

## Troubleshooting

### "invalid issuer" error

The `issuer` URL in Dex config must exactly match `idp.issuerURL` in the hub config, including scheme (`https://`) and no trailing slash.

### Callback URL mismatch

The `redirectURIs` in Dex's `staticClients` must exactly match `<hubExternalURL>/auth/callback`.

### Certificate errors

If using self-signed certificates, set `hub.devMode: true` to skip TLS verification for the OIDC issuer.

### Check Dex logs

```bash
kubectl -n dex logs -l app.kubernetes.io/name=dex
```

### Verify OIDC discovery

```bash
curl -s https://idp.example.com/.well-known/openid-configuration | jq .
```

---

## Choosing an Approach

| Scenario | Recommendation |
|:---------|:---------------|
| Single user, home lab | Static token |
| Family/friends sharing | Static token with care, or OIDC |
| Small team | OIDC with GitHub/Google |
| Enterprise | OIDC with LDAP/SAML |
| CI/CD automation | Static token (scoped to CI) |
