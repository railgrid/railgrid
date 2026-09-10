#!/usr/bin/env bash
# Record local git commits through faros so App Studio can promote them.
#
#   faros-commit.sh <repositoryRef> [branch]
#
# Run inside a clone of the project repo. Workflow:
#   1. edit, then `git add -A && git commit -m "…"` LOCALLY (never push)
#   2. faros-commit.sh <repositoryRef>
#
# It sends every file that differs between origin/<branch> and HEAD to
# code__commit_files (deletions as deletePaths), using the local commit
# subjects as the message (capped at the CRD's 512-byte limit). When faros
# reports Succeeded it fetches, checks that origin/<branch> now has exactly
# the tree you committed, and resets the local branch onto it — so your clone
# carries the faros-recorded SHA, not a divergent local one.
#
# Binary files (not UTF-8, or containing NUL) are sent base64-encoded (at most
# 25 MiB each, 48 MiB per commit) when the tool's input schema declares a
# files[].encoding property; otherwise the script refuses the change because
# the hub's code provider does not support binary files yet.
#
# Needs faros-env.sh (sourced automatically from this directory if MCP_URL is
# unset), git, jq, curl, base64.
set -euo pipefail

repo=${1:?usage: faros-commit.sh <repositoryRef> [branch]}
branch=${2:-main}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
if [ -z "${MCP_URL:-}" ] || [ -z "${MCP_TOKEN:-}" ]; then
  # shellcheck source=/dev/null
  . "$here/faros-env.sh"
fi

git fetch -q origin "$branch"
base=origin/$branch
if [ -n "$(git status --porcelain)" ]; then
  echo "faros-commit: uncommitted changes; git add -A && git commit first" >&2
  exit 1
fi
if [ "$(git rev-parse HEAD^{tree})" = "$(git rev-parse "$base^{tree}")" ]; then
  echo "faros-commit: nothing to send; HEAD matches $base" >&2
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# mcp_rpc <request file>: POST one JSON-RPC request to the aggregate and print
# the reply's JSON-RPC message (the body, or the last SSE data line). Request
# bodies go through a file: base64 content is far past the argv limit that
# faros-env.sh's mcp helper (jq --argjson) runs into.
mcp_rpc() {
  local out
  out=$(curl -s -X POST "$MCP_URL" -H "Authorization: Bearer $MCP_TOKEN" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    --data-binary @"$1")
  case $out in
    '{'*) printf '%s\n' "$out" ;;
    *) printf '%s\n' "$out" | sed -n 's/^data: //p' | tail -n 1 ;;
  esac
}

# is_text FILE: FILE is valid UTF-8 without NUL bytes, i.e. it survives a JSON
# string unchanged (jq replaces invalid UTF-8, so compare its round trip).
is_text() {
  LC_ALL=C tr -d '\000' < "$1" | cmp -s - "$1" &&
    jq -jn --rawfile c "$1" '$c' | cmp -s - "$1"
}

# binary_supported: the code__commit_files input schema declares an encoding
# property on its file items (following a local $ref), per tools/list.
binary_supported() {
  printf '%s' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' > "$tmp/list-req"
  mcp_rpc "$tmp/list-req" | jq -e --arg n code__commit_files '
    [ .result.tools[]? | select(.name == $n) | .inputSchema as $s
      | ($s.properties.files.items // {}) as $i
      | (if (($i["$ref"] // "") | startswith("#/"))
           then ($s | getpath($i["$ref"] | ltrimstr("#/") | split("/")))
           else $i end)
      | ((. // {}).properties // {}) | has("encoding") ] | any' >/dev/null 2>&1
}

# Message: local commit subjects, oldest first, trimmed to 512 bytes.
msg=$(git log --reverse --format='%s' "$base..HEAD")
if [ "$(printf '%s\n' "$msg" | wc -l)" -gt 1 ]; then
  msg="$(printf '%s\n' "$msg" | head -n 1)"$'\n\n'"$(printf '%s\n' "$msg" | tail -n +2 | sed 's/^/- /')"
fi
msg=$(printf '%s' "$msg" | head -c 500)

: > "$tmp/files"
deletes='[]'
binaries=()
total=0
while IFS=$'\t' read -r status path; do
  case $status in
    D) deletes=$(jq -c --arg p "$path" '. + [$p]' <<<"$deletes") ;;
    *)
      # The committed blob, byte for byte (the tree is clean, but this skips
      # any checkout filters).
      git cat-file blob "HEAD:$path" > "$tmp/blob"
      size=$(wc -c < "$tmp/blob" | tr -d ' ')
      total=$((total + size))
      if [ "$total" -gt 50331648 ]; then
        echo "faros-commit: change is over 48 MiB in total (reached at $path); split it into smaller commits" >&2
        exit 1
      fi
      if is_text "$tmp/blob"; then
        jq -cn --arg p "$path" --rawfile c "$tmp/blob" '{path:$p, content:$c}' >> "$tmp/files"
      else
        if [ "$size" -gt 26214400 ]; then
          echo "faros-commit: $path is binary and $size bytes; binary files are limited to 25 MiB each" >&2
          exit 1
        fi
        base64 < "$tmp/blob" | tr -d '\n' > "$tmp/b64"
        jq -cn --arg p "$path" --rawfile c "$tmp/b64" '{path:$p, content:$c, encoding:"base64"}' >> "$tmp/files"
        binaries+=("$path")
      fi
      ;;
  esac
done < <(git diff --no-renames --name-status "$base" HEAD)

if [ "${#binaries[@]}" -gt 0 ] && ! binary_supported; then
  echo "faros-commit: ${binaries[*]}: the hub's code provider doesn't support binary files yet (code__commit_files accepts UTF-8 text only); drop them from this change or commit them another way" >&2
  exit 1
fi

jq -n --arg r "$repo" --arg b "$branch" --arg m "$msg" --slurpfile f "$tmp/files" --argjson d "$deletes" \
  '{repositoryRef:$r, branch:$b, message:$m, files:$f, deletePaths:$d}' > "$tmp/payload"
echo "faros-commit: sending $(jq '.files|length' "$tmp/payload") file(s), $(jq '.deletePaths|length' "$tmp/payload") deletion(s) to $repo@$branch" >&2

jq -n --slurpfile a "$tmp/payload" \
  '{jsonrpc:"2.0", id:1, method:"tools/call", params:{name:"code__commit_files", arguments:$a[0]}}' > "$tmp/call-req"
reply=$(mcp_rpc "$tmp/call-req")
if printf '%s' "$reply" | jq -e '.error' >/dev/null 2>&1; then
  echo "faros-commit: commit_files failed: $(printf '%s' "$reply" | jq -c .error)" >&2
  exit 1
fi
result=$(printf '%s' "$reply" | jq -r '.result.structuredContent // (.result.content[0].text | (fromjson? // .))' 2>/dev/null || true)
if printf '%s' "$reply" | jq -e '.result.isError == true' >/dev/null 2>&1 || [ -z "$reply" ]; then
  echo "faros-commit: commit_files failed: ${result:-no reply}" >&2
  exit 1
fi
phase=$(jq -r '.phase // empty' <<<"$result" 2>/dev/null || true)
sha=$(jq -r '.commitSHA // empty' <<<"$result" 2>/dev/null || true)
if [ "$phase" != Succeeded ] || [ -z "$sha" ]; then
  echo "faros-commit: commit not confirmed (phase=${phase:-?}); result: $result" >&2
  echo "faros-commit: check 'kubectl get repositorycommits.code.faros.sh -l code.faros.sh/repository=$repo'" >&2
  exit 1
fi

git fetch -q origin "$branch"
if [ "$(git rev-parse HEAD^{tree})" != "$(git rev-parse "$base^{tree}")" ]; then
  echo "faros-commit: recorded $sha, but $base's tree differs from your HEAD (someone else committed?)." >&2
  echo "faros-commit: left your branch alone; reconcile with 'git rebase $base'." >&2
  exit 1
fi
git reset -q --hard "$base"
echo "$sha"
