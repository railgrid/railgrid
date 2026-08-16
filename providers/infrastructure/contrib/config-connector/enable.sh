#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# Apply the checked-in Pub/Sub Template to the infrastructure provider
# workspace, then wait until the provider APIExport, tenant-facing Template
# cache, and runtime KRO graph expose it. This is a separate manual action from
# installing Config Connector.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
cd "${ROOT_DIR}"

: "${FAROS_E2E_TILT_KUBECONFIG:?FAROS_E2E_TILT_KUBECONFIG is required}"

if [[ ! -f "${FAROS_E2E_TILT_KUBECONFIG}" ]]; then
  echo "kcp kubeconfig does not exist: ${FAROS_E2E_TILT_KUBECONFIG}" >&2
  exit 1
fi

for command in kubectl grep sed; do
  command -v "${command}" >/dev/null || {
    echo "${command} is required" >&2
    exit 1
  }
done

TEMPLATE_FILE="${FAROS_KCC_TEMPLATE_FILE:-providers/infrastructure/contrib/config-connector/pubsub-template.yaml}"
WORKSPACE_PATH="${FAROS_KCC_PROVIDER_WORKSPACE:-root:faros:providers:infrastructure}"
APIEXPORT_NAME="${FAROS_KCC_APIEXPORT_NAME:-infrastructure.providers.faros.sh}"
INSTANCE_RESOURCE="${FAROS_KCC_INSTANCE_RESOURCE:-gcppubsubtopics}"
INSTANCE_GROUP="${FAROS_KCC_INSTANCE_GROUP:-infrastructure.faros.sh}"
CACHED_RESOURCE_NAME="${FAROS_KCC_CACHED_RESOURCE_NAME:-publish-templates}"
RUNTIME_KUBECONFIG="${FAROS_E2E_TILT_RUNTIME_KUBECONFIG:-.faros-cluster.kubeconfig}"
WAIT="${FAROS_KCC_WAIT:-15m}"
WAIT_SECONDS="${FAROS_KCC_WAIT_SECONDS:-900}"
POLL_SECONDS="${FAROS_KCC_POLL_SECONDS:-5}"

if ! [[ "${WAIT_SECONDS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAROS_KCC_WAIT_SECONDS must be a positive integer" >&2
  exit 1
fi
if ! [[ "${POLL_SECONDS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAROS_KCC_POLL_SECONDS must be a positive integer" >&2
  exit 1
fi

if [[ ! -f "${TEMPLATE_FILE}" ]]; then
  echo "Config Connector Template fixture does not exist: ${TEMPLATE_FILE}" >&2
  exit 1
fi
if [[ ! -f "${RUNTIME_KUBECONFIG}" ]]; then
  echo "runtime kubeconfig does not exist: ${RUNTIME_KUBECONFIG}" >&2
  exit 1
fi

kcp_server="${FAROS_KCC_KCP_SERVER:-}"
if [[ -z "${kcp_server}" ]]; then
  kcp_server="$({ kubectl --kubeconfig "${FAROS_E2E_TILT_KUBECONFIG}" config view --minify -o jsonpath='{.clusters[0].cluster.server}'; printf '\n'; } | sed -E 's#/clusters/.*$##')"
fi
kcp_server="${kcp_server%/}"
if [[ -z "${kcp_server}" ]]; then
  echo "could not determine the kcp front-proxy server" >&2
  exit 1
fi

kcp=(kubectl --kubeconfig "${FAROS_E2E_TILT_KUBECONFIG}" \
  --server="${kcp_server}/clusters/${WORKSPACE_PATH}" --insecure-skip-tls-verify)
runtime=(kubectl --kubeconfig "${RUNTIME_KUBECONFIG}")

template_name="$("${kcp[@]}" apply --dry-run=client --validate=false -f "${TEMPLATE_FILE}" -o jsonpath='{.metadata.name}')"
if [[ -z "${template_name}" ]]; then
  echo "Config Connector Template fixture has no metadata.name" >&2
  exit 1
fi

echo ">>> enabling Config Connector Template ${template_name} in ${WORKSPACE_PATH}"
"${kcp[@]}" apply --validate=false -f "${TEMPLATE_FILE}"
"${kcp[@]}" wait \
  --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
  "template/${template_name}" --timeout="${WAIT}"

deadline=$((SECONDS + WAIT_SECONDS))
echo ">>> waiting for ${APIEXPORT_NAME} to publish ${INSTANCE_GROUP}/${INSTANCE_RESOURCE}"
while (( SECONDS < deadline )); do
  resources="$("${kcp[@]}" get apiexport "${APIEXPORT_NAME}" \
    -o jsonpath='{range .spec.resources[*]}{.group}/{.name}{"\n"}{end}' 2>/dev/null || true)"
  if grep -Fxq "${INSTANCE_GROUP}/${INSTANCE_RESOURCE}" <<<"${resources}"; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if ! grep -Fxq "${INSTANCE_GROUP}/${INSTANCE_RESOURCE}" <<<"${resources}"; then
  echo "APIExport ${APIEXPORT_NAME} never published ${INSTANCE_GROUP}/${INSTANCE_RESOURCE}" >&2
  exit 1
fi

deadline=$((SECONDS + WAIT_SECONDS))
cache_phase=""
local_count=""
cached_count=""
echo ">>> waiting for CachedResource ${CACHED_RESOURCE_NAME} to converge"
while (( SECONDS < deadline )); do
  cache_state="$("${kcp[@]}" get cachedresource "${CACHED_RESOURCE_NAME}" \
    -o jsonpath='{.status.phase}|{.status.resourceCounts.local}|{.status.resourceCounts.cache}' \
    2>/dev/null || true)"
  IFS='|' read -r cache_phase local_count cached_count <<<"${cache_state}"
  if [[ "${cache_phase}" == "Ready" && "${local_count}" =~ ^[0-9]+$ && \
    "${cached_count}" =~ ^[0-9]+$ && "${local_count}" == "${cached_count}" ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ "${cache_phase}" != "Ready" || ! "${local_count}" =~ ^[0-9]+$ || \
  ! "${cached_count}" =~ ^[0-9]+$ || "${local_count}" != "${cached_count}" ]]; then
  echo "CachedResource ${CACHED_RESOURCE_NAME} did not converge: phase=${cache_phase:-unknown} local=${local_count:-unknown} cache=${cached_count:-unknown}" >&2
  echo "Inspect root-kcp logs for kcp-cached-resources-controller errors; a stopped shared informer requires KCP controller recovery." >&2
  exit 1
fi

source_cluster="$("${kcp[@]}" get template "${template_name}" \
  -o jsonpath='{.metadata.annotations.kcp\.io/cluster}')"
replication_endpoint="$("${kcp[@]}" get cachedresourceendpointslice "${CACHED_RESOURCE_NAME}" \
  -o jsonpath='{.status.endpoints[0].url}')"
if [[ -z "${source_cluster}" || -z "${replication_endpoint}" ]]; then
  echo "CachedResource ${CACHED_RESOURCE_NAME} is missing its source cluster or replication endpoint" >&2
  exit 1
fi

replication=(kubectl --kubeconfig "${FAROS_E2E_TILT_KUBECONFIG}" \
  --server="${replication_endpoint%/}/clusters/${source_cluster}" --insecure-skip-tls-verify)
deadline=$((SECONDS + WAIT_SECONDS))
cached_template_name=""
echo ">>> verifying ${template_name} through the CachedResource replication endpoint"
while (( SECONDS < deadline )); do
  cached_template_name="$("${replication[@]}" get template "${template_name}" \
    -o jsonpath='{.metadata.name}' 2>/dev/null || true)"
  if [[ "${cached_template_name}" == "${template_name}" ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ "${cached_template_name}" != "${template_name}" ]]; then
  echo "Template ${template_name} is absent from CachedResource ${CACHED_RESOURCE_NAME} replication storage" >&2
  echo "The provider object is Ready, but tenant catalogs cannot see it; inspect root-kcp cache-controller logs." >&2
  exit 1
fi

echo ">>> waiting for runtime KRO ResourceGraphDefinition ${template_name}"
"${runtime[@]}" wait \
  --for=jsonpath='{.status.conditions[?(@.type=="GraphAccepted")].status}'=True \
  "resourcegraphdefinition/${template_name}" --timeout="${WAIT}"

echo ">>> Config Connector Template ${template_name} is enabled, cached, and GraphAccepted"
