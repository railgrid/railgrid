#!/usr/bin/env bash
#
# Every e2e suite must be reached by a workflow that runs on its own.
#
# hack/ci/result.py guards the job inventory, so a new workflow job cannot
# escape the completion gate. Nothing guarded the layer below it: whether a
# suite under test/e2e/suites/ is reached by any job at all. A suite with a
# working Makefile target and no caller is invisible -- it compiles, `make`
# runs it on demand, and CI stays green without it. providerflags sat in that
# state while .github/copilot-instructions.md named it a suite reviewers must
# not weaken.
#
# This reads the real tree rather than a second plan: suite directories from
# disk, suite-to-target from `make -n` (so Make resolves prerequisites and
# aggregate targets such as e2e-provider-all), and invocations from the
# workflow files.
#
# Usage: make verify-e2e-suites
set -euo pipefail
cd "$(dirname "$0")/.."

# Suites deliberately not wired into per-change CI. Removing an entry is how
# you promote a suite back into CI; adding one means justifying the gap here.
#
#   tiltcluster      driven by tilt-e2e.yaml, workflow_dispatch-only on
#                    purpose: needs a live Tilt cluster and cloud credentials.
#   installembedded  Helm install path; E2E_INSTALL_TIMEOUT is 45m, too heavy
#   installexternal  for every PR. TODO: wire both into a nightly `schedule:`
#                    workflow -- the repo has no scheduled trigger today, so
#                    they currently run nowhere.
MANUAL_SUITES="tiltcluster installembedded installexternal"

# Workflows that fire without a human. A workflow_dispatch-only workflow means
# someone has to remember, which for coverage purposes is the same as never
# running -- otherwise a dark suite could be "fixed" by referencing it from a
# manual workflow.
automatic_workflows() {
  local file
  for file in .github/workflows/*.y*ml; do
    awk '
      /^on:/          { in_on = 1; next }
      /^[a-z]/        { in_on = 0 }
      in_on && /^  (pull_request|push|schedule):/ { print FILENAME; exit }
    ' "$file"
  done
}

# Targets those workflows invoke, then the suites each target actually runs.
covered_suites() {
  local target
  for target in $(automatic_workflows | xargs grep -ho 'make e2e-[a-z-]*' | sed 's/^make //' | sort -u); do
    make -n "$target" 2>/dev/null | grep -oE 'test/e2e/suites/[a-z_]+' | sed 's|.*/||'
  done | sort -u
}

# Suite directories that actually hold Go tests.
all_suites() {
  local dir
  for dir in test/e2e/suites/*/; do
    compgen -G "${dir}*_test.go" >/dev/null && basename "$dir"
  done | sort
}

dark=$(comm -23 <(all_suites) <(covered_suites) \
  | grep -vxF -f <(tr ' ' '\n' <<<"$MANUAL_SUITES") || true)

if [ -n "$dark" ]; then
  echo "e2e suites reached by no workflow:" >&2
  echo "$dark" | sed 's/^/  /' >&2
  echo >&2
  echo "Add a CI step that runs the suite's make target, or record it in" >&2
  echo "MANUAL_SUITES in $0 with the reason it is excluded." >&2
  exit 1
fi

echo "All e2e suites run in CI or are declared manual."
