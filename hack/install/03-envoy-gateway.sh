#!/usr/bin/env bash
# Step 3 — Envoy Gateway (Gateway API) with a TLS-passthrough listener.
#
# One Gateway serves the whole stack over SNI on port 8443:
#   ${KCP_DOMAIN}              → kcp front-proxy   (TLSRoute, step 6)
#   root.${KCP_DOMAIN}         → root shard        (TLSRoute, step 6)
#   ${KCP_SHARD_2}.${KCP_DOMAIN} → second shard    (TLSRoute, step 6)
#   ${HUB_DOMAIN}              → faros hub         (TLSRoute, step 7/8)
#
# TLS is PASSTHROUGH: Envoy routes on the SNI hostname and the backends
# terminate TLS themselves (kcp with operator-issued certs, the hub with its
# chart-managed cert). The Gateway claims a fixed ClusterIP
# (${KCP_GATEWAY_IP}) so in-cluster pods can resolve the kcp hostnames to it
# via hostAliases; from your machine you reach it with
# `hack/install/port-forward.sh start`.
#
# In production, drop spec.addresses (the cloud LoadBalancer assigns one) and
# let external-dns publish the hostnames — see the Cloudflare DNS section of
# the install docs.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl helm

helm upgrade --install envoy oci://registry-1.docker.io/envoyproxy/gateway-helm \
  --version "${ENVOY_GATEWAY_VERSION}" \
  --namespace envoy-gateway-system --create-namespace \
  --kube-context "${KUBE_CONTEXT}" \
  --wait

kc apply -f - <<EOF
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
  # Fixed ClusterIP so in-cluster clients can hostAlias the kcp hostnames to
  # the gateway. Remove in production (the LoadBalancer address is used).
  addresses:
    - type: IPAddress
      value: ${KCP_GATEWAY_IP}
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

kc -n envoy-gateway-system wait gateway/eg --for=condition=Accepted --timeout=5m
