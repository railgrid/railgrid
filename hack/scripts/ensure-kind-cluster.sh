#!/usr/bin/env bash
# Ensures a kind cluster exists for the agent's backing cluster.
# Creates one if it doesn't exist, writes the kubeconfig to a file.
#
# Usage:
#   hack/scripts/ensure-kind-cluster.sh [cluster-name]
#
# Outputs the kubeconfig path to stdout (last line).

set -euo pipefail

CLUSTER_NAME="${1:-kedge-agent}"
KUBECONFIG_FILE=".kubeconfig-${CLUSTER_NAME}"

if ! command -v kind &>/dev/null; then
  echo "ERROR: kind is not installed. Install from https://kind.sigs.k8s.io/" >&2
  exit 1
fi

# Create cluster if it doesn't exist.
if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  echo "Creating kind cluster '${CLUSTER_NAME}'..."
  kind create cluster --name "${CLUSTER_NAME}" --wait 60s
else
  echo "Kind cluster '${CLUSTER_NAME}' already exists"
fi

# Export kubeconfig.
kind get kubeconfig --name "${CLUSTER_NAME}" > "${KUBECONFIG_FILE}"
echo "Kubeconfig written to ${KUBECONFIG_FILE}"
