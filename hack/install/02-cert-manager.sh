#!/usr/bin/env bash
# Step 2 — cert-manager + a self-signed Issuer.
#
# The kcp-operator issues every kcp certificate (serving certs, client CAs,
# the admin kubeconfig client certs) through cert-manager. The self-signed
# Issuer lives in the SAME namespace as the kcp custom resources (default)
# because RootShard/Shard reference it by name with kind Issuer.
#
# For production TLS on the *faros hub* (browser-facing), see the Cloudflare
# section of the install docs — kcp itself stays on the operator-managed CA
# either way, clients trust it via the extracted kubeconfigs.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl

kc apply --server-side -f \
  "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

kc -n cert-manager wait --for=condition=Available deployment --all --timeout=5m

kc apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: selfsigned
  namespace: default
spec:
  selfSigned: {}
EOF
