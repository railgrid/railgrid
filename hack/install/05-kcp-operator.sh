#!/usr/bin/env bash
# Step 5 — the kcp-operator.
#
# The operator turns RootShard / Shard / FrontProxy / Kubeconfig custom
# resources (applied in step 6) into running kcp deployments, wiring
# certificates (via cert-manager, step 2) and etcd (step 4) for us.
#
# The upstream config/default kustomization references a development image tag
# that is not published, so we overlay it with a published tag
# (KCP_OPERATOR_TAG, defaults to the git ref — main → ghcr.io tag "main").

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl

KCP_OPERATOR_TAG="${KCP_OPERATOR_TAG:-${KCP_OPERATOR_REF}}"

overlay="${FAROS_INSTALL_STATE_DIR}/kcp-operator-kustomize"
mkdir -p "${overlay}"
cat > "${overlay}/kustomization.yaml" <<EOF
resources:
  - https://github.com/kcp-dev/kcp-operator/config/default?ref=${KCP_OPERATOR_REF}
images:
  - name: ghcr.io/kcp-dev/kcp-operator
    newTag: ${KCP_OPERATOR_TAG}
EOF

kc apply --server-side -k "${overlay}"

kc -n kcp-operator-system rollout status deploy/kcp-operator-controller-manager --timeout=5m
