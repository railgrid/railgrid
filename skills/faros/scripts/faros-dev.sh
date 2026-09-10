#!/usr/bin/env bash
# Drive a development-mode infrastructure Instance through the hub data plane,
# as you. Works whether or not infrastructure__dev_* tools exist on the MCP
# aggregate (they do not when the provider is org-scoped).
#
#   faros-dev.sh sync    <instance> <component> [dir]   push dir (default .) into the component workspace
#   faros-dev.sh exec    <instance> <component> -- <argv…>   run a command, print stdout/stderr, exit with its code
#   faros-dev.sh logs    <instance> <component>          tail the dev process log (Ctrl-C to stop)
#   faros-dev.sh restart <instance> <component>
#   faros-dev.sh status  <instance> [component]          instance status, or the component's process state
#
# Sync sends every non-ignored file under dir (git ls-files when dir is in a
# git repo, else find minus node_modules/dist/.git). Paths are relative to the
# component's workspacePath: for the application template, sync api/ to
# component "api" and web/ to "web". Production instances answer 409.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=/dev/null
[ -n "${TOKEN:-}" ] && [ -n "${WS:-}" ] || . "$here/faros-env.sh"

verb=${1:?usage: faros-dev.sh sync|exec|logs|restart|status <instance> [component] …}
inst=${2:?instance name required}
comp=${3:-}
DP=$HUB/services/providers/infrastructure/dataplane/clusters/$CLUSTER/instances/$inst
auth=(-H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS")

need_comp() { [ -n "$comp" ] || { echo "faros-dev: component required" >&2; exit 2; }; }

case $verb in
  sync)
    need_comp
    dir=${4:-.}
    payload=$(mktemp); trap 'rm -f "$payload"' EXIT
    if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      list=$(cd "$dir" && git ls-files -co --exclude-standard .)
    else
      list=$(cd "$dir" && find . -type f -not -path './node_modules/*' -not -path './dist/*' -not -path './.git/*' | sed 's#^\./##')
    fi
    list=$(printf '%s\n' "$list" | LC_ALL=C sort)
    files='[]'
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      files=$(jq -c --arg p "$f" --rawfile c "$dir/$f" '. + [{path:$p, content:$c}]' <<<"$files")
    done <<<"$list"
    # An authoritative sync (sourceRevision + sourceDigest) replaces the whole
    # managed file set and is what exec later verifies against. The digest is
    # sha256 over path NUL content NUL for every file, sorted by path.
    digest=$(while IFS= read -r f; do
      [ -n "$f" ] || continue
      printf '%s\0' "$f"; cat "$dir/$f"; printf '\0'
    done <<<"$list" | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1)
    rev=$(date +%s)
    jq -n --argjson f "$files" --argjson rev "$rev" --arg dig "$digest" \
      '{files:$f, deletePaths:[], restart:"auto", sourceRevision:$rev, sourceDigest:$dig}' > "$payload"
    echo "faros-dev: syncing $(jq '.files|length' "$payload") file(s) to $inst/$comp" >&2
    curl -s -X POST "${auth[@]}" -H 'Content-Type: application/json' --data-binary @"$payload" "$DP/components/$comp/sync"
    echo
    ;;
  exec)
    need_comp
    shift 3; [ "${1:-}" = "--" ] && shift
    [ $# -gt 0 ] || { echo "faros-dev: exec needs argv after --" >&2; exit 2; }
    proc=$(curl -s "${auth[@]}" "$DP/components/$comp/process")
    rev=$(jq -r '.sourceRevision' <<<"$proc"); dig=$(jq -r '.sourceDigest' <<<"$proc")
    if [ "$rev" = null ] || [ "$rev" = 0 ]; then
      echo "faros-dev: $inst/$comp has no source revision; run 'faros-dev.sh sync' first (exec needs an authoritative sync)" >&2; exit 1
    fi
    argv='[]'
    for a in "$@"; do argv=$(jq -c --arg a "$a" '. + [$a]' <<<"$argv"); done   # not --args: jq eats -e/-c
    body=$(jq -n --argjson rev "$rev" --arg dig "$dig" --argjson argv "$argv" \
      '{action:"start", argv:$argv, timeoutSeconds:120, sourceRevision:$rev, sourceDigest:$dig}')
    start=$(curl -s -X POST "${auth[@]}" -H 'Content-Type: application/json' -H "Idempotency-Key: faros-dev-$$-$(date +%s)" -d "$body" "$DP/components/$comp/exec")
    sid=$(jq -r '.sessionID // empty' <<<"$start" 2>/dev/null || true)
    [ -n "$sid" ] || { echo "faros-dev: exec start failed: $start" >&2; exit 1; }
    for _ in $(seq 1 130); do
      r=$(curl -s -X POST "${auth[@]}" -H 'Content-Type: application/json' -d "{\"action\":\"poll\",\"sessionID\":\"$sid\"}" "$DP/components/$comp/exec")
      case $(jq -r .state <<<"$r") in
        queued|running) sleep 1 ;;
        *) jq -r '.stdout // empty' <<<"$r"; jq -r '.stderr // empty' <<<"$r" >&2
           [ "$(jq -r .truncated <<<"$r")" = true ] && echo "faros-dev: output truncated" >&2
           exit "$(jq -r '.exitCode // 1' <<<"$r")" ;;
      esac
    done
    echo "faros-dev: exec still running after 130 s (session $sid)" >&2; exit 124
    ;;
  logs)    need_comp; curl -sN "${auth[@]}" "$DP/components/$comp/log" ;;
  restart) need_comp; curl -s -X POST "${auth[@]}" "$DP/components/$comp/restart"; echo ;;
  status)
    if [ -n "$comp" ]; then curl -s "${auth[@]}" "$DP/components/$comp/process"; else curl -s "${auth[@]}" "$DP/status"; fi
    echo ;;
  *) echo "faros-dev: unknown verb $verb" >&2; exit 2 ;;
esac
