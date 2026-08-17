#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# Apply one opt-in infrastructure Template to the provider workspace and prove
# that all publication layers expose the exact offering. Backend-specific
# wrappers set FAROS_CONTRIB_* so Config Connector, Terraform, and future
# integrations share one readiness contract.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
cd "${ROOT_DIR}"

: "${FAROS_E2E_TILT_KUBECONFIG:?FAROS_E2E_TILT_KUBECONFIG is required}"
: "${FAROS_CONTRIB_NAME:?FAROS_CONTRIB_NAME is required}"
: "${FAROS_CONTRIB_TEMPLATE_FILE:?FAROS_CONTRIB_TEMPLATE_FILE is required}"
: "${FAROS_CONTRIB_INSTANCE_RESOURCE:?FAROS_CONTRIB_INSTANCE_RESOURCE is required}"

if [[ ! -f "${FAROS_E2E_TILT_KUBECONFIG}" ]]; then
  echo "kcp kubeconfig does not exist: ${FAROS_E2E_TILT_KUBECONFIG}" >&2
  exit 1
fi

for command in kubectl grep sed sha256sum; do
  command -v "${command}" >/dev/null || {
    echo "${command} is required" >&2
    exit 1
  }
done

TEMPLATE_FILE="${FAROS_CONTRIB_TEMPLATE_FILE}"
WORKSPACE_PATH="${FAROS_CONTRIB_PROVIDER_WORKSPACE:-root:faros:providers:infrastructure}"
APIEXPORT_NAME="${FAROS_CONTRIB_APIEXPORT_NAME:-infrastructure.providers.faros.sh}"
INSTANCE_RESOURCE="${FAROS_CONTRIB_INSTANCE_RESOURCE}"
INSTANCE_GROUP="${FAROS_CONTRIB_INSTANCE_GROUP:-infrastructure.faros.sh}"
CACHED_RESOURCE_NAME="${FAROS_CONTRIB_CACHED_RESOURCE_NAME:-publish-templates}"
RUNTIME_KUBECONFIG="${FAROS_E2E_TILT_RUNTIME_KUBECONFIG:-.faros-cluster.kubeconfig}"
WAIT="${FAROS_CONTRIB_WAIT:-15m}"
WAIT_SECONDS="${FAROS_CONTRIB_WAIT_SECONDS:-900}"
POLL_SECONDS="${FAROS_CONTRIB_POLL_SECONDS:-5}"

if ! [[ "${WAIT_SECONDS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAROS_CONTRIB_WAIT_SECONDS must be a positive integer" >&2
  exit 1
fi
if ! [[ "${POLL_SECONDS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAROS_CONTRIB_POLL_SECONDS must be a positive integer" >&2
  exit 1
fi
if [[ ! -f "${TEMPLATE_FILE}" ]]; then
  echo "${FAROS_CONTRIB_NAME} Template fixture does not exist: ${TEMPLATE_FILE}" >&2
  exit 1
fi
if [[ ! -f "${RUNTIME_KUBECONFIG}" ]]; then
  echo "runtime kubeconfig does not exist: ${RUNTIME_KUBECONFIG}" >&2
  exit 1
fi

kcp_server="${FAROS_CONTRIB_KCP_SERVER:-}"
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
  echo "${FAROS_CONTRIB_NAME} Template fixture has no metadata.name" >&2
  exit 1
fi

echo ">>> enabling ${FAROS_CONTRIB_NAME} Template ${template_name} in ${WORKSPACE_PATH}"
"${kcp[@]}" apply --validate=false -f "${TEMPLATE_FILE}"

deadline=$((SECONDS + WAIT_SECONDS))
template_generation=""
template_observed_generation=""
template_ready=""
echo ">>> waiting for Template ${template_name} to reconcile its current generation"
while (( SECONDS < deadline )); do
  template_state="$("${kcp[@]}" get template "${template_name}" \
    -o jsonpath='{.metadata.generation}|{.status.observedGeneration}|{.status.conditions[?(@.type=="Ready")].status}' \
    2>/dev/null || true)"
  IFS='|' read -r template_generation template_observed_generation template_ready <<<"${template_state}"
  if [[ -n "${template_generation}" && "${template_observed_generation}" == "${template_generation}" && \
    "${template_ready}" == "True" ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ -z "${template_generation}" || "${template_observed_generation}" != "${template_generation}" || \
  "${template_ready}" != "True" ]]; then
  echo "Template ${template_name} did not reconcile its current generation: generation=${template_generation:-unknown} observed=${template_observed_generation:-unknown} Ready=${template_ready:-unknown}" >&2
  exit 1
fi

deadline=$((SECONDS + WAIT_SECONDS))
resources=""
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

source_state="$("${kcp[@]}" get template "${template_name}" \
  -o jsonpath='{.metadata.annotations.kcp\.io/cluster}|{.metadata.generation}')"
IFS='|' read -r source_cluster source_generation <<<"${source_state}"
source_spec="$("${kcp[@]}" get template "${template_name}" -o jsonpath='{.spec}')"
source_spec_hash="$(printf '%s' "${source_spec}" | sha256sum | sed -E 's/[[:space:]].*$//')"
replication_endpoint="$("${kcp[@]}" get cachedresourceendpointslice "${CACHED_RESOURCE_NAME}" -o jsonpath='{.status.endpoints[0].url}')"
if [[ -z "${source_cluster}" || -z "${source_generation}" || -z "${source_spec_hash}" || -z "${replication_endpoint}" ]]; then
  echo "CachedResource ${CACHED_RESOURCE_NAME} is missing its source identity or replication endpoint" >&2
  exit 1
fi

replication=(kubectl --kubeconfig "${FAROS_E2E_TILT_KUBECONFIG}" \
  --server="${replication_endpoint%/}/clusters/${source_cluster}" --insecure-skip-tls-verify)
deadline=$((SECONDS + WAIT_SECONDS))
cached_template_name=""
cached_template_generation=""
cached_template_spec_hash=""
echo ">>> verifying ${template_name} through the CachedResource replication endpoint"
while (( SECONDS < deadline )); do
  cached_template_state="$("${replication[@]}" get template "${template_name}" \
    -o jsonpath='{.metadata.name}|{.metadata.generation}' 2>/dev/null || true)"
  IFS='|' read -r cached_template_name cached_template_generation <<<"${cached_template_state}"
  cached_template_spec="$("${replication[@]}" get template "${template_name}" -o jsonpath='{.spec}' 2>/dev/null || true)"
  cached_template_spec_hash="$(printf '%s' "${cached_template_spec}" | sha256sum | sed -E 's/[[:space:]].*$//')"
  if [[ "${cached_template_name}" == "${template_name}" && "${cached_template_generation}" == "${source_generation}" && \
    "${cached_template_spec_hash}" == "${source_spec_hash}" ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ "${cached_template_name}" != "${template_name}" || "${cached_template_generation}" != "${source_generation}" || \
  "${cached_template_spec_hash}" != "${source_spec_hash}" ]]; then
  echo "Template ${template_name} current content is absent from CachedResource ${CACHED_RESOURCE_NAME}: sourceGeneration=${source_generation} cachedGeneration=${cached_template_generation:-unknown} sourceSpec=${source_spec_hash} cachedSpec=${cached_template_spec_hash:-unknown}" >&2
  echo "The provider object is Ready, but tenant catalogs cannot see its current content; inspect root-kcp cache-controller logs." >&2
  exit 1
fi

echo ">>> waiting for runtime KRO ResourceGraphDefinition ${template_name}"
deadline=$((SECONDS + WAIT_SECONDS))
rgd_generation=""
rgd_observed_generation=""
rgd_accepted=""
while (( SECONDS < deadline )); do
  rgd_state="$("${runtime[@]}" get resourcegraphdefinition "${template_name}" \
    -o jsonpath='{.metadata.generation}|{.status.conditions[?(@.type=="GraphAccepted")].observedGeneration}|{.status.conditions[?(@.type=="GraphAccepted")].status}' \
    2>/dev/null || true)"
  IFS='|' read -r rgd_generation rgd_observed_generation rgd_accepted <<<"${rgd_state}"
  if [[ -n "${rgd_generation}" && "${rgd_observed_generation}" == "${rgd_generation}" && \
    "${rgd_accepted}" == "True" ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ -z "${rgd_generation}" || "${rgd_observed_generation}" != "${rgd_generation}" || \
  "${rgd_accepted}" != "True" ]]; then
  echo "ResourceGraphDefinition ${template_name} did not accept its current generation: generation=${rgd_generation:-unknown} observed=${rgd_observed_generation:-unknown} GraphAccepted=${rgd_accepted:-unknown}" >&2
  exit 1
fi

echo ">>> ${FAROS_CONTRIB_NAME} Template ${template_name} is enabled, cached, and GraphAccepted"
