#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

if [[ -z "${CONTROLLER_GEN:-}" ]]; then
    echo "You must either set CONTROLLER_GEN to the path to controller-gen or invoke via make"
    exit 1
fi

if [[ -z "${KCP_APIGEN_GEN:-}" ]]; then
    echo "You must either set KCP_APIGEN_GEN to the path to apigen or invoke via make"
    exit 1
fi

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Step 1: Generate CRDs from Go types using controller-gen.
# Run from kedge API dir so paths resolve correctly.
echo "Generating CRDs with controller-gen..."
(
    cd "${REPO_ROOT}/apis"
    "${REPO_ROOT}/${CONTROLLER_GEN}" \
        crd \
        paths="./..." \
        output:crd:artifacts:config="${REPO_ROOT}"/config/crds
)

# Copy CRDs into the bootstrap embed directory.
echo "Copying CRDs to bootstrap embed..."
rm -rf "${REPO_ROOT}/pkg/hub/bootstrap/crds"
mkdir -p "${REPO_ROOT}/pkg/hub/bootstrap/crds"
cp "${REPO_ROOT}"/config/crds/*.yaml "${REPO_ROOT}/pkg/hub/bootstrap/crds/"

# Step 2: Generate KCP APIResourceSchemas and APIExport from CRDs using apigen.
echo "Generating KCP APIResourceSchemas with apigen..."
rm -rf "${REPO_ROOT}/config/kcp/apiresourceschemas/"*.yaml
rm -rf "${REPO_ROOT}/config/kcp/apiexports/"*.yaml
(
    cd "${REPO_ROOT}"
    "./${KCP_APIGEN_GEN}" \
        --input-dir "${REPO_ROOT}/config/crds" \
        --output-dir "${REPO_ROOT}/config/kcp/apiresourceschemas"
)

# apigen puts both APIResourceSchemas and the APIExport in the output dir.
# Move the APIExport file to the apiexports directory.
mv "${REPO_ROOT}"/config/kcp/apiresourceschemas/apiexport-*.yaml \
   "${REPO_ROOT}/config/kcp/apiexports/" 2>/dev/null || true

echo "Codegen complete."
