#!/usr/bin/env bash
# Runs hub with hot reload via air.
# Requires kcp and Dex to be running (make dev-infra).
# Ctrl-C kills air.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

TOOLS=hack/tools

resolve() { ls -1 "${TOOLS}/${1}"-v* 2>/dev/null | sort -V | tail -1; }

AIR_BIN=$(resolve air)

if [[ -z "${AIR_BIN}" ]]; then
  echo "ERROR: air not found in ${TOOLS}/. Run 'make dev-hub' to install tools." >&2
  exit 1
fi

if [[ ! -f .kcp/admin.kubeconfig ]]; then
  echo "ERROR: .kcp/admin.kubeconfig not found. Start infra first: make dev-infra" >&2
  exit 1
fi

echo "==> Starting air (hub with hot reload)..."
"${AIR_BIN}" -c .air.toml
