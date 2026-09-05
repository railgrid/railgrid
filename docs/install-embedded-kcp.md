---
layout: default
title: Install — Embedded kcp
nav_order: 7
description: "Install faros hub with embedded kcp behind the same Gateway API setup, with optional Cloudflare DNS"
---

# Install: faros hub with embedded kcp
{: .no_toc }

The single-binary installation: the hub chart runs kcp (with its embedded
etcd) inside the release, exposed through the same Gateway API wiring as the
[multi-shard install]({% link install-external-kcp.md %}) — minus the
cert-manager / etcd / kcp-operator machinery.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## How this guide stays correct

Like the multi-shard guide, each step is a script in
[`hack/install/`](https://github.com/faroshq/faros/tree/main/hack/install)
and `make e2e-install-embedded` runs those scripts verbatim in CI. Change the
scripts and this page together.

### When to choose embedded kcp

| | Embedded (this guide) | [External multi-shard]({% link install-external-kcp.md %}) |
|:--|:--|:--|
| Workload kind | StatefulSet, **1 replica** (kcp etcd on a PVC) | Deployment, N replicas |
| Components | hub chart only | cert-manager, etcd, kcp-operator, 2 shards |
| Scaling / HA | no | yes |
| Good for | dev, small teams, edge boxes | production, shard-level scale-out |

---

## Prerequisites

Same as the multi-shard guide: kind, kubectl, Helm v3+, Docker, and a repo
checkout for `deploy/charts/faros-hub`.

## Step 1 — create the cluster

```bash
hack/install/01-kind-cluster.sh          # kind create cluster --name faros
```

## Step 2 — Envoy Gateway (optional but recommended)

The same TLS-passthrough gateway as the multi-shard install. Skip it if you
only ever access the hub via port-forward; keep it if you want the
production-shaped SNI routing and the Cloudflare DNS option.

```bash
hack/install/03-envoy-gateway.sh
```

## Step 3 — faros hub (embedded kcp)

```bash
hack/install/08-faros-hub-embedded.sh
```

which is, in essence:

```bash
kubectl create namespace faros-system

helm upgrade --install faros-hub deploy/charts/faros-hub \
  --namespace faros-system \
  --set hub.hubExternalURL=https://localhost:9443 \
  --set hub.devMode=true \
  --set hub.embeddedGraphQL=true \
  --set "hub.staticAuthTokens={$(cat .faros-install/hub-token)}" \
  --set 'hub.tls.selfSigned.dnsNames={faros.kcp.localhost}' \
  --wait
```

The static token is not a fixed value: the first `hack/install` script you
run generates a random one, saves it to `.faros-install/hub-token` and prints
it; every later script reuses it. Export `FAROS_STATIC_TOKEN` before step 1 to
bring your own.

No `kcp.*` overrides: embedded kcp is the chart default. The pod runs two
containers (kcp + hub); the hub waits for kcp's `admin.kubeconfig` before
serving, so first startup takes 30–60s. kcp state persists on the release's
PVC.

If the gateway from step 2 is present, the script also attaches the hub to it
with a `TLSRoute` (SNI `faros.kcp.localhost` → `faros-hub:9443`) — the hub
still terminates its own TLS.

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

---

## Production: Cloudflare DNS

Identical to the [multi-shard guide's Cloudflare section]({% link install-external-kcp.md %}#production-cloudflare-dns),
just with fewer hostnames — only the hub:

```bash
export HUB_DOMAIN=hub.example.com
export HUB_EXTERNAL_URL=https://hub.example.com:8443
export CLOUDFLARE_API_TOKEN=...   # Zone:Read + DNS:Edit
hack/install/09-cloudflare-dns.sh
```

external-dns (Cloudflare provider, Gateway API sources) publishes
`hub.example.com` → the gateway's LoadBalancer address. Keep the record
DNS-only (grey cloud): the route is TLS passthrough and agent tunnels are
long-lived WebSockets. For a browser-trusted certificate use the chart's
`hub.tls.certManager` block with an ACME DNS-01 ClusterIssuer backed by the
same Cloudflare token.

{: .note }
The Cloudflare add-on needs a real zone + token and is not covered by e2e;
everything above it is.

---

## Teardown

```bash
hack/install/teardown.sh
```

---

## e2e coverage

`make e2e-install-embedded` runs scripts `01`, `03`, `08` plus the
port-forwards, then asserts the hub is healthy directly and through the
gateway SNI route, `faros login` works with the static token, and organization
and workspace CRUD function end-to-end.

Moving from embedded to multi-shard later: the kcp data does not migrate
automatically — stand up the external kcp per the
[multi-shard guide]({% link install-external-kcp.md %}) and re-create tenants,
or contact the maintainers about migration tooling.
