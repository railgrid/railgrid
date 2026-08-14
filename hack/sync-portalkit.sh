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
TS_FILES=(icons.ts modal.ts tenant.ts toast.ts)

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
VUE_FILES=(confirm.ts ConfirmDialog.vue ResourceTable.vue ResourceTableDeleteButton.vue ResourceTableDeleteButton.css ResourceTableEditButton.vue ResourceTableEditButton.css ConditionsPanel.vue StatusBadge.vue)

# vibe-studio's vanilla-TS portal consumes tenant.ts + icons.ts (no modal).
VIBE_PORTALS=("providers/vibe-studio/portal")
VIBE_FILES=(tenant.ts icons.ts)

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

verify_file() {
  local src="$1"
  local dst="$2"
  local source_rel="${src#"$ROOT"/}"
  local target_rel="${dst#"$ROOT"/}"

  if [[ ! -f "$src" ]]; then
    printf 'missing canonical portalkit file: %s\n' "$source_rel" >&2
    return 1
  fi
  if [[ ! -f "$dst" ]]; then
    printf 'stale portalkit copy: %s (missing; expected %s)\n' "$target_rel" "$source_rel" >&2
    return 1
  fi
  if ! cmp -s "$src" "$dst"; then
    printf 'stale portalkit copy: %s (does not match %s)\n' "$target_rel" "$source_rel" >&2
    return 1
  fi
}

verify_group() {
  local src="$1"
  local -n portals=$2
  local -n files=$3
  local stale=0

  for p in "${portals[@]}"; do
    local dst="$ROOT/$p/src/portalkit"
    for f in "${files[@]}"; do
      if ! verify_file "$src/$f" "$dst/$f"; then
        stale=1
      fi
    done
  done
  return "$stale"
}

verify_all() {
  local stale=0

  if ! verify_group "$TS_SRC" TS_PORTALS TS_FILES; then
    stale=1
  fi
  if ! verify_group "$VUE_SRC" VUE_PORTALS VUE_FILES; then
    stale=1
  fi
  if ! verify_group "$TS_SRC" VIBE_PORTALS VIBE_FILES; then
    stale=1
  fi
  # tenant.ts and toast.ts are plain TS (no framework) and shared by portals of
  # BOTH kinds, so the vanilla canonicals are also vendored into the Vue portals.
  local vue_shared_files=(tenant.ts toast.ts)
  if ! verify_group "$TS_SRC" VUE_PORTALS vue_shared_files; then
    stale=1
  fi

  if (( stale )); then
    printf "ERROR: portalkit copies are stale. Run 'make sync-portalkit' to update them.\n" >&2
    return 1
  fi
  echo "portalkit copies are in sync"
}

case "${1:-}" in
"")
  ;;
--verify)
  if [[ "$#" -ne 1 ]]; then
    echo "usage: $0 [--verify]" >&2
    exit 2
  fi
  verify_all
  exit $?
  ;;
*)
  echo "usage: $0 [--verify]" >&2
  exit 2
  ;;
esac

sync_group "$TS_SRC" TS_PORTALS TS_FILES
sync_group "$VUE_SRC" VUE_PORTALS VUE_FILES

sync_group "$TS_SRC" VIBE_PORTALS VIBE_FILES

# tenant.ts and toast.ts are plain TS (no framework) and shared by portals of
# BOTH kinds, so the vanilla canonicals are also vendored into the Vue portals.
for p in "${VUE_PORTALS[@]}"; do
  cp "$TS_SRC/tenant.ts" "$ROOT/$p/src/portalkit/tenant.ts"
  cp "$TS_SRC/toast.ts" "$ROOT/$p/src/portalkit/toast.ts"
  echo "synced tenant.ts, toast.ts -> $p/src/portalkit"
done

# dashboardtile.ts is the shared scaffolding behind every provider's
# <faros-dashboard-tile-*> element. Plain TS for the same reason as tenant.ts,
# and vendored into every portal that ships (or may ship) a tile.
for p in "${VUE_PORTALS[@]}" "${TS_PORTALS[@]}" "${VIBE_PORTALS[@]}"; do
  cp "$TS_SRC/dashboardtile.ts" "$ROOT/$p/src/portalkit/dashboardtile.ts"
  echo "synced dashboardtile.ts -> $p/src/portalkit"
done

# icons.ts reaches the Vue portals too: their dashboard tiles draw stat glyphs
# from the same set the vanilla portals use, so a check mark is the same check
# mark on every card. (Vue portals otherwise have no icon library — they ship
# self-contained, without lucide.)
for p in "${VUE_PORTALS[@]}"; do
  cp "$TS_SRC/icons.ts" "$ROOT/$p/src/portalkit/icons.ts"
  echo "synced icons.ts -> $p/src/portalkit"
done
