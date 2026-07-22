#!/usr/bin/env bash
# Vendors the shared portal UI kits into each provider portal's src/portalkit/.
# The portals build self-contained (no npm workspace / symlink), so shared UI
# primitives are copied per portal rather than imported across package
# boundaries.
#
#   provider-sdk/portalkit      → vanilla-TS portals  (icons.ts, modal.ts)
#   provider-sdk/portalkit-vue  → Vue SFC portals     (confirm.ts, ConfirmDialog.vue)
#
# Edit the canonical files under provider-sdk/ and run `make sync-portalkit`.
# CI runs `make verify-portalkit` to fail on drift.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Vanilla-TS (string-building) portals + files.
TS_SRC="$ROOT/provider-sdk/portalkit"
TS_PORTALS=(
  "providers/agents/portal"
  "providers/kuery/portal"
  "providers/quickstart/portal"
)
TS_FILES=(icons.ts modal.ts tenant.ts)

# Vue SFC portals + files.
VUE_SRC="$ROOT/provider-sdk/portalkit-vue"
VUE_PORTALS=(
  "portal"
  "providers/app-studio/portal"
  "providers/code/portal"
  "providers/databricks/portal"
  "providers/edges/portal"
  "providers/infrastructure/portal"
)
VUE_FILES=(confirm.ts ConfirmDialog.vue ResourceTable.vue ConditionsPanel.vue StatusBadge.vue)

sync_group() {
  local src="$1"; shift
  local -n portals=$1; shift
  local -n files=$1; shift
  for p in "${portals[@]}"; do
    local dst="$ROOT/$p/src/portalkit"
    mkdir -p "$dst"
    for f in "${files[@]}"; do
      cp "$src/$f" "$dst/$f"
    done
    echo "synced $(basename "$src") -> $p/src/portalkit"
  done
}

sync_group "$TS_SRC" TS_PORTALS TS_FILES
sync_group "$VUE_SRC" VUE_PORTALS VUE_FILES

# tenant.ts is plain TS (no framework) and shared by hub-proxy portals of BOTH
# kinds, so the vanilla canonical is also vendored into the Vue portals.
for p in "${VUE_PORTALS[@]}"; do
  cp "$TS_SRC/tenant.ts" "$ROOT/$p/src/portalkit/tenant.ts"
  echo "synced tenant.ts -> $p/src/portalkit"
done
