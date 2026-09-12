#!/usr/bin/env bash
# Vendors core PortalKit and optional AgentKit into their declared consumers.
#
# Portals build self-contained (no npm workspace / symlink), so the kit is
# copied per portal rather than imported across package boundaries. Visual
# core recipes live in provider-sdk/portalkit/faros-ui.css; optional agentic
# recipes live in provider-sdk/agentkit/. Exact stylesheets are copy-synced
# and their separate style loaders inject them when a bundle needs them.
#
# Edit canonical files, then run `make sync-portalkit`. CI runs
# `make verify-portalkit` to fail on drift, missing files, or unexpected copies.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Vanilla-TS (string-building) portals + files.
TS_SRC="$ROOT/provider-sdk/portalkit"
TS_PORTALS=(
  "providers/linear/portal"
  "providers/quickstart/portal"
)
TS_FILES=(navigation.ts dashboardtile.ts faros-ui.css form-select.ts icons.ts kube.ts modal.ts resource-table-filter.ts styles.ts tabs.ts tenant.ts toast.ts)

# Vue SFC portals + files.
VUE_SRC="$ROOT/provider-sdk/portalkit-vue"
VUE_PORTALS=(
  "portal"
  "providers/agents/portal"
  "providers/app-studio/portal"
  "providers/code/portal"
  "providers/databricks/portal"
  "providers/edges/portal"
  "providers/infrastructure/portal"
  "providers/kuery/portal"
)
VUE_FILES=(ActionMenu.vue confirm.ts ConditionsPanel.vue ConfirmDialog.vue CreateGuidance.vue FirstRunGuide.vue FormSelect.vue LayoutSelector.vue layoutPreference.ts ResourceBackLink.vue ResourcePage.vue ResourceSectionCard.vue ResourceStatCards.vue ResourceTable.vue ResourceTableFilter.vue table.ts ResourceTableActionButton.vue ResourceTableDeleteButton.vue ResourceTableEditButton.vue StatusBadge.vue Tabs.vue useAnchoredPopover.ts useDelayedLoading.ts)
VUE_TOAST_FILES=(InlineNotification.vue ToastHost.vue toast.ts)
VUE_TOAST_PORTALS=(
  "portal"
  "providers/app-studio/portal"
  "providers/code/portal"
  "providers/databricks/portal"
  "providers/edges/portal"
  "providers/infrastructure/portal"
  "providers/kuery/portal"
)

# Agents is now a Vue portal, but its provider-local subscription adapter still
# depends on the frozen framework-neutral toast bus. Keep that one compatibility
# file until Agents is explicitly migrated to the Vue host/service contract.
AGENTS_PORTALS=("providers/agents/portal")
AGENTS_LEGACY_FILES=(toast.ts)

# Optional AgentKit assets are distributed only to the two AI consumers. The
# explicit lists keep new conversation primitives reviewable and prevent an
# AI surface from silently appearing in unrelated provider bundles.
AGENTKIT_SRC="$ROOT/provider-sdk/agentkit"
AGENTKIT_VUE_SRC="$ROOT/provider-sdk/agentkit-vue"
AGENTKIT_PORTALS=(
  "providers/agents/portal"
  "providers/app-studio/portal"
)
AGENTKIT_FILES=(activity.css activity.ts agent-ui.css conversation.css styles.ts)
AGENTKIT_VUE_FILES=(
  AIActionRow.vue AIActivityDisclosure.vue AIComposer.vue
  AIConversationHeader.vue AIConversationIdentity.vue AIConversationLayout.vue
  AIConversationRail.vue AIInterrupt.vue AIMessage.vue AIPrimaryAction.vue
  AITranscript.vue AIWorkbenchTab.vue AIWorkbenchTabs.vue AIWorkbenchLauncher.vue AIWorkspace.vue AIPaneDivider.vue
  ModelConnectionCard.vue ModelUsageSection.vue ModelConnectionForm.vue
  ModelIDSelector.vue ai.ts modelIDSelection.ts
  AIActivityFeed.vue AIExecutionDetails.vue AIConversationTurn.vue AITurnProgress.vue AIPlanDisclosure.vue AIPlanSteps.vue
  conversation.ts AITimestamp.vue clock.ts timestamp.ts
)

# Plain assets from the vanilla kit are shared by both portal styles.
VUE_SHARED_FILES=(navigation.ts dashboardtile.ts faros-ui.css icons.ts kube.ts page-state.ts styles.ts tabs.ts tenant.ts)
ALL_PORTALS=("${TS_PORTALS[@]}" "${VUE_PORTALS[@]}")
HOST_UI="$ROOT/portal/src/assets/faros-ui.css"

# README.md documents the canonical kit but is not a distributable vendored
# asset. Every other direct file in the canonical directories must be listed
# above so adding a new source file cannot silently skip every portal.
TS_CANONICAL_ONLY=(README.md dashboardtile.conformance.test.mjs kube.behavior.test.mjs page-state.ts)
VUE_CANONICAL_ONLY=(ActionMenu.conformance.test.mjs Toast.behavior.test.mjs Toast.conformance.test.mjs)
AGENTKIT_CANONICAL_ONLY=(README.md styles.conformance.test.mjs)
AGENTKIT_VUE_CANONICAL_ONLY=(conversation.conformance.test.mjs)

# Vue migrations no longer need the vanilla confirm/alert implementation;
# remove that legacy copy while preserving it for string-building portals.
VUE_LEGACY_FILES=(form-select.ts modal.ts resource-table-filter.ts)

# These files were visual implementations before faros-ui.css became the sole
# recipe. Remove only this known migration set; arbitrary unexpected files are
# deliberately left in place so --verify can report them instead of hiding
# drift.
OBSOLETE_FILES=(tabs.css ConfirmDialog.css ResourceTable.css ResourceTableDeleteButton.css ResourceTableEditButton.css)
AGENTKIT_OBSOLETE_FILES=(
  AIActionRow.vue AIActivityDisclosure.vue AIComposer.vue AIConversationHeader.vue
  AIConversationIdentity.vue AIConversationLayout.vue AIConversationRail.vue
  AIInterrupt.vue AIMessage.vue AIPrimaryAction.vue AITranscript.vue
  AIWorkbenchTab.vue AIWorkspace.vue ai.ts
  ModelConnectionCard.vue ModelUsageSection.vue ModelConnectionForm.vue
  ModelIDSelector.vue modelIDSelection.ts
)

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

sync_agentkit_vue_file() {
  local src="$1"
  local dst="$2"
  local name="$3"
  case "$name" in
    ModelConnectionCard.vue)
      # Canonical AgentKit resolves the core badge from the sibling SDK. The
      # self-contained portal copy resolves that same dependency from its
      # vendored portalkit directory.
      sed 's#\.\./portalkit-vue/StatusBadge\.vue#../portalkit/StatusBadge.vue#g' "$src/$name" > "$dst/$name"
      ;;
    *)
      cp "$src/$name" "$dst/$name"
      ;;
  esac
}

sync_agentkit_vue_group() {
  for p in "${AGENTKIT_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/agentkit"
    mkdir -p "$dst"
    for f in "${AGENTKIT_VUE_FILES[@]}"; do
      sync_agentkit_vue_file "$AGENTKIT_VUE_SRC" "$dst" "$f"
    done
    echo "synced agentkit-vue -> $p/src/agentkit"
  done
}

sync_agentkit_plain_group() {
  for p in "${AGENTKIT_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/agentkit"
    mkdir -p "$dst"
    for f in "${AGENTKIT_FILES[@]}"; do
      cp "$AGENTKIT_SRC/$f" "$dst/$f"
    done
    echo "synced agentkit -> $p/src/agentkit"
  done
}

remove_obsolete() {
  for p in "${ALL_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/portalkit"
    for f in "${OBSOLETE_FILES[@]}"; do
      rm -f "$dst/$f"
    done
    for f in "${AGENTKIT_OBSOLETE_FILES[@]}"; do
      rm -f "$dst/$f"
    done
  done

  for p in "${VUE_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/portalkit"
    for f in "${VUE_LEGACY_FILES[@]}"; do
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

verify_agentkit_vue_file() {
  local src="$1"
  local dst="$2"
  local name="$3"
  local source_rel="${src#"$ROOT"/}"
  local target_rel="${dst#"$ROOT"/}"

  if [[ ! -f "$src/$name" ]]; then
    printf 'missing canonical agentkit-vue file: %s\n' "$source_rel/$name" >&2
    return 1
  fi
  if [[ ! -f "$dst/$name" ]]; then
    printf 'stale agentkit copy: %s (missing; expected %s)\n' "$target_rel/$name" "$source_rel/$name" >&2
    return 1
  fi

  local expected
  expected="$(mktemp)"
  case "$name" in
    ModelConnectionCard.vue)
      sed 's#\.\./portalkit-vue/StatusBadge\.vue#../portalkit/StatusBadge.vue#g' "$src/$name" > "$expected"
      ;;
    *)
      cp "$src/$name" "$expected"
      ;;
  esac
  if ! cmp -s "$expected" "$dst/$name"; then
    printf 'stale agentkit copy: %s (does not match %s)\n' "$target_rel/$name" "$source_rel/$name" >&2
    rm -f "$expected"
    return 1
  fi
  rm -f "$expected"
}

verify_agentkit_vue_group() {
  local stale=0
  for p in "${AGENTKIT_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/agentkit"
    for f in "${AGENTKIT_VUE_FILES[@]}"; do
      if ! verify_agentkit_vue_file "$AGENTKIT_VUE_SRC" "$dst" "$f"; then
        stale=1
      fi
    done
  done
  return "$stale"
}

verify_agentkit_plain_group() {
  local stale=0
  for p in "${AGENTKIT_PORTALS[@]}"; do
    local dst="$ROOT/$p/src/agentkit"
    for f in "${AGENTKIT_FILES[@]}"; do
      if ! verify_file "$AGENTKIT_SRC/$f" "$dst/$f"; then
        stale=1
      fi
    done
  done
  return "$stale"
}

verify_absent_agentkit_dir() {
  local stale=0
  local -a allowed=("${AGENTKIT_PORTALS[@]}")
  for p in "${ALL_PORTALS[@]}"; do
    local is_allowed=1
    for allowed_portal in "${allowed[@]}"; do
      if [[ "$p" == "$allowed_portal" ]]; then
        is_allowed=0
        break
      fi
    done
    if (( is_allowed )) && [[ -d "$ROOT/$p/src/agentkit" ]]; then
      printf 'unexpected agentkit directory: %s\n' "$ROOT/$p/src/agentkit" >&2
      stale=1
    fi
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
  local vue_canonical_expected=("${VUE_FILES[@]}" "${VUE_TOAST_FILES[@]}")

  if ! verify_manifest "$TS_SRC" TS_FILES TS_CANONICAL_ONLY; then stale=1; fi
  if ! verify_manifest "$VUE_SRC" vue_canonical_expected VUE_CANONICAL_ONLY; then stale=1; fi
  if ! verify_manifest "$AGENTKIT_SRC" AGENTKIT_FILES AGENTKIT_CANONICAL_ONLY; then stale=1; fi
  if ! verify_manifest "$AGENTKIT_VUE_SRC" AGENTKIT_VUE_FILES AGENTKIT_VUE_CANONICAL_ONLY; then stale=1; fi
  if ! verify_file "$TS_SRC/faros-ui.css" "$HOST_UI"; then stale=1; fi
  if ! verify_group "$TS_SRC" TS_PORTALS TS_FILES; then stale=1; fi
  if ! verify_group "$VUE_SRC" VUE_PORTALS VUE_FILES; then stale=1; fi
  if ! verify_group "$VUE_SRC" VUE_TOAST_PORTALS VUE_TOAST_FILES; then stale=1; fi
  if ! verify_group "$TS_SRC" AGENTS_PORTALS AGENTS_LEGACY_FILES; then stale=1; fi
  if ! verify_group "$TS_SRC" VUE_PORTALS VUE_SHARED_FILES; then stale=1; fi
  if ! verify_agentkit_plain_group; then stale=1; fi
  if ! verify_agentkit_vue_group; then stale=1; fi

  local ts_expected=("${TS_FILES[@]}")
  local vue_expected=("${VUE_FILES[@]}" "${VUE_SHARED_FILES[@]}" "${VUE_TOAST_FILES[@]}")
  for p in "${TS_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/portalkit" ts_expected; then stale=1; fi
  done
  for p in "${VUE_TOAST_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/portalkit" vue_expected; then stale=1; fi
  done
  local agents_expected=("${VUE_FILES[@]}" "${VUE_SHARED_FILES[@]}" "${AGENTS_LEGACY_FILES[@]}")
  for p in "${AGENTS_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/portalkit" agents_expected; then stale=1; fi
  done
  local agentkit_expected=("${AGENTKIT_FILES[@]}" "${AGENTKIT_VUE_FILES[@]}")
  for p in "${AGENTKIT_PORTALS[@]}"; do
    if ! verify_unexpected "$ROOT/$p/src/agentkit" agentkit_expected; then stale=1; fi
  done
  if ! verify_absent_agentkit_dir; then stale=1; fi

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
sync_group "$VUE_SRC" VUE_TOAST_PORTALS VUE_TOAST_FILES
sync_group "$TS_SRC" AGENTS_PORTALS AGENTS_LEGACY_FILES
sync_group "$TS_SRC" VUE_PORTALS VUE_SHARED_FILES
sync_agentkit_plain_group
sync_agentkit_vue_group
cp "$TS_SRC/faros-ui.css" "$HOST_UI"
echo "synced portalkit/faros-ui.css -> portal/src/assets/faros-ui.css"
