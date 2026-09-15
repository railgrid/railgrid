#!/usr/bin/env bash
# Tear down everything: stop port-forwards, delete the kind cluster, remove
# extracted state.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kind

"$(dirname "${BASH_SOURCE[0]}")/port-forward.sh" stop || true

if kind get clusters 2>/dev/null | grep -qx "${RAILGRID_INSTALL_CLUSTER}"; then
  kind delete cluster --name "${RAILGRID_INSTALL_CLUSTER}"
fi

rm -rf "${RAILGRID_INSTALL_STATE_DIR}"
