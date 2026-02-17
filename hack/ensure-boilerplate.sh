#!/usr/bin/env bash

# Ensures all .go files have the Apache 2.0 license boilerplate header.
# Adds the header to files that are missing it (skips generated files).
# Usage: hack/ensure-boilerplate.sh [--verify]
#   --verify  Check only, do not modify files. Exit 1 if any are missing.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

BOILERPLATE="${SCRIPT_DIR}/boilerplate/boilerplate.go.txt"
YEAR="$(date +%Y)"
VERIFY=false

if [[ "${1:-}" == "--verify" ]]; then
  VERIFY=true
fi

# Build the header with the current year.
HEADER=$(sed "s/YEAR/${YEAR}/" "${BOILERPLATE}")

missing=()

while IFS= read -r -d '' file; do
  # Skip generated files.
  if head -5 "$file" | grep -q "DO NOT EDIT"; then
    continue
  fi
  # Skip zz_generated files.
  if [[ "$(basename "$file")" == zz_generated* ]]; then
    continue
  fi

  # Check if file already has a copyright header (any year).
  if head -3 "$file" | grep -q "Copyright .* The Faros Authors"; then
    continue
  fi

  missing+=("$file")
done < <(find "${ROOT_DIR}" -name '*.go' -not -path '*/vendor/*' -not -path '*/hack/tools/*' -print0)

if [[ ${#missing[@]} -eq 0 ]]; then
  echo "All Go files have the license boilerplate."
  exit 0
fi

if [[ "${VERIFY}" == true ]]; then
  echo "The following files are missing the license boilerplate:"
  printf '  %s\n' "${missing[@]}"
  exit 1
fi

for file in "${missing[@]}"; do
  echo "Adding boilerplate to ${file#"${ROOT_DIR}/"}"
  # Prepend header + blank line, then original content.
  tmp=$(mktemp)
  { echo "${HEADER}"; echo ""; cat "$file"; } > "$tmp"
  mv "$tmp" "$file"
done

echo "Added boilerplate to ${#missing[@]} file(s)."
