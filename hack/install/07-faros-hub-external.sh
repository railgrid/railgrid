#!/usr/bin/env bash
# Step 7 — the faros hub against the EXTERNAL kcp from step 6.
#
# With kcp.external.enabled=true the chart renders a stateless Deployment
# (HUB_REPLICAS replicas — every replica serves the full request surface, no
# session affinity needed). The hub connects to kcp through the front-proxy
# admin kubeconfig extracted in step 6, mounted from a Secret.
#
# The hub pod gets the same hostAliases as the kcp pods so the
# operator-issued kubeconfig (which points at https://${KCP_DOMAIN}:8443)
# resolves to the Envoy gateway from inside the cluster.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl helm

FRONTPROXY_KUBECONFIG="${FAROS_INSTALL_STATE_DIR}/kcp-frontproxy.kubeconfig"
if [[ ! -f "${FRONTPROXY_KUBECONFIG}" ]]; then
  echo "ERROR: ${FRONTPROXY_KUBECONFIG} not found — run 06-kcp-shards.sh first" >&2
  exit 1
fi

# Optionally load a locally-built hub image into kind (e2e path).
if [[ "${HUB_KIND_LOAD}" == "true" && -n "${HUB_IMAGE}" && -n "${HUB_IMAGE_TAG}" ]]; then
  kind load docker-image "${HUB_IMAGE}:${HUB_IMAGE_TAG}" --name "${FAROS_INSTALL_CLUSTER}"
fi

kc create namespace "${HUB_NAMESPACE}" --dry-run=client -o yaml | kc apply -f -

kc create secret generic kcp-frontproxy-admin -n "${HUB_NAMESPACE}" \
  --from-file=admin.kubeconfig="${FRONTPROXY_KUBECONFIG}" \
  --dry-run=client -o yaml | kc apply -f -

# The chart ships no ServiceAccount/RBAC; the hub installs CRDs into its own
# cluster with the pod's in-cluster client. Dev-grade grant — scope this down
# for production.
kc apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: faros-hub-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: default
    namespace: ${HUB_NAMESPACE}
EOF

helm_image_args=()
if [[ -n "${HUB_IMAGE}" ]]; then helm_image_args+=(--set "image.hub.repository=${HUB_IMAGE}"); fi
if [[ -n "${HUB_IMAGE_TAG}" ]]; then helm_image_args+=(--set "image.hub.tag=${HUB_IMAGE_TAG}"); fi
if [[ -n "${HUB_IMAGE_PULL_POLICY}" ]]; then helm_image_args+=(--set "image.hub.pullPolicy=${HUB_IMAGE_PULL_POLICY}"); fi

helm upgrade --install faros-hub "${HUB_CHART}" \
  --namespace "${HUB_NAMESPACE}" \
  --kube-context "${KUBE_CONTEXT}" \
  --set "replicaCount=${HUB_REPLICAS}" \
  --set "kcp.embedded.enabled=false" \
  --set "kcp.external.enabled=true" \
  --set "kcp.external.existingSecret=kcp-frontproxy-admin" \
  --set "hub.hubExternalURL=${HUB_EXTERNAL_URL}" \
  --set "hub.devMode=true" \
  --set "hub.embeddedGraphQL=true" \
  --set "hub.staticAuthTokens={${FAROS_STATIC_TOKEN}}" \
  --set "hub.tls.selfSigned.dnsNames={${HUB_DOMAIN}}" \
  --set "hostAliases[0].ip=${KCP_GATEWAY_IP}" \
  --set "hostAliases[0].hostnames[0]=${KCP_DOMAIN}" \
  --set "hostAliases[0].hostnames[1]=root.${KCP_DOMAIN}" \
  --set "hostAliases[0].hostnames[2]=${KCP_SHARD_2}.${KCP_DOMAIN}" \
  "${helm_image_args[@]}" \
  --wait --timeout 15m

# Expose the hub through the same TLS-passthrough gateway as kcp (SNI
# ${HUB_DOMAIN}). The hub terminates TLS itself, so passthrough keeps
# WebSockets (agent tunnels) and client-cert flows intact.
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

kc -n "${HUB_NAMESPACE}" rollout status deployment/faros-hub --timeout=10m

echo
echo "faros hub is up. After 'hack/install/port-forward.sh start':"
echo "  curl -k ${HUB_EXTERNAL_URL}/healthz"
echo "  faros login --hub-url ${HUB_EXTERNAL_URL} --token ${FAROS_STATIC_TOKEN} --insecure-skip-tls-verify"
