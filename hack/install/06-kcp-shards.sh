#!/usr/bin/env bash
# Step 6 — kcp itself: a two-shard deployment behind one front-proxy.
#
#   RootShard  root            https://root.${KCP_DOMAIN}:8443
#   Shard      ${KCP_SHARD_2}  https://${KCP_SHARD_2}.${KCP_DOMAIN}:8443
#   FrontProxy frontproxy      https://${KCP_DOMAIN}:8443   ← clients use this
#
# Each shard gets its own --etcd-prefix in the shared etcd (step 4). All
# components resolve the kcp hostnames to the Envoy gateway ClusterIP via
# hostAliases, so the externally-advertised shard URLs work from inside the
# cluster too (kcp's own controllers call back into those URLs).
#
# Static-token auth: the operator mounts the kcp-static-tokens Secret into the
# shards AND the front-proxy (spec.auth.tokenAuthFile) so the faros hub can
# forward the shared static token. The CSV maps the token to the same identity
# the hub derives (see lib.sh).
#
# Admin kubeconfigs are minted by the operator from Kubeconfig CRs and
# extracted to ${FAROS_INSTALL_STATE_DIR}/kcp-<name>.kubeconfig.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl openssl

# --- static token CSV for kcp's --token-auth-file ---
kc create secret generic kcp-static-tokens -n default \
  --from-literal=token.csv="$(static_token_csv "${FAROS_STATIC_TOKEN}")" \
  --dry-run=client -o yaml | kc apply -f -

# --- shards + front-proxy + SNI routes ---
kc apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: RootShard
metadata:
  name: root
  namespace: default
spec:
  auth:
    serviceAccount:
      enabled: true
    tokenAuthFile:
      secretName: kcp-static-tokens
  external:
    hostname: ${KCP_DOMAIN}
    port: 8443
  certificates:
    issuerRef:
      group: cert-manager.io
      kind: Issuer
      name: selfsigned
  cache:
    embedded:
      enabled: true
  etcd:
    endpoints:
      - http://etcd.kcp-etcd.svc.cluster.local:2379
  extraArgs:
    - --feature-gates=${KCP_FEATURE_GATES}
    # Shared etcd: isolate this shard's keyspace with a distinct prefix.
    - --etcd-prefix=/shard/root
  shardBaseURL: https://root.${KCP_DOMAIN}:8443
  deploymentTemplate:
    spec:
      template:
        spec:
          hostAliases:
            - ip: ${KCP_GATEWAY_IP}
              hostnames:
                - ${KCP_DOMAIN}
                - root.${KCP_DOMAIN}
                - ${KCP_SHARD_2}.${KCP_DOMAIN}
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - root.${KCP_DOMAIN}
  proxy:
    deploymentTemplate:
      spec:
        template:
          spec:
            hostAliases:
              - ip: ${KCP_GATEWAY_IP}
                hostnames:
                  - ${KCP_DOMAIN}
                  - root.${KCP_DOMAIN}
                  - ${KCP_SHARD_2}.${KCP_DOMAIN}
---
apiVersion: operator.kcp.io/v1alpha1
kind: Shard
metadata:
  name: ${KCP_SHARD_2}
  namespace: default
spec:
  # External hostname, certificate issuer and cache config are inherited from
  # the referenced RootShard — they are RootShard-only fields.
  rootShard:
    ref:
      name: root
  auth:
    serviceAccount:
      enabled: true
    tokenAuthFile:
      secretName: kcp-static-tokens
  etcd:
    endpoints:
      - http://etcd.kcp-etcd.svc.cluster.local:2379
  extraArgs:
    - --feature-gates=${KCP_FEATURE_GATES}
    - --etcd-prefix=/shard/${KCP_SHARD_2}
  shardBaseURL: https://${KCP_SHARD_2}.${KCP_DOMAIN}:8443
  deploymentTemplate:
    spec:
      template:
        spec:
          hostAliases:
            - ip: ${KCP_GATEWAY_IP}
              hostnames:
                - ${KCP_DOMAIN}
                - root.${KCP_DOMAIN}
                - ${KCP_SHARD_2}.${KCP_DOMAIN}
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - ${KCP_SHARD_2}.${KCP_DOMAIN}
---
apiVersion: operator.kcp.io/v1alpha1
kind: FrontProxy
metadata:
  name: frontproxy
  namespace: default
spec:
  rootShard:
    ref:
      name: root
  auth:
    serviceAccount:
      enabled: true
    tokenAuthFile:
      secretName: kcp-static-tokens
  external:
    hostname: ${KCP_DOMAIN}
    port: 8443
  deploymentTemplate:
    spec:
      template:
        spec:
          hostAliases:
            - ip: ${KCP_GATEWAY_IP}
              hostnames:
                - ${KCP_DOMAIN}
                - root.${KCP_DOMAIN}
                - ${KCP_SHARD_2}.${KCP_DOMAIN}
  certificateTemplates:
    server:
      spec:
        dnsNames:
          - ${KCP_DOMAIN}
---
apiVersion: gateway.networking.k8s.io/v1alpha3
kind: TLSRoute
metadata:
  name: front-proxy
  namespace: default
spec:
  parentRefs:
    - name: eg
      namespace: envoy-gateway-system
  hostnames:
    - ${KCP_DOMAIN}
  rules:
    - backendRefs:
        - name: frontproxy-front-proxy
          port: 8443
          namespace: default
---
apiVersion: gateway.networking.k8s.io/v1alpha3
kind: TLSRoute
metadata:
  name: root
  namespace: default
spec:
  parentRefs:
    - name: eg
      namespace: envoy-gateway-system
  hostnames:
    - root.${KCP_DOMAIN}
  rules:
    - backendRefs:
        - name: root-kcp
          port: 6443
          namespace: default
---
apiVersion: gateway.networking.k8s.io/v1alpha3
kind: TLSRoute
metadata:
  name: ${KCP_SHARD_2}
  namespace: default
spec:
  parentRefs:
    - name: eg
      namespace: envoy-gateway-system
  hostnames:
    - ${KCP_SHARD_2}.${KCP_DOMAIN}
  rules:
    - backendRefs:
        - name: ${KCP_SHARD_2}-shard-kcp
          port: 6443
          namespace: default
EOF

# --- admin kubeconfigs ---
# The front-proxy drops system:masters on ingress, so the kubeconfig that goes
# through it must additionally carry system:kcp:admin to stay privileged.
kc apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: frontproxy
  namespace: default
spec:
  username: kcp-admin
  groups:
    - system:masters
    - system:kcp:admin
  validity: 8766h
  secretRef:
    name: kcp-frontproxy-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
---
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: root
  namespace: default
spec:
  username: kcp-admin
  groups:
    - system:masters
  validity: 8766h
  secretRef:
    name: kcp-root-kubeconfig
  target:
    rootShardRef:
      name: root
---
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: ${KCP_SHARD_2}
  namespace: default
spec:
  username: kcp-admin
  groups:
    - system:masters
  validity: 8766h
  secretRef:
    name: kcp-${KCP_SHARD_2}-kubeconfig
  target:
    shardRef:
      name: ${KCP_SHARD_2}
EOF

# --- wait for the stack, extract the kubeconfigs ---
for name in frontproxy root "${KCP_SHARD_2}"; do
  kc -n default wait "kubeconfig/${name}" --for=create --timeout=5m
  kc -n default wait "kubeconfig/${name}" --for=condition=Available --timeout=10m
  kc -n default wait "secret/kcp-${name}-kubeconfig" --for=create --timeout=5m
  kc -n default get "secret/kcp-${name}-kubeconfig" -o jsonpath='{.data.kubeconfig}' \
    | base64 -d > "${FAROS_INSTALL_STATE_DIR}/kcp-${name}.kubeconfig"
  echo "wrote ${FAROS_INSTALL_STATE_DIR}/kcp-${name}.kubeconfig"
done

kc -n default rollout status deployment/frontproxy-front-proxy --timeout=10m
kc -n default rollout status deployment/root-kcp --timeout=10m
kc -n default rollout status "deployment/${KCP_SHARD_2}-shard-kcp" --timeout=10m

echo
echo "kcp is up. After 'hack/install/port-forward.sh start' reach it with:"
echo "  kubectl --kubeconfig ${FAROS_INSTALL_STATE_DIR}/kcp-frontproxy.kubeconfig get workspaces"
