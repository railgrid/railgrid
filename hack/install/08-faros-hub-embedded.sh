#!/usr/bin/env bash
# Embedded-kcp variant — the faros hub with kcp running INSIDE the release.
#
# The chart's default mode: a StatefulSet where kcp (with its embedded etcd,
# persisted to a PVC) runs next to the hub. No cert-manager, external etcd or
# kcp-operator needed — steps 01 (cluster) and 03 (gateway, optional) are the
# only prerequisites. Single replica by design: each replica would be its own
# control plane; scaling the hub requires the external-kcp install.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl helm

# Optionally load a locally-built hub image into kind (e2e path).
if [[ "${HUB_KIND_LOAD}" == "true" && -n "${HUB_IMAGE}" && -n "${HUB_IMAGE_TAG}" ]]; then
  kind load docker-image "${HUB_IMAGE}:${HUB_IMAGE_TAG}" --name "${FAROS_INSTALL_CLUSTER}"
fi

kc create namespace "${HUB_NAMESPACE}" --dry-run=client -o yaml | kc apply -f -

helm_image_args=()
if [[ -n "${HUB_IMAGE}" ]]; then helm_image_args+=(--set "image.hub.repository=${HUB_IMAGE}"); fi
if [[ -n "${HUB_IMAGE_TAG}" ]]; then helm_image_args+=(--set "image.hub.tag=${HUB_IMAGE_TAG}"); fi
if [[ -n "${HUB_IMAGE_PULL_POLICY}" ]]; then helm_image_args+=(--set "image.hub.pullPolicy=${HUB_IMAGE_PULL_POLICY}"); fi

helm upgrade --install faros-hub "${HUB_CHART}" \
  --namespace "${HUB_NAMESPACE}" \
  --kube-context "${KUBE_CONTEXT}" \
  --set "hub.hubExternalURL=${HUB_EXTERNAL_URL}" \
  --set "hub.devMode=true" \
  --set "hub.embeddedGraphQL=true" \
  --set "hub.staticAuthTokens={${FAROS_STATIC_TOKEN}}" \
  --set "hub.tls.selfSigned.dnsNames={${HUB_DOMAIN}}" \
  "${helm_image_args[@]}" \
  --wait --timeout 15m

# Optional: expose the hub through the Envoy gateway (step 3) with SNI
# passthrough — same wiring as the external-kcp install. Skipped automatically
# when the gateway isn't installed.
if kc -n envoy-gateway-system get gateway eg >/dev/null 2>&1; then
  kc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1alpha3
kind: TLSRoute
metadata:
  name: faros-hub
  namespace: ${HUB_NAMESPACE}
spec:
  parentRefs:
    - name: eg
      namespace: envoy-gateway-system
  hostnames:
    - ${HUB_DOMAIN}
  rules:
    - backendRefs:
        - name: faros-hub
          port: 9443
          namespace: ${HUB_NAMESPACE}
EOF
fi

kc -n "${HUB_NAMESPACE}" rollout status statefulset/faros-hub --timeout=10m

echo
echo "faros hub (embedded kcp) is up. After 'hack/install/port-forward.sh start':"
echo "  curl -k ${HUB_EXTERNAL_URL}/healthz"
echo "  faros login --hub-url ${HUB_EXTERNAL_URL} --token ${FAROS_STATIC_TOKEN} --insecure-skip-tls-verify"
