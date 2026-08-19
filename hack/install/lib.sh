# shellcheck shell=bash
# Shared environment contract for the hack/install/ scripts.
#
# Every script sources this file. All values can be overridden from the
# environment, so the SAME scripts serve three audiences:
#   * docs/install-external-kcp.md and docs/install-embedded-kcp.md quote
#     these scripts step by step,
#   * developers run them by hand against a local kind cluster,
#   * the e2e suites (test/e2e/suites/installexternal, installembedded)
#     execute them verbatim in CI.
# Keep the scripts and the docs in sync: if you change a step here, update
# the corresponding doc section.

set -euo pipefail

# --- cluster -----------------------------------------------------------------
export FAROS_INSTALL_CLUSTER="${FAROS_INSTALL_CLUSTER:-faros}"
KUBE_CONTEXT="kind-${FAROS_INSTALL_CLUSTER}"

# Where extracted kubeconfigs and pidfiles land. Git-ignored.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export FAROS_INSTALL_STATE_DIR="${FAROS_INSTALL_STATE_DIR:-${REPO_ROOT}/.faros-install}"
mkdir -p "${FAROS_INSTALL_STATE_DIR}"

# --- kcp ---------------------------------------------------------------------
# Base DNS domain for kcp. The front-proxy is served at https://${KCP_DOMAIN}:8443,
# each shard at https://<shard>.${KCP_DOMAIN}:8443. The default *.localhost
# domain resolves to loopback on Linux and macOS, so together with a
# port-forward to the Envoy gateway no /etc/hosts entries are needed.
export KCP_DOMAIN="${KCP_DOMAIN:-kcp.localhost}"

# Name of the second shard (the root shard is always called "root").
export KCP_SHARD_2="${KCP_SHARD_2:-theseus}"

# Fixed ClusterIP the Envoy gateway Service claims. In-cluster clients (the
# kcp pods themselves, the faros hub) resolve ${KCP_DOMAIN} to this IP via
# hostAliases; external clients port-forward to the Service instead. Must be a
# free IP inside the cluster's Service CIDR (kind default: 10.96.0.0/16).
export KCP_GATEWAY_IP="${KCP_GATEWAY_IP:-10.96.2.2}"

# kcp-operator git ref to install (kustomize base from GitHub).
export KCP_OPERATOR_REF="${KCP_OPERATOR_REF:-main}"

export KCP_FEATURE_GATES="${KCP_FEATURE_GATES:-CacheAPIs=true,WorkspaceMounts=true}"

# --- infrastructure versions -------------------------------------------------
export CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.19.2}"
export ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.7.0}"
export ETCD_IMAGE="${ETCD_IMAGE:-gcr.io/etcd-development/etcd:v3.6.4}"

# --- auth --------------------------------------------------------------------
# Static bearer token shared by the hub and kcp. The hub derives a
# deterministic kcp identity from it (see pkg/hub/kcp/embedded.go):
#   user = faros:static:<first 16 hex chars of sha256("static-token/"+token)>
# The kcp token-auth CSV below MUST use the same mapping or requests
# authenticate at kcp as the wrong user and get 403.
export FAROS_STATIC_TOKEN="${FAROS_STATIC_TOKEN:-dev-token}"

# --- faros hub ---------------------------------------------------------------
export HUB_NAMESPACE="${HUB_NAMESPACE:-faros-system}"
# Hostname the hub is reachable at THROUGH the Envoy gateway (TLS passthrough).
export HUB_DOMAIN="${HUB_DOMAIN:-faros.${KCP_DOMAIN}}"
# External URL baked into kubeconfigs/callbacks. Locally this is the
# port-forward address (plain localhost — resolves everywhere, unlike
# *.localhost subdomains on macOS); in production your real hub hostname.
export HUB_EXTERNAL_URL="${HUB_EXTERNAL_URL:-https://localhost:9443}"
export HUB_CHART="${HUB_CHART:-${REPO_ROOT}/deploy/charts/faros-hub}"
export HUB_REPLICAS="${HUB_REPLICAS:-2}"
# Optional image override (e2e sets these to the locally-built test image).
export HUB_IMAGE="${HUB_IMAGE:-}"
export HUB_IMAGE_TAG="${HUB_IMAGE_TAG:-}"
export HUB_IMAGE_PULL_POLICY="${HUB_IMAGE_PULL_POLICY:-}"
# When "true", `kind load` the hub image into the cluster before installing.
export HUB_KIND_LOAD="${HUB_KIND_LOAD:-false}"

# --- helpers -----------------------------------------------------------------
kc() { kubectl --context "${KUBE_CONTEXT}" "$@"; }

require() {
  for tool in "$@"; do
    command -v "${tool}" >/dev/null 2>&1 || {
      echo "ERROR: required tool '${tool}' not found in PATH" >&2
      exit 1
    }
  done
}

# static_token_csv prints the kcp --token-auth-file CSV line for a token,
# using the same identity derivation as the faros hub.
static_token_csv() {
  local token="$1"
  local hash
  hash="$(printf 'static-token/%s' "${token}" | openssl dgst -sha256 | awk '{print $NF}' | cut -c1-16)"
  printf '%s,faros:static:%s,%s,"system:authenticated"\n' "${token}" "${hash}" "${hash}"
}
