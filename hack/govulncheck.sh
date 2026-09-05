#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# govulncheck.sh runs govulncheck over one or more Go modules and fails only on
# vulnerabilities govulncheck traced to a *called* symbol and that are not in
# hack/govulncheck-allow.yaml. See hack/govulncheck/main.go for why plain
# `govulncheck ./...` cannot be used as a blocking gate directly.
#
# Usage:
#   hack/govulncheck.sh                    # every module in the repo
#   hack/govulncheck.sh . providers/edges  # only these module directories
#
# Each module is scanned standalone (GOWORK=off), matching the CI test matrix:
# provider modules `replace github.com/faroshq/provider-sdk => ../../provider-sdk`,
# so the in-tree SDK is what gets linked either way.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ALLOWLIST="${SCRIPT_DIR}/govulncheck-allow.yaml"

# Standalone modules, matching the govulncheck matrix in .github/workflows/ci.yaml.
ALL_MODULES=(
  .
  provider-sdk
  providers/agents
  providers/app-studio
  providers/code
  providers/databricks
  providers/edges
  providers/infrastructure
  providers/kuery
  providers/quickstart
)

MODULES=("$@")
if [[ ${#MODULES[@]} -eq 0 ]]; then
  MODULES=("${ALL_MODULES[@]}")
fi

if ! command -v govulncheck >/dev/null 2>&1; then
  echo "govulncheck not found on PATH. Install it with:" >&2
  echo "  go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
  exit 2
fi

export GOWORK=off

TMP_DIR="$(mktemp -d)"
STUBS=()
cleanup() {
  rm -rf "${TMP_DIR}"
  # Remove only the portal/dist stubs this script created; a real built portal
  # or a tracked .gitkeep is left alone.
  for stub in ${STUBS+"${STUBS[@]}"}; do
    rm -f "${stub}"
    rmdir "$(dirname "${stub}")" 2>/dev/null || true
  done
}
trap cleanup EXIT

failed=()
for module in "${MODULES[@]}"; do
  module_dir="${ROOT_DIR}/${module}"
  if [[ ! -f "${module_dir}/go.mod" ]]; then
    echo "no go.mod in ${module}, skipping" >&2
    continue
  fi

  # Provider binaries `//go:embed all:portal/dist`. The portal is a separate npm
  # project, so give the embed a target rather than building every portal just
  # to run a vulnerability scan; `all:` accepts a lone dotfile but an empty
  # directory is a compile error.
  if [[ -d "${module_dir}/portal" && ! -e "${module_dir}/portal/dist" ]]; then
    mkdir -p "${module_dir}/portal/dist"
    touch "${module_dir}/portal/dist/.gitkeep"
    STUBS+=("${module_dir}/portal/dist/.gitkeep")
  fi

  raw="${TMP_DIR}/$(echo "${module}" | tr '/.' '__').json"
  # govulncheck always exits 0 in JSON mode; the exit code comes from the filter.
  ( cd "${module_dir}" && govulncheck -format json ./... ) >"${raw}"

  if ! ( cd "${ROOT_DIR}" && go run ./hack/govulncheck -allowlist "${ALLOWLIST}" -label "${module}" ) <"${raw}"; then
    failed+=("${module}")
  fi
done

if [[ ${#failed[@]} -gt 0 ]]; then
  echo
  echo "govulncheck gate failed for: ${failed[*]}"
  exit 1
fi
