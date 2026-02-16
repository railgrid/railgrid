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

sleep 2

if [[ -f .env ]]; then
  source .env
  ./tmp/kedge-agent join \
    --hub-kubeconfig="${KEDGE_SITE_KUBECONFIG}" \
    --kubeconfig=.kind-kubeconfig \
    --tunnel-url=https://localhost:8443 \
    --site-name="${KEDGE_SITE_NAME}" \
    --labels="${KEDGE_LABELS}" &
else
  echo "WARN: .env not found, skipping agent (run 'make dev-login && make dev-site-create')"
fi

wait
