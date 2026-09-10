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
# Needs faros-env.sh (sourced automatically from this directory if MCP_URL is
# unset), git, jq, curl. Text files only: the tool rejects binaries.
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

payload=$(mktemp)
trap 'rm -f "$payload"' EXIT

# Message: local commit subjects, oldest first, trimmed to 512 bytes.
msg=$(git log --reverse --format='%s' "$base..HEAD")
if [ "$(printf '%s\n' "$msg" | wc -l)" -gt 1 ]; then
  msg="$(printf '%s\n' "$msg" | head -n 1)"$'\n\n'"$(printf '%s\n' "$msg" | tail -n +2 | sed 's/^/- /')"
fi
msg=$(printf '%s' "$msg" | head -c 500)

files='[]'
deletes='[]'
while IFS=$'\t' read -r status path; do
  case $status in
    D) deletes=$(jq -c --arg p "$path" '. + [$p]' <<<"$deletes") ;;
    *)
      if ! git diff --numstat "$base" HEAD -- "$path" | grep -qv '^-'; then
        echo "faros-commit: $path is binary; commit_files accepts UTF-8 text only" >&2
        exit 1
      fi
      files=$(jq -c --arg p "$path" --rawfile c "$path" '. + [{path:$p, content:$c}]' <<<"$files")
      ;;
  esac
done < <(git diff --no-renames --name-status "$base" HEAD)

jq -n --arg r "$repo" --arg b "$branch" --arg m "$msg" --argjson f "$files" --argjson d "$deletes" \
  '{repositoryRef:$r, branch:$b, message:$m, files:$f, deletePaths:$d}' > "$payload"
echo "faros-commit: sending $(jq '.files|length' "$payload") file(s), $(jq '.deletePaths|length' "$payload") deletion(s) to $repo@$branch" >&2

if ! result=$(mcp code__commit_files "@$payload"); then
  echo "faros-commit: commit_files failed: $result" >&2
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
