#!/usr/bin/env bash
# Step 1 — create the Kubernetes cluster.
#
# Uses kind by default. Any conformant cluster works: skip this script and
# point your kubectl context at an existing cluster instead (set
# FAROS_INSTALL_CLUSTER so the other scripts pick the right context, or edit
# KUBE_CONTEXT in lib.sh).

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kind kubectl

if kind get clusters 2>/dev/null | grep -qx "${FAROS_INSTALL_CLUSTER}"; then
  echo "kind cluster '${FAROS_INSTALL_CLUSTER}' already exists — reusing it"
else
  kind create cluster --name "${FAROS_INSTALL_CLUSTER}"
fi

kc cluster-info
