#!/usr/bin/env bash
# Runs kcp and Dex only. Use in a separate terminal from dev-hub.
# Ctrl-C kills both.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

TOOLS=hack/tools

resolve() { ls -1 "${TOOLS}/${1}"-v* 2>/dev/null | sort -V | tail -1; }

KCP_BIN=$(resolve kcp)
DEX_BIN=$(resolve dex)

for bin in KCP_BIN DEX_BIN; do
  if [[ -z "${!bin}" ]]; then
    echo "ERROR: ${bin%%_*} not found in ${TOOLS}/. Run 'make dev-infra' to install tools." >&2
    exit 1
  fi
done

cleanup() {
  echo ""
  echo "Shutting down infra..."
  [[ -n "${KCP_PID:-}" ]] && kill "${KCP_PID}" 2>/dev/null
  [[ -n "${DEX_PID:-}" ]] && kill "${DEX_PID}" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

echo "==> Starting kcp..."
"${KCP_BIN}" start --root-directory=.kcp --feature-gates=WorkspaceMounts=true &
KCP_PID=$!
sleep 3

echo "==> Starting Dex..."
"${DEX_BIN}" serve hack/dev/dex/dex-config-dev.yaml &
DEX_PID=$!

echo "==> Infra ready (kcp + Dex). Run 'make dev-hub' in another terminal."
wait
