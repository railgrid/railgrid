#!/usr/bin/env bash
# Install the local Envoy Gateway and configure the HTTPS parent used by
# application-template previews. This deliberately keeps certificate material
# local to the ignored .kcp state directory so the base Tilt loop needs no
# cert-manager installation.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: configure-tilt-preview-gateway.sh [apply|cleanup]

Environment:
  KUBECONFIG                         target cluster kubeconfig
  PREVIEW_GATEWAY_CONTEXT            target kubeconfig context (default: kind-faros-kro)
  ENVOY_GATEWAY_RELEASE              Helm release name (default: envoy-gateway)
  ENVOY_GATEWAY_CHART                Helm OCI chart (default: oci://docker.io/envoyproxy/gateway-helm)
  ENVOY_GATEWAY_CRDS_CHART           Envoy-only CRD OCI chart (default: oci://docker.io/envoyproxy/gateway-crds-helm)
  ENVOY_GATEWAY_VERSION              pinned chart version (default: v1.8.3)
  PREVIEW_GATEWAY_NAMESPACE          namespace (default: envoy-gateway-system)
  PREVIEW_GATEWAY_NAME               Gateway name (default: app-studio-preview)
  PREVIEW_GATEWAY_CLASS              GatewayClass name (default: eg)
  PREVIEW_GATEWAY_CONTROLLER          controller name (default: gateway.envoyproxy.io/gatewayclass-controller)
  PREVIEW_GATEWAY_PROXY_CONFIG        EnvoyProxy name (default: app-studio-preview-proxy)
  PREVIEW_GATEWAY_HOSTNAME            wildcard hostname (default: *.apps.127.0.0.1.sslip.io)
  PREVIEW_GATEWAY_LISTENER_NAME       listener name (default: https)
  PREVIEW_GATEWAY_TLS_SECRET          TLS Secret name (default: app-studio-preview-tls)
  PREVIEW_GATEWAY_PORT                HTTPS listener port (default: 10443)
  PREVIEW_GATEWAY_STATE_DIR           certificate state directory (default: .kcp/tilt-preview-gateway-tls)
  PREVIEW_GATEWAY_TIMEOUT             kubectl/Helm wait timeout (default: 5m)
  PREVIEW_GATEWAY_UNINSTALL           cleanup only: also helm-uninstall (default: false)
  KUBECTL_BIN                         kubectl executable (test seam; default: kubectl)
  HELM_BIN                            helm executable (test seam; default: helm)
EOF
  exit 2
}

action="${1:-apply}"
if [[ $# -gt 1 || ("${action}" != "apply" && "${action}" != "cleanup") ]]; then
  usage
fi

kubectl_bin="${KUBECTL_BIN:-kubectl}"
helm_bin="${HELM_BIN:-helm}"
kube_context="${PREVIEW_GATEWAY_CONTEXT:-kind-faros-kro}"
kubectl=("${kubectl_bin}" --context "$kube_context")
helm=("${helm_bin}")

helm_release="${ENVOY_GATEWAY_RELEASE:-envoy-gateway}"
helm_chart="${ENVOY_GATEWAY_CHART:-oci://docker.io/envoyproxy/gateway-helm}"
helm_crds_chart="${ENVOY_GATEWAY_CRDS_CHART:-oci://docker.io/envoyproxy/gateway-crds-helm}"
helm_version="${ENVOY_GATEWAY_VERSION:-v1.8.3}"
namespace="${PREVIEW_GATEWAY_NAMESPACE:-envoy-gateway-system}"
gateway_name="${PREVIEW_GATEWAY_NAME:-app-studio-preview}"
gateway_class="${PREVIEW_GATEWAY_CLASS:-eg}"
controller_name="${PREVIEW_GATEWAY_CONTROLLER:-gateway.envoyproxy.io/gatewayclass-controller}"
proxy_config="${PREVIEW_GATEWAY_PROXY_CONFIG:-app-studio-preview-proxy}"
hostname="${PREVIEW_GATEWAY_HOSTNAME:-*.apps.127.0.0.1.sslip.io}"
listener_name="${PREVIEW_GATEWAY_LISTENER_NAME:-https}"
tls_secret="${PREVIEW_GATEWAY_TLS_SECRET:-app-studio-preview-tls}"
gateway_port="${PREVIEW_GATEWAY_PORT:-10443}"
state_dir="${PREVIEW_GATEWAY_STATE_DIR:-.kcp/tilt-preview-gateway-tls}"
timeout="${PREVIEW_GATEWAY_TIMEOUT:-5m}"

die() {
  echo "preview gateway: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

valid_kubernetes_name() {
  [[ "$1" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]]
}

valid_hostname() {
  [[ "$1" =~ ^\*\.[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]]
}

valid_port() {
  [[ "$1" =~ ^[0-9]+$ ]] && ((1 <= 10#$1 && 10#$1 <= 65535))
}

resource_exists() {
  local resource_namespace="$1" kind="$2" name="$3"
  if [[ -n "$resource_namespace" ]]; then
    "${kubectl[@]}" -n "$resource_namespace" get "$kind" "$name" >/dev/null 2>&1
  else
    "${kubectl[@]}" get "$kind" "$name" >/dev/null 2>&1
  fi
}

resource_ownership() {
  local resource_namespace="$1" kind="$2" name="$3"
  local output=(
    -o 'jsonpath={.metadata.labels.faros\.sh/managed-by}{"|"}{.metadata.labels.faros\.sh/component}'
  )
  if [[ -n "$resource_namespace" ]]; then
    "${kubectl[@]}" -n "$resource_namespace" get "$kind" "$name" "${output[@]}"
  else
    "${kubectl[@]}" get "$kind" "$name" "${output[@]}"
  fi
}

assert_owned_or_absent() {
  local resource_namespace="$1" kind="$2" name="$3"
  resource_exists "$resource_namespace" "$kind" "$name" || return 0
  local ownership
  ownership="$(resource_ownership "$resource_namespace" "$kind" "$name" 2>/dev/null || true)"
  [[ "$ownership" == "tilt|preview-gateway" ]] || die \
    "refusing to adopt ${kind}/${name}: expected Tilt preview ownership labels, got ${ownership:-none}"
}

delete_owned_resource() {
  local resource_namespace="$1" kind="$2" name="$3"
  resource_exists "$resource_namespace" "$kind" "$name" || return 0
  local ownership
  ownership="$(resource_ownership "$resource_namespace" "$kind" "$name" 2>/dev/null || true)"
  [[ "$ownership" == "tilt|preview-gateway" ]] || die \
    "refusing to delete ${kind}/${name}: expected Tilt preview ownership labels, got ${ownership:-none}"
  if [[ -n "$resource_namespace" ]]; then
    "${kubectl[@]}" -n "$resource_namespace" delete "$kind" "$name" \
      --ignore-not-found >/dev/null
  else
    "${kubectl[@]}" delete "$kind" "$name" --ignore-not-found >/dev/null
  fi
}

valid_kubernetes_name "$helm_release" || die "invalid Helm release name: $helm_release"
valid_kubernetes_name "$namespace" || die "invalid namespace: $namespace"
valid_kubernetes_name "$gateway_name" || die "invalid Gateway name: $gateway_name"
valid_kubernetes_name "$gateway_class" || die "invalid GatewayClass name: $gateway_class"
valid_kubernetes_name "$proxy_config" || die "invalid EnvoyProxy name: $proxy_config"
valid_kubernetes_name "$listener_name" || die "invalid listener name: $listener_name"
valid_kubernetes_name "$tls_secret" || die "invalid TLS Secret name: $tls_secret"
valid_hostname "$hostname" || die "expected wildcard DNS hostname, got: $hostname"
valid_port "$gateway_port" || die "invalid HTTPS listener port: $gateway_port"
[[ -n "$kube_context" ]] || die "preview Gateway context must not be empty"

cert_file="${state_dir}/tls.crt"
key_file="${state_dir}/tls.key"
work_dir=""

cleanup_work_dir() {
  if [[ -n "$work_dir" ]]; then
    rm -rf "$work_dir"
  fi
}
trap cleanup_work_dir EXIT

valid_certificate() {
  [[ -s "$cert_file" && -s "$key_file" ]] || return 1
  openssl x509 -in "$cert_file" -noout -checkend 86400 >/dev/null 2>&1 || return 1
  openssl x509 -in "$cert_file" -noout -checkhost "$hostname" >/dev/null 2>&1 || return 1
  openssl x509 -in "$cert_file" -noout -ext basicConstraints 2>/dev/null |
    grep -q 'CA:FALSE' || return 1
  openssl pkey -in "$key_file" -noout >/dev/null 2>&1 || return 1

  local cert_public key_public
  cert_public="$({ openssl x509 -in "$cert_file" -pubkey -noout |
    openssl pkey -pubin -outform DER; } 2>/dev/null | openssl dgst -sha256)" || return 1
  key_public="$({ openssl pkey -in "$key_file" -pubout -outform DER; } 2>/dev/null |
    openssl dgst -sha256)" || return 1
  [[ "$cert_public" == "$key_public" ]]
}

ensure_certificate() {
  if valid_certificate; then
    return 0
  fi

  mkdir -p "$state_dir"
  work_dir="$(mktemp -d "${state_dir}.tmp.XXXXXX")"
  umask 077
  openssl req -x509 -nodes -newkey rsa:2048 \
    -keyout "${work_dir}/tls.key" \
    -out "${work_dir}/tls.crt" \
    -days 365 \
    -subj "/CN=${hostname}" \
    -addext "subjectAltName=DNS:${hostname}" \
    -addext "basicConstraints=critical,CA:FALSE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment" \
    -addext "extendedKeyUsage=serverAuth" \
    >/dev/null 2>&1
  chmod 0600 "${work_dir}/tls.key"
  chmod 0644 "${work_dir}/tls.crt"
  mv -f "${work_dir}/tls.crt" "$cert_file"
  mv -f "${work_dir}/tls.key" "$key_file"
  rm -rf "$work_dir"
  work_dir=""
}

delete_owned_resources() {
  # Teardown must remain successful when Tilt already deleted the kind cluster.
  delete_owned_resource "$namespace" gateway "$gateway_name"
  delete_owned_resource "$namespace" envoyproxy "$proxy_config"
  delete_owned_resource "$namespace" secret "$tls_secret"
  if [[ "${PREVIEW_GATEWAY_UNINSTALL:-false}" == "true" ]]; then
    # Keep this opt-in: the namespace/controller may be shared by another
    # local Gateway. `dev-kro-down` remains the authoritative full cleanup.
    local release_owner
    release_owner="$("${kubectl[@]}" -n "$namespace" get deployment \
      -l "app.kubernetes.io/instance=${helm_release}" \
      -o 'jsonpath={.items[0].metadata.labels.app\.kubernetes\.io/instance}' \
      2>/dev/null || true)"
    [[ "$release_owner" == "$helm_release" ]] || die \
      "refusing to uninstall Helm release ${helm_release}: no matching managed deployment"
    "${helm[@]}" --kube-context "$kube_context" -n "$namespace" \
      uninstall "$helm_release" >/dev/null
  fi
}

if [[ "$action" == "cleanup" ]]; then
  delete_owned_resources
  exit 0
fi

require_command "$kubectl_bin"
require_command "$helm_bin"
require_command openssl

# Refuse conflicting names before installing or upgrading any cluster-scoped
# controller resources. EnvoyProxy may not be discoverable on the first run;
# that is correctly treated as absent until its CRD is installed below.
assert_owned_or_absent "$namespace" secret "$tls_secret"
assert_owned_or_absent "$namespace" envoyproxy "$proxy_config"
assert_owned_or_absent "$namespace" gateway "$gateway_name"
ensure_certificate

echo ">>> installing Envoy Gateway ${helm_version} into ${namespace}"
# The faros-kro bootstrap owns the standard Gateway API CRDs. Apply only the
# Envoy-specific CRDs from Envoy's companion chart, then disable the main
# chart's CRD dependency so Helm cannot replace or compete with that ownership.
work_dir="$(mktemp -d "${state_dir}.chart.XXXXXX")"
"${helm[@]}" pull "$helm_crds_chart" \
  --version "$helm_version" \
  --untar \
  --untardir "$work_dir"
crds_chart_dir="${work_dir}/gateway-crds-helm"
rendered_crds_dir="${work_dir}/rendered"
[[ -f "${crds_chart_dir}/Chart.yaml" ]] || die \
  "Envoy CRD chart did not unpack at ${crds_chart_dir}"
"${helm[@]}" template "${helm_release}-crds" "$crds_chart_dir" \
  --version "$helm_version" \
  --set crds.gatewayAPI.enabled=false \
  --set crds.envoyGateway.enabled=true \
  --output-dir "$rendered_crds_dir" >/dev/null
"${kubectl[@]}" apply --server-side --recursive -f "$rendered_crds_dir" >/dev/null
"${helm[@]}" upgrade --install "$helm_release" "$helm_chart" \
  --kube-context "$kube_context" \
  --version "$helm_version" \
  --namespace "$namespace" \
  --create-namespace \
  --set crds.enabled=false \
  --wait \
  --timeout "$timeout"
"${kubectl[@]}" -n "$namespace" wait deployment/envoy-gateway \
  --for=condition=Available --timeout="$timeout" >/dev/null

# The Helm chart installs the controller; the GatewayClass is an explicit
# resource here so this bootstrap remains self-contained and does not rely on
# the example quickstart manifest or another Tiltfile.
existing_controller="$("${kubectl[@]}" get gatewayclass "$gateway_class" \
  -o jsonpath='{.spec.controllerName}' 2>/dev/null || true)"
if [[ -n "$existing_controller" ]]; then
  [[ "$existing_controller" == "$controller_name" ]] || die \
    "GatewayClass ${gateway_class} is owned by ${existing_controller}, expected ${controller_name}"
else
  cat <<EOF | "${kubectl[@]}" apply -f - >/dev/null
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: ${gateway_class}
  labels:
    faros.sh/managed-by: tilt
    faros.sh/component: preview-gateway
spec:
  controllerName: ${controller_name}
EOF
fi
"${kubectl[@]}" wait gatewayclass/"$gateway_class" \
  --for=condition=Accepted --timeout="$timeout" >/dev/null

assert_owned_or_absent "$namespace" secret "$tls_secret"
"${kubectl[@]}" -n "$namespace" create secret tls "$tls_secret" \
  --cert="$cert_file" \
  --key="$key_file" \
  --dry-run=client \
  -o yaml |
  "${kubectl[@]}" label --local -f - \
    faros.sh/managed-by=tilt \
    faros.sh/component=preview-gateway \
    --overwrite -o yaml |
  "${kubectl[@]}" apply -f - >/dev/null

# A plain kind cluster has no LoadBalancer implementation. Make Envoy's data
# plane Service a ClusterIP: the host only reaches it through Tilt's explicit
# port-forward, and Envoy Gateway can then publish a real Gateway address
# instead of leaving Programmed=False with AddressNotAssigned.
assert_owned_or_absent "$namespace" envoyproxy "$proxy_config"
cat <<EOF | "${kubectl[@]}" apply -f - >/dev/null
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: ${proxy_config}
  namespace: ${namespace}
  labels:
    faros.sh/managed-by: tilt
    faros.sh/component: preview-gateway
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        type: ClusterIP
EOF

assert_owned_or_absent "$namespace" gateway "$gateway_name"
cat <<EOF | "${kubectl[@]}" apply -f - >/dev/null
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${gateway_name}
  namespace: ${namespace}
  labels:
    faros.sh/managed-by: tilt
    faros.sh/component: preview-gateway
spec:
  gatewayClassName: ${gateway_class}
  infrastructure:
    parametersRef:
      group: gateway.envoyproxy.io
      kind: EnvoyProxy
      name: ${proxy_config}
  listeners:
    - name: ${listener_name}
      hostname: "${hostname}"
      port: ${gateway_port}
      protocol: HTTPS
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: ${tls_secret}
      allowedRoutes:
        namespaces:
          from: All
EOF

"${kubectl[@]}" -n "$namespace" wait gateway/"$gateway_name" \
  --for=condition=Programmed --timeout="$timeout" >/dev/null
echo ">>> preview Gateway ${gateway_name} is Programmed (HTTPS :${gateway_port}, ${hostname})"
