#!/usr/bin/env bash
# Runs the full dev stack in one terminal.
# kcp and Dex start once and stay up. Hub runs directly.
# Ctrl-C kills everything.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

TOOLS=hack/tools

# Resolve tool binaries (versioned names, pick latest).
resolve() { ls -1 "${TOOLS}/${1}"-v* 2>/dev/null | sort -V | tail -1; }

KCP_BIN=$(resolve kcp)
DEX_BIN=$(resolve dex)

for bin in KCP_BIN DEX_BIN; do
  if [[ -z "${!bin}" ]]; then
    echo "ERROR: ${bin%%_*} not found in ${TOOLS}/. Run 'make dev' to install tools." >&2
    exit 1
  fi
done

cleanup() {
  echo ""
  echo "Shutting down dev stack..."
  [[ -n "${KCP_PID:-}" ]] && kill "${KCP_PID}" 2>/dev/null
  [[ -n "${DEX_PID:-}" ]] && kill "${DEX_PID}" 2>/dev/null
  [[ -n "${HUB_PID:-}" ]] && kill "${HUB_PID}" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

# 1. Start kcp
echo "==> Starting kcp..."
"${KCP_BIN}" start --root-directory=.kcp --feature-gates=WorkspaceMounts=true &
KCP_PID=$!
sleep 3

# 2. Start Dex
echo "==> Starting Dex..."
"${DEX_BIN}" serve hack/dev/dex/dex-config-dev.yaml &
DEX_PID=$!
sleep 1

# 3. Start hub
echo "==> Starting hub..."
./bin/kedge-hub \
  --dex-issuer-url=https://localhost:5554/dex \
  --dex-client-id=kedge \
  --dex-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0 \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=https://localhost:8443 \
  --external-kcp-kubeconfig=.kcp/admin.kubeconfig \
  --dev-mode &
HUB_PID=$!

wait
