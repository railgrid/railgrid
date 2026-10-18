#!/bin/sh
set -eu
# Cold/backend-only runs have no browser stack to refresh.
if [ -f "$RAILGRID_PREPARE_ROOT/.kcp/dev-browser.json" ]; then
  make dev-browser-ready
fi
