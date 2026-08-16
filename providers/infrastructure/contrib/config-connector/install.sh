#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# Install the opt-in, cluster-mode Config Connector runtime used by the
# development Pub/Sub smoke test. This is intentionally outside the provider
# bootstrap path: ordinary Tilt startup must not install a cloud controller or
# import credentials into a cluster.

set -euo pipefail

readonly KCC_VERSION="1.153.0"
readonly KCC_BUNDLE_URL="https://storage.googleapis.com/configconnector-operator/${KCC_VERSION}/release-bundle.tar.gz"
readonly KCC_BUNDLE_SHA256="e2e46bb51638a39dbbf6e28f35260890d8bc32c4fccc455a02b34c947221e3f2"
readonly KCC_NAMESPACE="cnrm-system"
readonly KCC_SECRET="gsa-key"
readonly KCC_NAME="configconnector.core.cnrm.cloud.google.com"
readonly PUBSUB_CRD="pubsubtopics.pubsub.cnrm.cloud.google.com"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
cd "${ROOT_DIR}"

# Make imports the repository .env for direct invocations as well as Make
# recipes. The file is gitignored; only its path and the credential filename
# are passed to kubectl, never the JSON contents.
ENV_FILE="${FAROS_KCC_ENV_FILE:-.env}"
dotenv_names=()
command -v sed >/dev/null || {
  echo "sed is required to load ${ENV_FILE}" >&2
  exit 1
}
if [[ -f "${ENV_FILE}" ]]; then
  while IFS= read -r env_name; do
    [[ -n "${env_name}" ]] && dotenv_names+=("${env_name}")
  done < <(sed -n -E 's/^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*[:?+]?=.*/\2/p' "${ENV_FILE}")
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
fi

# Make exports every assignment it finds in .env. Keep the values available to
# this script, but explicitly remove their export attributes before invoking
# curl/kubectl so unrelated credentials are never inherited by subprocesses.
dotenv_names+=(
  FAROS_CONFIG_CONNECTOR_GCP_PROJECT
  FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE
  FAROS_E2E_GCP_PROJECT
  FAROS_E2E_GCP_CREDENTIALS_FILE
)
for env_name in "${dotenv_names[@]}"; do
  export -n "${env_name}" 2>/dev/null || true
done

: "${FAROS_E2E_TILT_RUNTIME_KUBECONFIG:?FAROS_E2E_TILT_RUNTIME_KUBECONFIG is required}"
gcp_credentials_file="${FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE:-${FAROS_E2E_GCP_CREDENTIALS_FILE:-}}"
: "${gcp_credentials_file:?FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy FAROS_E2E_GCP_CREDENTIALS_FILE) is required}"

if [[ ! -f "${FAROS_E2E_TILT_RUNTIME_KUBECONFIG}" ]]; then
  echo "runtime kubeconfig does not exist: ${FAROS_E2E_TILT_RUNTIME_KUBECONFIG}" >&2
  exit 1
fi
if [[ ! -f "${gcp_credentials_file}" ]]; then
  echo "GCP credentials file does not exist" >&2
  exit 1
fi

for command in curl kubectl sha256sum tar mktemp; do
  command -v "${command}" >/dev/null || {
    echo "${command} is required" >&2
    exit 1
  }
done

task_tmp="$(mktemp -d)"
trap 'rm -rf "${task_tmp}"' EXIT
bundle="${task_tmp}/release-bundle.tar.gz"

echo ">>> downloading checksum-pinned Config Connector ${KCC_VERSION} bundle"
curl -fsSL "${KCC_BUNDLE_URL}" -o "${bundle}"
echo "${KCC_BUNDLE_SHA256}  ${bundle}" | sha256sum -c -
tar -xzf "${bundle}" -C "${task_tmp}"

kc=(kubectl --kubeconfig "${FAROS_E2E_TILT_RUNTIME_KUBECONFIG}")

echo ">>> applying Config Connector operator ${KCC_VERSION}"
"${kc[@]}" apply -f "${task_tmp}/operator-system/configconnector-operator.yaml"
"${kc[@]}" wait --for=condition=Established \
  crd/configconnectors.core.cnrm.cloud.google.com --timeout=2m
"${kc[@]}" -n configconnector-operator-system rollout status \
  statefulset/configconnector-operator --timeout=5m

echo ">>> importing the caller-supplied service-account key into ${KCC_NAMESPACE}/${KCC_SECRET}"
"${kc[@]}" create namespace "${KCC_NAMESPACE}" --dry-run=client -o yaml | "${kc[@]}" apply -f -
"${kc[@]}" -n "${KCC_NAMESPACE}" create secret generic "${KCC_SECRET}" \
  --from-file="key.json=${gcp_credentials_file}" \
  --dry-run=client -o yaml | "${kc[@]}" apply -f -

echo ">>> configuring Config Connector cluster mode"
"${kc[@]}" apply -f - <<EOF
apiVersion: core.cnrm.cloud.google.com/v1beta1
kind: ConfigConnector
metadata:
  name: ${KCC_NAME}
spec:
  mode: cluster
  credentialSecretName: ${KCC_SECRET}
  stateIntoSpec: Absent
EOF

"${kc[@]}" wait --for=jsonpath='{.status.healthy}'=true \
  "configconnector/${KCC_NAME}" --timeout=10m

echo ">>> waiting for Config Connector controllers and Pub/Sub CRD"
"${kc[@]}" -n "${KCC_NAMESPACE}" wait --for=condition=Ready pod --all --timeout=10m
"${kc[@]}" wait --for=condition=Established "crd/${PUBSUB_CRD}" --timeout=10m

echo ">>> Config Connector ${KCC_VERSION} is healthy and PubSubTopic is Established"
