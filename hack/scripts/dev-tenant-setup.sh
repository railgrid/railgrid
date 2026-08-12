#!/usr/bin/env bash
# Create the tenant-scoped credentials and connection objects used by the
# local App Studio / Code / Databricks workflow.
#
# The script reads shell-compatible values from the repository-root .env file,
# which is gitignored. Secret values stay in the shell, protected temporary
# files, or stdin: they are never passed as kubectl/curl arguments or rendered
# into the checked-in manifests.
#
# Provider ordering is intentional. Tilt makes code-init and
# databricks-init prerequisites of this resource, and this script additionally
# waits for both tenant APIBindings to be Bound before creating Connections.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

die() {
  echo "dev tenant setup: $*" >&2
  exit 1
}

ENV_FILE="${FAROS_DEV_TENANT_ENV_FILE:-.env}"
if [[ ! -f "${ENV_FILE}" ]]; then
  die "missing ${ENV_FILE}. Copy hack/dev/tenant-bootstrap.env.example to .env and fill it in."
fi

if ! source "${ENV_FILE}"; then
  die "could not load ${ENV_FILE}; check that it contains valid shell assignments"
fi

# Do not export values loaded from .env to kubectl, jq, or curl. A variable
# that was already exported by the caller keeps that attribute after source,
# so explicitly remove it for every credential this script handles.
for sensitive_name in \
  FAROS_BOOTSTRAP_STATIC_TOKEN \
  STATIC_AUTH_TOKEN \
  FAROS_BOOTSTRAP_GITHUB_TOKEN \
  FAROS_BOOTSTRAP_LLM_API_KEY \
  FAROS_BOOTSTRAP_DATABRICKS_TOKEN; do
  export -n "${sensitive_name}" 2>/dev/null || true
done

command -v kubectl >/dev/null || die "kubectl is required"
command -v curl >/dev/null || die "curl is required to log in to the local hub"
command -v jq >/dev/null || die "jq is required to render the login response and Connection objects"
command -v base64 >/dev/null || die "base64 is required to decode the tenant kubeconfig"
command -v mktemp >/dev/null || die "mktemp is required to protect bootstrap credentials"

require_value() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    die "missing required ${name} in ${ENV_FILE}"
  fi
}

require_value FAROS_BOOTSTRAP_GITHUB_OWNER
require_value FAROS_BOOTSTRAP_GITHUB_TOKEN
require_value FAROS_BOOTSTRAP_LLM_API_KEY
require_value FAROS_BOOTSTRAP_DATABRICKS_HOST
require_value FAROS_BOOTSTRAP_DATABRICKS_TOKEN

GITHUB_CONNECTION_NAME="${FAROS_BOOTSTRAP_GITHUB_CONNECTION_NAME:-github}"
GITHUB_SECRET_NAME="${FAROS_BOOTSTRAP_GITHUB_SECRET_NAME:-${GITHUB_CONNECTION_NAME}-credentials}"
LLM_PROVIDER="${FAROS_BOOTSTRAP_LLM_PROVIDER:-openai-compatible}"
LLM_BASE_URL="${FAROS_BOOTSTRAP_LLM_BASE_URL:-https://api.openai.com/v1}"
LLM_MODEL="${FAROS_BOOTSTRAP_LLM_MODEL:-gpt-5.4}"
DATABRICKS_CONNECTION_NAME="${FAROS_BOOTSTRAP_DATABRICKS_CONNECTION_NAME:-databricks}"
DATABRICKS_SECRET_NAME="${FAROS_BOOTSTRAP_DATABRICKS_SECRET_NAME:-${DATABRICKS_CONNECTION_NAME}-credentials}"

HUB_URL="${FAROS_BOOTSTRAP_HUB_URL:-https://localhost:9443}"
STATIC_TOKEN="${FAROS_BOOTSTRAP_STATIC_TOKEN:-${STATIC_AUTH_TOKEN:-dev-token}}"
TENANT_KUBECONFIG="${FAROS_BOOTSTRAP_KUBECONFIG:-.kcp/dev-tenant.kubeconfig}"

RETRY_ATTEMPTS="${FAROS_BOOTSTRAP_RETRIES:-30}"
RETRY_DELAY_SECONDS="${FAROS_BOOTSTRAP_RETRY_DELAY_SECONDS:-2}"
REQUEST_TIMEOUT_SECONDS="${FAROS_BOOTSTRAP_REQUEST_TIMEOUT_SECONDS:-10}"
BINDING_RETRIES="${FAROS_BOOTSTRAP_BINDING_RETRIES:-${RETRY_ATTEMPTS}}"

require_positive_integer() {
  local name="$1"
  local value="${!name}"
  if [[ ! "${value}" =~ ^[0-9]+$ ]] || (( value < 1 )); then
    die "${name} must be a positive integer (got ${value@Q})"
  fi
}

require_nonnegative_integer() {
  local name="$1"
  local value="${!name}"
  if [[ ! "${value}" =~ ^[0-9]+$ ]]; then
    die "${name} must be a non-negative integer (got ${value@Q})"
  fi
}

require_positive_integer RETRY_ATTEMPTS
require_nonnegative_integer RETRY_DELAY_SECONDS
require_positive_integer REQUEST_TIMEOUT_SECONDS
require_positive_integer BINDING_RETRIES

HUB_BASE="${HUB_URL%/}"
HUB_AUTHORITY=""
HUB_HOST=""

validate_hub_url() {
  if [[ ! "${HUB_URL}" =~ ^https://([^/?#]+)(/[^?#]*)?$ ]]; then
    die "FAROS_BOOTSTRAP_HUB_URL must be an HTTPS URL without credentials, query, or fragment (got ${HUB_URL@Q})"
  fi

  HUB_AUTHORITY="${BASH_REMATCH[1]}"
  if [[ "${HUB_AUTHORITY}" =~ ^\[([0-9A-Fa-f:]+)\](:[0-9]{1,5})?$ ]]; then
    HUB_HOST="${BASH_REMATCH[1]}"
  elif [[ "${HUB_AUTHORITY}" =~ ^([[:alnum:].-]+)(:[0-9]{1,5})?$ ]]; then
    HUB_HOST="${BASH_REMATCH[1]}"
  else
    die "FAROS_BOOTSTRAP_HUB_URL has an invalid host or port (got ${HUB_URL@Q})"
  fi
}

is_local_hub_host() {
  local host="${HUB_HOST,,}"
  case "${host}" in
    localhost|*.localhost|127.0.0.1|::1)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

validate_hub_url

CURL_TLS_ARGS=()
CA_CERT="${FAROS_BOOTSTRAP_CA_CERT:-}"
INSECURE_TLS="${FAROS_BOOTSTRAP_INSECURE_TLS:-false}"

case "${INSECURE_TLS,,}" in
  true|yes|1)
    if ! is_local_hub_host; then
      die "refusing insecure TLS for non-local hub ${HUB_HOST}; set FAROS_BOOTSTRAP_CA_CERT to a trusted CA instead"
    fi
    CURL_TLS_ARGS+=(--insecure --noproxy '*')
    ;;
  false|no|0|'')
    ;;
  *)
    die "FAROS_BOOTSTRAP_INSECURE_TLS must be true or false (got ${INSECURE_TLS@Q})"
    ;;
esac

if [[ -n "${CA_CERT}" ]]; then
  if [[ "${INSECURE_TLS,,}" == true || "${INSECURE_TLS,,}" == yes || "${INSECURE_TLS}" == 1 ]]; then
    die "set only one of FAROS_BOOTSTRAP_CA_CERT and FAROS_BOOTSTRAP_INSECURE_TLS"
  fi
  [[ -f "${CA_CERT}" && -r "${CA_CERT}" ]] || die "FAROS_BOOTSTRAP_CA_CERT is not a readable file: ${CA_CERT}"
  CURL_TLS_ARGS+=(--cacert "${CA_CERT}")
fi

# Keep every temporary file private. In particular, LOGIN_RESPONSE and the
# generated tenant kubeconfig contain bearer credentials.
umask 077
PRIVATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/faros-dev-tenant.XXXXXX")"
chmod 700 "${PRIVATE_DIR}"

LOGIN_RESPONSE="${PRIVATE_DIR}/login-response.json"
CURL_ERROR="${PRIVATE_DIR}/curl.error"
KUBECTL_ERROR="${PRIVATE_DIR}/kubectl.error"
TENANT_KUBECONFIG_TMP="${PRIVATE_DIR}/tenant.kubeconfig"
GITHUB_TOKEN_FILE="${PRIVATE_DIR}/github-token"
LLM_PROVIDER_FILE="${PRIVATE_DIR}/llm-provider"
LLM_BASE_URL_FILE="${PRIVATE_DIR}/llm-base-url"
LLM_MODEL_FILE="${PRIVATE_DIR}/llm-model"
LLM_API_KEY_FILE="${PRIVATE_DIR}/llm-api-key"
DATABRICKS_TOKEN_FILE="${PRIVATE_DIR}/databricks-token"

cleanup() {
  rm -f \
    "${LOGIN_RESPONSE}" \
    "${CURL_ERROR}" \
    "${KUBECTL_ERROR}" \
    "${TENANT_KUBECONFIG_TMP}" \
    "${GITHUB_TOKEN_FILE}" \
    "${LLM_PROVIDER_FILE}" \
    "${LLM_BASE_URL_FILE}" \
    "${LLM_MODEL_FILE}" \
    "${LLM_API_KEY_FILE}" \
    "${DATABRICKS_TOKEN_FILE}"
  rmdir "${PRIVATE_DIR}" 2>/dev/null || true
}
trap cleanup EXIT

write_protected_file() {
  local path="$1"
  local value="$2"
  printf '%s' "${value}" >"${path}"
  chmod 600 "${path}"
}

write_protected_file "${GITHUB_TOKEN_FILE}" "${FAROS_BOOTSTRAP_GITHUB_TOKEN}"
write_protected_file "${LLM_PROVIDER_FILE}" "${LLM_PROVIDER}"
write_protected_file "${LLM_BASE_URL_FILE}" "${LLM_BASE_URL}"
write_protected_file "${LLM_MODEL_FILE}" "${LLM_MODEL}"
write_protected_file "${LLM_API_KEY_FILE}" "${FAROS_BOOTSTRAP_LLM_API_KEY}"
write_protected_file "${DATABRICKS_TOKEN_FILE}" "${FAROS_BOOTSTRAP_DATABRICKS_TOKEN}"

CURL_COMMON_ARGS=(
  --silent
  --show-error
  --connect-timeout "${REQUEST_TIMEOUT_SECONDS}"
  --max-time "${REQUEST_TIMEOUT_SECONDS}"
)

curl_error_detail() {
  local detail=""
  if [[ -s "${CURL_ERROR}" ]]; then
    detail="$(<"${CURL_ERROR}")"
    detail="${detail//$'\n'/ }"
    detail="${detail//$'\r'/ }"
  fi
  if [[ -n "${detail}" ]]; then
    printf '%s' "${detail:0:240}"
  else
    printf '%s' "no curl error detail"
  fi
}

kubectl_error_detail() {
  local detail=""
  if [[ -s "${KUBECTL_ERROR}" ]]; then
    detail="$(<"${KUBECTL_ERROR}")"
    detail="${detail//$'\n'/ }"
    detail="${detail//$'\r'/ }"
  fi
  if [[ -n "${detail}" ]]; then
    printf '%s' "${detail:0:300}"
  else
    printf '%s' "no kubectl error detail"
  fi
}

probe_hub() {
  local path="$1"
  curl \
    "${CURL_COMMON_ARGS[@]}" \
    "${CURL_TLS_ARGS[@]}" \
    --fail \
    --output /dev/null \
    "${HUB_BASE}${path}" \
    2>"${CURL_ERROR}"
}

wait_for_hub_ready() {
  local attempt
  local health_ok=0
  local last_error=""

  echo "Waiting for hub readiness at ${HUB_BASE}..."
  for ((attempt = 1; attempt <= RETRY_ATTEMPTS; attempt++)); do
    if probe_hub /healthz; then
      health_ok=1
      if probe_hub /readyz; then
        return 0
      fi
      last_error="/healthz is OK but /readyz is not ready: $(curl_error_detail)"
    else
      health_ok=0
      last_error="could not reach /healthz: $(curl_error_detail)"
    fi

    if (( attempt < RETRY_ATTEMPTS )); then
      sleep "${RETRY_DELAY_SECONDS}"
    fi
  done

  if (( health_ok == 1 )); then
    echo "Hub is reachable but did not become ready after ${RETRY_ATTEMPTS} attempts." >&2
    echo "The hub may still be bootstrapping kcp; inspect the faros-hub Tilt resource and retry." >&2
  else
    echo "Hub preflight failed after ${RETRY_ATTEMPTS} attempts: ${last_error}" >&2
    echo "Check that Tilt is running, faros-hub is listening, and FAROS_BOOTSTRAP_HUB_URL is correct." >&2
  fi
  if [[ "${INSECURE_TLS,,}" == true || "${INSECURE_TLS,,}" == yes || "${INSECURE_TLS}" == 1 ]]; then
    echo "Insecure TLS is enabled only for the validated local host ${HUB_HOST}." >&2
  else
    echo "TLS verification is enabled; use FAROS_BOOTSTRAP_CA_CERT for a local CA or explicitly enable insecure TLS for localhost only." >&2
  fi
  return 1
}

login_to_hub() {
  local attempt
  local curl_status
  local http_status
  local response_message
  local last_error=""

  echo "Logging in to the local hub..."
  for ((attempt = 1; attempt <= RETRY_ATTEMPTS; attempt++)); do
    : >"${LOGIN_RESPONSE}"
    : >"${CURL_ERROR}"

    # curl reads the bearer header from stdin. The token is therefore absent
    # from curl's process argv and from the environment inherited by curl.
    if http_status="$(
      printf 'Authorization: Bearer %s\n' "${STATIC_TOKEN}" |
        curl \
          "${CURL_COMMON_ARGS[@]}" \
          "${CURL_TLS_ARGS[@]}" \
          --request POST \
          --header @- \
          --output "${LOGIN_RESPONSE}" \
          --write-out '%{http_code}' \
          "${HUB_BASE}/auth/token-login" \
          2>"${CURL_ERROR}"
    )"; then
      curl_status=0
    else
      curl_status=$?
    fi

    if (( curl_status == 0 )) && [[ "${http_status}" == 2?? ]] &&
      jq -e '(.kubeconfig | strings | length > 0)' "${LOGIN_RESPONSE}" >/dev/null 2>&1; then
      return 0
    fi

    response_message="$(jq -r '.message // .status // empty' "${LOGIN_RESPONSE}" 2>/dev/null || true)"
    if [[ -n "${response_message}" ]]; then
      last_error="HTTP ${http_status:-000}: ${response_message}"
    elif (( curl_status != 0 )); then
      last_error="${http_status:-000}: $(curl_error_detail)"
    else
      last_error="HTTP ${http_status:-000}: response did not contain a kubeconfig"
    fi

    if (( attempt < RETRY_ATTEMPTS )); then
      sleep "${RETRY_DELAY_SECONDS}"
    fi
  done

  echo "Token login failed after ${RETRY_ATTEMPTS} attempts: ${last_error}" >&2
  echo "A fresh hub can answer /readyz before its users APIBinding settles; retry with a larger FAROS_BOOTSTRAP_RETRIES if startup is still progressing." >&2
  return 1
}

decode_tenant_kubeconfig() {
  local target_dir
  target_dir="$(dirname "${TENANT_KUBECONFIG}")"
  mkdir -p "${target_dir}"

  if ! jq -er '.kubeconfig | strings | select(length > 0)' "${LOGIN_RESPONSE}" |
    base64 --decode >"${TENANT_KUBECONFIG_TMP}"; then
    die "hub returned an invalid base64 kubeconfig"
  fi
  chmod 600 "${TENANT_KUBECONFIG_TMP}"
  mv "${TENANT_KUBECONFIG_TMP}" "${TENANT_KUBECONFIG}"
  chmod 600 "${TENANT_KUBECONFIG}"
  export KUBECONFIG="${TENANT_KUBECONFIG}"
}

wait_for_tenant_api() {
  local attempt
  local last_error=""

  for ((attempt = 1; attempt <= RETRY_ATTEMPTS; attempt++)); do
    : >"${KUBECTL_ERROR}"
    if kubectl --kubeconfig="${TENANT_KUBECONFIG}" get namespace default -o name 2>"${KUBECTL_ERROR}" >/dev/null; then
      return 0
    fi
    last_error="$(kubectl_error_detail)"
    if (( attempt < RETRY_ATTEMPTS )); then
      sleep "${RETRY_DELAY_SECONDS}"
    fi
  done

  die "login succeeded, but the tenant kubeconfig was not usable after ${RETRY_ATTEMPTS} attempts: ${last_error}"
}

wait_for_provider_binding() {
  local provider="$1"
  local expected_path="$2"
  local expected_name="$3"
  local attempt
  local binding_json
  local phase
  local export_path
  local export_name
  local condition_message
  local last_error=""

  echo "Waiting for the ${provider} tenant APIBinding to be Bound..."
  for ((attempt = 1; attempt <= BINDING_RETRIES; attempt++)); do
    : >"${KUBECTL_ERROR}"
    if binding_json="$(kubectl --kubeconfig="${TENANT_KUBECONFIG}" get apibinding "${provider}" -o json 2>"${KUBECTL_ERROR}")"; then
      phase="$(jq -r '.status.phase // empty' <<<"${binding_json}")"
      export_path="$(jq -r '.spec.reference.export.path // empty' <<<"${binding_json}")"
      export_name="$(jq -r '.spec.reference.export.name // empty' <<<"${binding_json}")"
      condition_message="$(jq -r '([.status.conditions[]? | .message? | select(type == "string" and length > 0)] | first) // empty' <<<"${binding_json}")"

      if [[ "${phase}" == Bound && "${export_path}" == "${expected_path}" && "${export_name}" == "${expected_name}" ]]; then
        return 0
      fi

      last_error="APIBinding ${provider} has phase=${phase:-unknown}, export=${export_path:-unknown}/${export_name:-unknown}"
      if [[ -n "${condition_message}" ]]; then
        last_error+=" (${condition_message})"
      fi
    else
      last_error="$(kubectl_error_detail)"
    fi

    if (( attempt < BINDING_RETRIES )); then
      sleep "${RETRY_DELAY_SECONDS}"
    fi
  done

  echo "${provider} APIBinding is unavailable or not Bound after ${BINDING_RETRIES} attempts." >&2
  echo "Expected ${provider} -> ${expected_path}/${expected_name}." >&2
  echo "Enable ${provider} in the active tenant workspace, wait for Bound, then rerun dev-tenant-setup." >&2
  echo "Last APIBinding check: ${last_error}" >&2
  return 1
}

apply_secret_from_file() {
  local name="$1"
  local key="$2"
  local value_file="$3"

  kubectl --kubeconfig="${TENANT_KUBECONFIG}" create secret generic "${name}" \
    --namespace=default \
    "--from-file=${key}=${value_file}" \
    --dry-run=client -o yaml |
    kubectl --kubeconfig="${TENANT_KUBECONFIG}" apply -f - >/dev/null
}

apply_connection_manifest() {
  local provider="$1"
  local manifest="$2"
  local attempt
  local last_error=""

  for ((attempt = 1; attempt <= BINDING_RETRIES; attempt++)); do
    : >"${KUBECTL_ERROR}"
    if kubectl --kubeconfig="${TENANT_KUBECONFIG}" apply -f "${manifest}" >/dev/null 2>"${KUBECTL_ERROR}" &&
      kubectl --kubeconfig="${TENANT_KUBECONFIG}" get -f "${manifest}" -o name >/dev/null 2>"${KUBECTL_ERROR}"; then
      return 0
    fi
    last_error="$(kubectl_error_detail)"
    if (( attempt < BINDING_RETRIES )); then
      sleep "${RETRY_DELAY_SECONDS}"
    fi
  done

  die "could not apply and verify the ${provider} Connection after ${BINDING_RETRIES} attempts: ${last_error}"
}

GITHUB_CONNECTION_MANIFEST="${PRIVATE_DIR}/github-connection.yaml"
DATABRICKS_CONNECTION_MANIFEST="${PRIVATE_DIR}/databricks-connection.yaml"

validate_hub_ready() {
  wait_for_hub_ready || exit 1
  login_to_hub || exit 1
  decode_tenant_kubeconfig
  wait_for_tenant_api
}

validate_hub_ready

# A provider binding is a hard precondition for its CRD/APIResourceSchema. Do
# this before writing any tenant secrets so a missing enablement cannot result
# in a misleading "setup applied" message or a half-created Connection flow.
wait_for_provider_binding \
  code \
  root:faros:providers:code \
  code.providers.faros.sh || exit 1
wait_for_provider_binding \
  databricks \
  root:faros:providers:databricks \
  databricks.providers.faros.sh || exit 1

echo "Applying provider-code GitHub credential and Connection..."
apply_secret_from_file "${GITHUB_SECRET_NAME}" token "${GITHUB_TOKEN_FILE}"

jq -n \
  --arg name "${GITHUB_CONNECTION_NAME}" \
  --arg owner "${FAROS_BOOTSTRAP_GITHUB_OWNER}" \
  --arg secret "${GITHUB_SECRET_NAME}" \
  '{
    apiVersion: "code.faros.sh/v1alpha1",
    kind: "Connection",
    metadata: {name: $name},
    spec: {
      provider: "github",
      type: "pat",
      owner: $owner,
      secretRef: {name: $secret, namespace: "default", key: "token"}
    }
  }' >"${GITHUB_CONNECTION_MANIFEST}"
chmod 600 "${GITHUB_CONNECTION_MANIFEST}"
apply_connection_manifest code "${GITHUB_CONNECTION_MANIFEST}"

echo "Applying App Studio LLM credential..."
kubectl --kubeconfig="${TENANT_KUBECONFIG}" create secret generic faros-projects-llm \
  --namespace=default \
  "--from-file=provider=${LLM_PROVIDER_FILE}" \
  "--from-file=baseURL=${LLM_BASE_URL_FILE}" \
  "--from-file=model=${LLM_MODEL_FILE}" \
  "--from-file=apiKey=${LLM_API_KEY_FILE}" \
  --dry-run=client -o yaml |
  kubectl --kubeconfig="${TENANT_KUBECONFIG}" apply -f - >/dev/null

echo "Applying Databricks credential and Connection..."
apply_secret_from_file "${DATABRICKS_SECRET_NAME}" token "${DATABRICKS_TOKEN_FILE}"

jq -n \
  --arg name "${DATABRICKS_CONNECTION_NAME}" \
  --arg host "${FAROS_BOOTSTRAP_DATABRICKS_HOST}" \
  --arg secret "${DATABRICKS_SECRET_NAME}" \
  '{
    apiVersion: "databricks.faros.sh/v1alpha1",
    kind: "Connection",
    metadata: {name: $name},
    spec: {
      host: $host,
      authType: "pat",
      secretRef: {name: $secret, namespace: "default", key: "token"}
    }
  }' >"${DATABRICKS_CONNECTION_MANIFEST}"
chmod 600 "${DATABRICKS_CONNECTION_MANIFEST}"
apply_connection_manifest databricks "${DATABRICKS_CONNECTION_MANIFEST}"

echo "Tenant setup applied with Code and Databricks APIBindings Bound. Controllers will validate the credentials asynchronously."
