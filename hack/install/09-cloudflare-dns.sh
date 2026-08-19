#!/usr/bin/env bash
# Production add-on — publish the gateway hostnames in Cloudflare DNS.
#
# NOT part of the local/kind flow and NOT covered by e2e (it needs a real
# Cloudflare zone and API token). Run it against a cluster whose Envoy
# gateway Service is a real LoadBalancer, with:
#
#   export KCP_DOMAIN=kcp.example.com          # zone or subdomain in Cloudflare
#   export HUB_DOMAIN=hub.example.com
#   export CLOUDFLARE_API_TOKEN=...            # Zone:Read + DNS:Edit
#   hack/install/09-cloudflare-dns.sh
#
# It installs external-dns with the Cloudflare provider, watching Gateway API
# routes (TLSRoute/HTTPRoute): every hostname on a route attached to the
# gateway gets an A/CNAME record pointing at the gateway's LoadBalancer
# address. TLS stays passthrough — kcp and the hub keep terminating their own
# certs, so no Cloudflare proxying (orange cloud) on these records.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl helm

: "${CLOUDFLARE_API_TOKEN:?set CLOUDFLARE_API_TOKEN (Cloudflare API token with Zone:Read + DNS:Edit)}"
CLOUDFLARE_DOMAIN_FILTER="${CLOUDFLARE_DOMAIN_FILTER:-${KCP_DOMAIN#*.}}"

kc create namespace external-dns --dry-run=client -o yaml | kc apply -f -
kc create secret generic cloudflare-api-token -n external-dns \
  --from-literal=apiToken="${CLOUDFLARE_API_TOKEN}" \
  --dry-run=client -o yaml | kc apply -f -

helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/ >/dev/null 2>&1 || true
helm repo update external-dns >/dev/null

helm upgrade --install external-dns external-dns/external-dns \
  --namespace external-dns \
  --kube-context "${KUBE_CONTEXT}" \
  --set "provider.name=cloudflare" \
  --set "env[0].name=CF_API_TOKEN" \
  --set "env[0].valueFrom.secretKeyRef.name=cloudflare-api-token" \
  --set "env[0].valueFrom.secretKeyRef.key=apiToken" \
  --set "sources={gateway-tlsroute,gateway-httproute}" \
  --set "domainFilters={${CLOUDFLARE_DOMAIN_FILTER}}" \
  --set "policy=upsert-only" \
  --set "txtOwnerId=faros-${FAROS_INSTALL_CLUSTER}" \
  --wait

echo
echo "external-dns (Cloudflare) is running. Records for hostnames on Gateway"
echo "API routes under ${CLOUDFLARE_DOMAIN_FILTER} will be created shortly:"
echo "  kubectl -n external-dns logs deploy/external-dns -f"
