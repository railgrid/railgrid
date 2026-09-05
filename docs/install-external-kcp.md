---
layout: default
title: Install — Multi-Shard kcp
nav_order: 6
description: "Install faros hub against a two-shard kcp deployed with the kcp-operator, exposed via Gateway API, with optional Cloudflare DNS"
---

# Install: faros hub with a multi-shard kcp
{: .no_toc }

A production-shaped installation: a two-shard kcp deployed by the
kcp-operator, one shared etcd, everything exposed through a Gateway API
(Envoy) TLS-passthrough listener, and the faros hub running stateless against
that kcp. Optionally published in Cloudflare DNS.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## How this guide stays correct

Every step below is a script in [`hack/install/`](https://github.com/faroshq/faros/tree/main/hack/install),
and the e2e suite `make e2e-install-external` runs those scripts **verbatim**
against a fresh kind cluster on every change. If a step in this guide drifted
from reality, CI would fail. The scripts and this page must be changed
together.

All knobs are environment variables with defaults (see `hack/install/lib.sh`):

| Variable | Default | Meaning |
|:---------|:--------|:--------|
| `FAROS_INSTALL_CLUSTER` | `faros` | kind cluster name |
| `KCP_DOMAIN` | `kcp.localhost` | kcp base domain (front-proxy hostname) |
| `KCP_SHARD_2` | `theseus` | name of the second shard |
| `KCP_GATEWAY_IP` | `10.96.2.2` | fixed ClusterIP the gateway claims |
| `FAROS_STATIC_TOKEN` | random, saved to `.faros-install/hub-token` | shared static bearer token (generated and printed by the first script you run; set it to bring your own) |
| `HUB_DOMAIN` | `faros.kcp.localhost` | hub hostname on the gateway |
| `HUB_EXTERNAL_URL` | `https://localhost:9443` | URL baked into kubeconfigs |
| `HUB_REPLICAS` | `2` | hub Deployment replicas |

The default `*.localhost` domains resolve to loopback on Linux and macOS, so
the local flow needs no `/etc/hosts` edits — access goes through two
port-forwards (`hack/install/port-forward.sh`).

### Architecture

```
                    you / faros CLI / browser
                          │ :8443 (SNI)              │ :9443
                          ▼                          ▼
              ┌──────────────────────┐      (local port-forward,
              │  Envoy Gateway       │       prod: same gateway)
              │  TLS passthrough     │
              └──┬───────┬───────┬───┴───────────┐
      SNI:       │       │       │               │
  kcp.localhost  │  root.…  theseus.…     faros.kcp.localhost
                 ▼       ▼       ▼               ▼
          ┌───────────┐ ┌─────┐ ┌────────┐ ┌───────────┐
          │front-proxy│ │root │ │theseus │ │ faros-hub │
          │           │ │shard│ │ shard  │ │ (2 repl.) │
          └─────┬─────┘ └──┬──┘ └───┬────┘ └─────┬─────┘
                └── kcp ───┴────────┘            │
                       │    │                    │
                       ▼    ▼          kcp-frontproxy-admin
                  ┌──────────────┐        kubeconfig
                  │ shared etcd  │
                  │ /shard/root  │
                  │ /shard/theseus│
                  └──────────────┘
```

---

## Prerequisites

| Tool | Purpose |
|:-----|:--------|
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | local Kubernetes cluster (any conformant cluster works) |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | applying manifests |
| [Helm](https://helm.sh/docs/intro/install/) v3+ | Envoy Gateway, faros-hub chart |
| [Docker](https://docs.docker.com/get-docker/) | kind's runtime |
| `openssl` | deriving the kcp static-token identity |

Clone the repo — the hub chart is installed from `deploy/charts/faros-hub`.

---

## Step 1 — create the cluster

```bash
hack/install/01-kind-cluster.sh
```

which is:

```bash
kind create cluster --name faros
kubectl cluster-info --context kind-faros
```

Using a managed/other cluster instead: skip this step and point the scripts at
your context (they use `kind-${FAROS_INSTALL_CLUSTER}`).

## Step 2 — cert-manager + self-signed issuer

The kcp-operator issues all kcp certificates (serving certs, client CAs, admin
kubeconfig client certs) through cert-manager.

```bash
hack/install/02-cert-manager.sh
```

which is:

```bash
kubectl apply --server-side -f \
  https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=Available deployment --all --timeout=5m

kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
  namespace: default
spec:
  selfSigned: {}
EOF
```

The Issuer lives in `default` — the same namespace as the kcp custom resources
in step 6, which reference it by name.

## Step 3 — Envoy Gateway (Gateway API)

One TLS-**passthrough** listener on port 8443 serves the whole stack by SNI:
the kcp front-proxy, both shards, and (step 7) the hub. Backends keep
terminating their own TLS, which preserves kcp client-cert auth and the hub's
WebSocket agent tunnels.

```bash
hack/install/03-envoy-gateway.sh
```

which is:

```bash
helm upgrade --install envoy oci://registry-1.docker.io/envoyproxy/gateway-helm \
  --version v1.7.0 --namespace envoy-gateway-system --create-namespace --wait

kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: eg
  namespace: envoy-gateway-system
spec:
  addresses:            # fixed ClusterIP so in-cluster pods can hostAlias
    - type: IPAddress   # kcp.localhost → the gateway. Remove in production —
      value: 10.96.2.2  # the cloud LoadBalancer assigns the address there.
  gatewayClassName: eg
  listeners:
    - name: passthrough
      protocol: TLS
      port: 8443
      tls:
        mode: Passthrough
      allowedRoutes:
        namespaces:
          from: All
EOF
```

## Step 4 — etcd

One shared etcd backs **both** shards: each shard isolates its keyspace with
its own `--etcd-prefix` (`/shard/root`, `/shard/theseus`), the same layout the
upstream kcp development stack uses.

```bash
hack/install/04-etcd.sh
```

The script applies a `kcp-etcd` namespace with a single-member etcd
StatefulSet (headless Service `etcd`, 8Gi PVC, raised backend quota) and waits
for it to roll out. Endpoint: `http://etcd.kcp-etcd.svc.cluster.local:2379`.

{: .warning }
This etcd is plaintext and single-member — fine for dev/CI. In production run
a 3-member TLS cluster (etcd-druid, a chart, or managed etcd) and point the
shard specs in step 6 at its endpoint.

## Step 5 — kcp-operator

The operator reconciles `RootShard` / `Shard` / `FrontProxy` / `Kubeconfig`
custom resources into running kcp deployments.

```bash
hack/install/05-kcp-operator.sh
```

which applies the upstream `config/default` kustomization with the published
image tag overlaid (the upstream base references an unpublished dev tag):

```bash
kubectl apply --server-side -k <<kustomization>>   # see the script
# resources:
#   - https://github.com/kcp-dev/kcp-operator/config/default?ref=main
# images:
#   - name: ghcr.io/kcp-dev/kcp-operator
#     newTag: main
kubectl -n kcp-operator-system rollout status deploy/kcp-operator-controller-manager --timeout=5m
```

Pin a release by setting `KCP_OPERATOR_REF` (and `KCP_OPERATOR_TAG` if the
image tag differs from the git ref).

## Step 6 — kcp: two shards + front-proxy

```bash
hack/install/06-kcp-shards.sh
```

This is the heart of the install. The script applies (all in namespace
`default`; full YAML in the script):

1. **`Secret kcp-static-tokens`** — a `--token-auth-file` CSV mapping
   the static token (`$FAROS_STATIC_TOKEN`, from `.faros-install/hub-token`)
   to the identity the faros hub derives from the same token
   (`faros:static:<first 16 hex of sha256("static-token/<token>")>`). Both
   the shards *and* the front-proxy get this file via `spec.auth.tokenAuthFile`
   — the front-proxy authenticates first, so wiring only the shards would 401.
2. **`RootShard root`** — etcd prefix `/shard/root`, shard base URL
   `https://root.kcp.localhost:8443`, certificates from the `selfsigned`
   Issuer, embedded cache server, and `hostAliases` pointing every kcp
   hostname at the gateway ClusterIP (kcp's own controllers call back into the
   advertised shard URLs, so the names must resolve in-cluster too).
3. **`Shard theseus`** — same shape, etcd prefix `/shard/theseus`, base URL
   `https://theseus.kcp.localhost:8443`.
4. **`FrontProxy frontproxy`** — the client-facing entrypoint at
   `https://kcp.localhost:8443`, routing requests to the right shard.
5. **Three `TLSRoute`s** — SNI `kcp.localhost` → `frontproxy-front-proxy:8443`,
   `root.kcp.localhost` → `root-kcp:6443`, `theseus.kcp.localhost` →
   `theseus-shard-kcp:6443`.
6. **Three `Kubeconfig`s** — the operator mints admin kubeconfigs
   (client-cert) for the front-proxy and each shard. The front-proxy one
   additionally carries the `system:kcp:admin` group because the proxy strips
   `system:masters` on ingress.

The script then waits for everything and extracts the kubeconfigs to
`.faros-install/`:

```
.faros-install/kcp-frontproxy.kubeconfig   ← use this one
.faros-install/kcp-root.kubeconfig
.faros-install/kcp-theseus.kubeconfig
```

### Verify

```bash
hack/install/port-forward.sh start

kubectl --kubeconfig .faros-install/kcp-frontproxy.kubeconfig get workspaces
kubectl --kubeconfig .faros-install/kcp-root.kubeconfig get shards
```

`get shards` (against the root shard) must list **two** shards, `root` and
`theseus`, both `Ready`.

## Step 7 — faros hub (external kcp)

```bash
hack/install/07-faros-hub-external.sh
```

which is, in essence:

```bash
kubectl create namespace faros-system

# the hub mounts the front-proxy admin kubeconfig from a Secret
kubectl create secret generic kcp-frontproxy-admin -n faros-system \
  --from-file=admin.kubeconfig=.faros-install/kcp-frontproxy.kubeconfig

# dev-grade RBAC: the hub installs CRDs into its own cluster
kubectl create clusterrolebinding faros-hub-cluster-admin \
  --clusterrole=cluster-admin --serviceaccount=faros-system:default

helm upgrade --install faros-hub deploy/charts/faros-hub \
  --namespace faros-system \
  --set replicaCount=2 \
  --set kcp.embedded.enabled=false \
  --set kcp.external.enabled=true \
  --set kcp.external.existingSecret=kcp-frontproxy-admin \
  --set hub.hubExternalURL=https://localhost:9443 \
  --set hub.devMode=true \
  --set hub.embeddedGraphQL=true \
  --set "hub.staticAuthTokens={$(cat .faros-install/hub-token)}" \
  --set 'hub.tls.selfSigned.dnsNames={faros.kcp.localhost}' \
  --set hostAliases[0].ip=10.96.2.2 \
  --set hostAliases[0].hostnames[0]=kcp.localhost \
  --set hostAliases[0].hostnames[1]=root.kcp.localhost \
  --set hostAliases[0].hostnames[2]=theseus.kcp.localhost \
  --wait
```

plus a `TLSRoute` attaching the hub to the same gateway at SNI
`faros.kcp.localhost` → `faros-hub:9443`.

With external kcp the chart renders a stateless **Deployment** (here: 2
replicas). Every replica serves the full request surface — singleton
controllers coordinate through a kcp-backed Lease, sessions live in kcp-backed
Secrets — so no ingress session affinity is needed (see
[Helm Deployment]({% link helm.md %}) for details).

### Verify

```bash
hack/install/port-forward.sh start

curl -k https://localhost:9443/healthz            # → ok
curl -k --resolve faros.kcp.localhost:8443:127.0.0.1 \
  https://faros.kcp.localhost:8443/healthz              # hub via the gateway

faros login --hub-url https://localhost:9443 \
  --token "$(cat .faros-install/hub-token)" --insecure-skip-tls-verify
kubectl get organizations
```

The token is the random one the first install script generated and printed
(`.faros-install/hub-token`); if you exported `FAROS_STATIC_TOKEN` yourself,
use that value instead.

---

## Production: Cloudflare DNS

{: .note }
This section targets real clusters with a real domain. It is **not** covered
by the e2e suite (it needs a Cloudflare zone and API token); everything above
this line is.

On a cloud cluster the Envoy gateway Service is a `LoadBalancer`. Three
changes publish the stack at, e.g., `kcp.example.com` / `hub.example.com`:

1. **Real hostnames.** Re-run the flow with:

   ```bash
   export KCP_DOMAIN=kcp.example.com
   export HUB_DOMAIN=hub.example.com
   export HUB_EXTERNAL_URL=https://hub.example.com:8443
   ```

   Remove `spec.addresses` from the Gateway in step 3 (the LoadBalancer
   assigns the address) and drop the `hostAliases` blocks — public DNS
   resolves in-cluster too.

2. **external-dns with the Cloudflare provider:**

   ```bash
   export CLOUDFLARE_API_TOKEN=...   # Zone:Read + DNS:Edit
   hack/install/09-cloudflare-dns.sh
   ```

   This installs [external-dns](https://kubernetes-sigs.github.io/external-dns/)
   watching Gateway API routes (`--source=gateway-tlsroute,gateway-httproute`):
   every hostname on a route attached to the gateway — `kcp.example.com`,
   `root.kcp.example.com`, `theseus.kcp.example.com`, `hub.example.com` — gets
   a DNS record pointing at the gateway's LoadBalancer address.

   Keep these records **DNS-only** (grey cloud, `upsert-only` policy does not
   proxy): the listener is TLS passthrough and kcp uses client certificates,
   which Cloudflare's HTTP proxy would break.

3. **Browser-trusted TLS for the hub (optional).** kcp clients trust the
   operator CA via the extracted kubeconfigs, so kcp can stay on self-signed
   certs. For the hub UI, issue a real certificate with cert-manager's ACME
   `DNS-01` solver against the same Cloudflare token, and hand it to the
   chart:

   ```yaml
   hub:
     tls:
       certManager:
         enabled: true
         issuerRef:
           name: letsencrypt-prod
           kind: ClusterIssuer
         dnsNames:
           - hub.example.com
   ```

---

## Teardown

```bash
hack/install/teardown.sh    # stops port-forwards, deletes the kind cluster and state
```

---

## e2e coverage

`make e2e-install-external` runs scripts `01`–`07` plus the port-forwards and
then asserts:

- both shards are `Ready` (via the root-shard kubeconfig),
- kcp answers through the gateway (front-proxy kubeconfig),
- the hub is healthy at `https://localhost:9443` **and** through the
  gateway SNI route,
- `faros login` with the static token works and the tenancy API (organization
  and workspace CRUD) functions end-to-end.

For the embedded-kcp variant of this install, see
[Install — Embedded kcp]({% link install-embedded-kcp.md %}).
