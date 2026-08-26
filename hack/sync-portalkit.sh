#!/usr/bin/env bash
# Vendors the shared portal UI kit into each portal's src/portalkit/.
#
# Portals build self-contained (no npm workspace / symlink), so the kit is
# copied per portal rather than imported across package boundaries. Visual
# recipes have one source of truth in provider-sdk/portalkit/faros-ui.css; the
# exact file is copied into every vendored kit directory and injected by
# styles.ts when a standalone bundle needs it.
#
# Edit canonical files, then run `make sync-portalkit`. CI runs
# `make verify-portalkit` to fail on drift, missing files, or unexpected copies.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Vanilla-TS (string-building) portals + files.
TS_SRC="$ROOT/provider-sdk/portalkit"
TS_PORTALS=(
  "providers/agents/portal"
  "providers/kuery/portal"
  "providers/quickstart/portal"
)
TS_FILES=(dashboardtile.ts faros-ui.css icons.ts modal.ts styles.ts tabs.ts tenant.ts toast.ts)

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
VUE_FILES=(confirm.ts ConditionsPanel.vue ConfirmDialog.vue LayoutSelector.vue layoutPreference.ts ResourcePage.vue ResourceSectionCard.vue ResourceStatCards.vue ResourceTable.vue table.ts ResourceTableDeleteButton.vue ResourceTableEditButton.vue StatusBadge.vue Tabs.vue)

# Plain assets from the vanilla kit are shared by both portal styles.
VUE_SHARED_FILES=(dashboardtile.ts faros-ui.css icons.ts page-state.ts styles.ts tabs.ts tenant.ts toast.ts)
ALL_PORTALS=("${TS_PORTALS[@]}" "${VUE_PORTALS[@]}")
HOST_UI="$ROOT/portal/src/assets/faros-ui.css"

# README.md documents the canonical kit but is not a distributable vendored
# asset. Every other direct file in the canonical directories must be listed
# above so adding a new source file cannot silently skip every portal.
TS_CANONICAL_ONLY=(README.md page-state.ts)
VUE_CANONICAL_ONLY=()

# These files were visual implementations before faros-ui.css became the sole
# recipe. Remove only this known migration set; arbitrary unexpected files are
# deliberately left in place so --verify can report them instead of hiding
# drift.
OBSOLETE_FILES=(tabs.css ConfirmDialog.css ResourceTable.css ResourceTableDeleteButton.css ResourceTableEditButton.css)

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

remove_obsolete() {
  for p in "${ALL_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/portalkit"
    for f in "${OBSOLETE_FILES[@]}"; do
      rm -f "$dst/$f"
    done
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

verify_manifest() {
  local src="$1"
  local -n expected=$2
  local -n canonical_only=$3
  local stale=0

  while IFS= read -r -d '' path; do
    local name="${path#"$src"/}"
    local known=1
    for f in "${expected[@]}" "${canonical_only[@]}"; do
      if [[ "$name" == "$f" ]]; then
        known=0
        break
      fi
    done
    if (( known )); then
      printf 'unmanifested canonical portalkit file: %s\n' "${path#"$ROOT"/}" >&2
      stale=1
    fi
  done < <(find "$src" -mindepth 1 -type f -print0 | sort -z)
  return "$stale"
}

verify_unexpected() {
  local dst="$1"
  local -n expected=$2
  local stale=0

  if [[ ! -d "$dst" ]]; then
    printf 'missing portalkit directory: %s\n' "${dst#"$ROOT"/}" >&2
    return 1
  fi

  while IFS= read -r -d '' path; do
    local name="${path##*/}"
    local known=1
    for f in "${expected[@]}"; do
      if [[ "$name" == "$f" ]]; then
        known=0
        break
      fi
    done
    if (( known )); then
      printf 'unexpected portalkit copy: %s\n' "${path#"$ROOT"/}" >&2
      stale=1
    fi
  done < <(find "$dst" -mindepth 1 -maxdepth 1 -print0 | sort -z)
  return "$stale"
}

verify_all() {
  local stale=0

  if ! verify_manifest "$TS_SRC" TS_FILES TS_CANONICAL_ONLY; then stale=1; fi
  if ! verify_manifest "$VUE_SRC" VUE_FILES VUE_CANONICAL_ONLY; then stale=1; fi
  if ! verify_file "$TS_SRC/faros-ui.css" "$HOST_UI"; then stale=1; fi
  if ! verify_group "$TS_SRC" TS_PORTALS TS_FILES; then stale=1; fi
  if ! verify_group "$VUE_SRC" VUE_PORTALS VUE_FILES; then stale=1; fi
  if ! verify_group "$TS_SRC" VUE_PORTALS VUE_SHARED_FILES; then stale=1; fi

  local ts_expected=("${TS_FILES[@]}")
  local vue_expected=("${VUE_FILES[@]}" "${VUE_SHARED_FILES[@]}")
  for p in "${TS_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/portalkit" ts_expected; then stale=1; fi
  done
  for p in "${VUE_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/portalkit" vue_expected; then stale=1; fi
  done

  if (( stale )); then
    printf "ERROR: portalkit copies are stale or unexpected. Run 'make sync-portalkit' to update known assets; remove arbitrary copies manually.\n" >&2
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

remove_obsolete
sync_group "$TS_SRC" TS_PORTALS TS_FILES
sync_group "$VUE_SRC" VUE_PORTALS VUE_FILES
sync_group "$TS_SRC" VUE_PORTALS VUE_SHARED_FILES
cp "$TS_SRC/faros-ui.css" "$HOST_UI"
echo "synced portalkit/faros-ui.css -> portal/src/assets/faros-ui.css"
