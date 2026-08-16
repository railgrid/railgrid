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

# apigen.sh wraps kcp's apigen so that a changed APIResourceSchema gets a NEW
# name instead of being rewritten under the old one.
#
# Why this wrapper exists
# -----------------------
# APIResourceSchemas are IMMUTABLE in kcp. apigen on its own does not help you
# here: pointed at an output directory that already contains a schema, it keeps
# that schema's name and rewrites the content underneath it. The generated files
# look fine and `git diff` looks innocent — but applying them to any cluster that
# already has the old content fails with
#
#   apiresourceschemas.apis.kcp.io "v260615-841bea0.providers.admin.faros.sh"
#     is forbidden: [spec: Invalid value: {...}: is immutable]
#
# and the hub's bootstrap (kcp's confighelpers.Bootstrap) retries that apply
# forever without surfacing the error, so the hub simply never becomes ready.
# A one-word doc-comment edit on an exported API type was enough to do it.
#
# What it does instead
# --------------------
#   1. Runs apigen into an empty temp dir, where it mints fresh names
#      (v<yymmdd>-<HEAD-sha>) from the current CRDs.
#   2. For each schema, compares the fresh output with what is already in the
#      output dir, ignoring metadata.name:
#        - identical  -> keeps the existing file, so the name does NOT churn on
#                        every commit;
#        - different  -> takes the fresh file, so the name bumps and kcp sees a
#                        create instead of a forbidden update;
#        - new        -> takes the fresh file.
#   3. Runs apigen again against the output dir, which preserves the names
#      chosen above and regenerates the APIExports to reference them.
#
# Usage: hack/apigen.sh --input-dir <crds> --output-dir <kcp>
# Requires KCP_APIGEN_GEN (exported by the Makefile).

set -o errexit
set -o nounset
set -o pipefail

if [[ -z "${KCP_APIGEN_GEN:-}" ]]; then
    echo "KCP_APIGEN_GEN is not set. Invoke via make (e.g. 'make crds')." >&2
    exit 1
fi

INPUT_DIR=""
OUTPUT_DIR=""
EXTRA_ARGS=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        --input-dir)  INPUT_DIR="$2"; shift 2 ;;
        --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
        *)            EXTRA_ARGS+=("$1"); shift ;;
    esac
done

if [[ -z "${INPUT_DIR}" || -z "${OUTPUT_DIR}" ]]; then
    echo "usage: $0 --input-dir <dir> --output-dir <dir> [apigen args...]" >&2
    exit 1
fi

APIGEN="${KCP_APIGEN_GEN}"
[[ -x "${APIGEN}" ]] || APIGEN="./${KCP_APIGEN_GEN}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${OUTPUT_DIR}"

# Step 1: fresh generation into an empty dir — every schema gets today's name.
"${APIGEN}" --input-dir "${INPUT_DIR}" --output-dir "${TMP_DIR}" ${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"} >/dev/null

# strip_name drops the single versioned `metadata.name` line so two generations
# can be compared on content alone. Both files come from the same apigen build,
# so nothing else about the formatting can differ.
strip_name() {
    grep -v '^  name: v[0-9]' "$1"
}

shopt -s nullglob
for fresh in "${TMP_DIR}"/apiresourceschema-*.yaml; do
    base="$(basename "${fresh}")"
    existing="${OUTPUT_DIR}/${base}"

    if [[ ! -f "${existing}" ]]; then
        echo "  + new schema ${base}"
        cp "${fresh}" "${existing}"
        continue
    fi

    if diff -q <(strip_name "${existing}") <(strip_name "${fresh}") >/dev/null; then
        continue # content unchanged: keep the established name
    fi

    old_name="$(grep -m1 '^  name: v[0-9]' "${existing}" | awk '{print $2}')"
    new_name="$(grep -m1 '^  name: v[0-9]' "${fresh}" | awk '{print $2}')"
    if [[ "${old_name}" == "${new_name}" ]]; then
        # apigen derives the name from HEAD, so a second schema-changing edit on
        # the same commit reuses it. Renaming would be a no-op and the immutable
        # update would come back, so say so loudly rather than generate a file
        # that cannot be applied.
        echo "  ! ${base}: content changed but the generated name is still ${new_name}." >&2
        echo "    apigen derives names from HEAD; commit the API change and re-run" >&2
        echo "    codegen so the schema gets a distinct name." >&2
        exit 1
    fi
    echo "  ~ schema changed, bumping name: ${old_name} -> ${new_name}"
    cp "${fresh}" "${existing}"
done

# Drop schemas whose CRD disappeared, so a removed API does not linger.
for existing in "${OUTPUT_DIR}"/apiresourceschema-*.yaml; do
    base="$(basename "${existing}")"
    if [[ ! -f "${TMP_DIR}/${base}" ]]; then
        echo "  - removing stale schema ${base}"
        rm -f "${existing}"
    fi
done
shopt -u nullglob

# Step 2: regenerate in place. apigen preserves the names selected above and
# writes the APIExports that reference them.
"${APIGEN}" --input-dir "${INPUT_DIR}" --output-dir "${OUTPUT_DIR}" ${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}
