#!/usr/bin/env bash
# Runs hub and agent. Called by air on each rebuild cycle.
# KCP and Dex are managed by dev-all.sh and stay up across restarts.
set -euo pipefail

trap 'kill 0; wait' EXIT

./tmp/kedge-hub \
  --dex-issuer-url=https://localhost:5554/dex \
  --dex-client-id=kedge \
  --dex-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0 \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=https://localhost:8443 \
  --external-kcp-kubeconfig=.kcp/admin.kubeconfig \
  --dev-mode &

wait
