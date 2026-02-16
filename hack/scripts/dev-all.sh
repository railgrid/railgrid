#!/usr/bin/env bash
# Runs the full dev stack in one terminal.
# KCP and Dex start once and stay up. Air manages hub+agent with hot reload.
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
AIR_BIN=$(resolve air)

for bin in KCP_BIN DEX_BIN AIR_BIN; do
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
  [[ -n "${AIR_PID:-}" ]] && kill "${AIR_PID}" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

# 1. Start KCP
echo "==> Starting KCP..."
"${KCP_BIN}" start --root-directory=.kcp &
KCP_PID=$!
sleep 3

# 2. Start Dex
echo "==> Starting Dex..."
"${DEX_BIN}" serve hack/dev/dex/dex-config-dev.yaml &
DEX_PID=$!
sleep 1

# 3. Start air (manages hub+agent with hot reload)
echo "==> Starting air (hub + agent with hot reload)..."
"${AIR_BIN}" -c .air.toml &
AIR_PID=$!

wait
