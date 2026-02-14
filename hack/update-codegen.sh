#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..

echo "Running deepcopy generation..."
# controller-gen object paths=./apis/...
echo "TODO: Install and run controller-gen for deepcopy generation"

echo "Running CRD generation..."
# controller-gen crd paths=./apis/... output:crd:dir=./config/crds
echo "TODO: Install and run controller-gen for CRD generation"

echo "Codegen complete."
