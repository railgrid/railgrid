#!/usr/bin/env bash
# Focused fixture test for the local preview Gateway bootstrap. It exercises
# certificate reuse, the Helm/Gateway manifests, and cleanup without a live
# cluster.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
state_dir="$(mktemp -d)"
fake_bin="$(mktemp -d)"
fake_state="$(mktemp -d)"
trap 'rm -rf "$state_dir" "$fake_bin" "$fake_state"' EXIT

cat >"$fake_bin/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'helm %s\n' "$*" >>"${PREVIEW_GATEWAY_TEST_LOG:?}"
case "${1:-}" in
  pull)
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "--untardir" ]]; then
        mkdir -p "$2/gateway-crds-helm"
        touch "$2/gateway-crds-helm/Chart.yaml"
        exit 0
      fi
      shift
    done
    exit 1
    ;;
  template)
    output_dir=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "--output-dir" ]]; then
        output_dir="$2"
        break
      fi
      shift
    done
    test -n "$output_dir"
    mkdir -p "$output_dir/gateway-crds-helm/templates/generated"
    cat >"$output_dir/gateway-crds-helm/templates/generated/envoyproxies.yaml" <<'YAML'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: envoyproxies.gateway.envoyproxy.io
YAML
    ;;
esac
EOF

cat >"$fake_bin/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
log="${PREVIEW_GATEWAY_TEST_LOG:?}"
class_state="${PREVIEW_GATEWAY_TEST_CLASS:?}"
resource_state="${PREVIEW_GATEWAY_TEST_RESOURCES:?}"
printf 'kubectl %s\n' "$*" >>"$log"

if [[ " $* " == *" label --local -f - "* ]]; then
  awk '
    /^metadata:$/ {
      print
      print "  labels:"
      print "    faros.sh/managed-by: tilt"
      print "    faros.sh/component: preview-gateway"
      next
    }
    { print }
  '
  exit 0
fi

if [[ " $* " == *" apply "* && " $* " == *" -f - "* ]]; then
  manifest="$(cat)"
  printf '%s\n---\n' "$manifest" >>"${PREVIEW_GATEWAY_TEST_MANIFESTS:?}"
  if grep -q '^kind: GatewayClass$' <<<"$manifest"; then
    touch "$class_state"
  fi
  if grep -q '^kind: Gateway$' <<<"$manifest"; then
    touch "$resource_state/gateway"
  fi
  if grep -q '^kind: EnvoyProxy$' <<<"$manifest"; then
    touch "$resource_state/envoyproxy"
  fi
  if grep -q '^kind: Secret$' <<<"$manifest"; then
    touch "$resource_state/secret"
  fi
  exit 0
fi

case " $* " in
  *" get gatewayclass "*)
    if [[ -f "$class_state" ]]; then
      printf 'gateway.envoyproxy.io/gatewayclass-controller\n'
      exit 0
    fi
    exit 1
    ;;
  *" get gateway app-studio-preview "*)
    test -f "$resource_state/gateway" || exit 1
    if [[ " $* " == *" jsonpath="* ]]; then
      printf '%s' "${PREVIEW_GATEWAY_TEST_OWNERSHIP:-tilt|preview-gateway}"
    fi
    exit 0
    ;;
  *" get envoyproxy app-studio-preview-proxy "*)
    test -f "$resource_state/envoyproxy" || exit 1
    if [[ " $* " == *" jsonpath="* ]]; then
      printf '%s' "${PREVIEW_GATEWAY_TEST_OWNERSHIP:-tilt|preview-gateway}"
    fi
    exit 0
    ;;
  *" get secret app-studio-preview-tls "*)
    test -f "$resource_state/secret" || exit 1
    if [[ " $* " == *" jsonpath="* ]]; then
      printf '%s' "${PREVIEW_GATEWAY_TEST_OWNERSHIP:-tilt|preview-gateway}"
    fi
    exit 0
    ;;
  *" create secret tls "*)
    cat <<'YAML'
apiVersion: v1
kind: Secret
metadata:
  name: app-studio-preview-tls
type: kubernetes.io/tls
data:
  tls.crt: ZHVtbXk=
  tls.key: ZHVtbXk=
YAML
    ;;
  *" wait "*)
    ;;
  *" delete "*)
    case " $* " in
      *" delete gateway app-studio-preview "*) rm -f "$resource_state/gateway" ;;
      *" delete envoyproxy app-studio-preview-proxy "*) rm -f "$resource_state/envoyproxy" ;;
      *" delete secret app-studio-preview-tls "*) rm -f "$resource_state/secret" ;;
    esac
    ;;
esac
EOF
chmod +x "$fake_bin/helm" "$fake_bin/kubectl"

export PATH="$fake_bin:$PATH"
export PREVIEW_GATEWAY_TEST_LOG="$fake_state/log"
export PREVIEW_GATEWAY_TEST_CLASS="$fake_state/gatewayclass"
mkdir -p "$fake_state/resources"
export PREVIEW_GATEWAY_TEST_RESOURCES="$fake_state/resources"
export PREVIEW_GATEWAY_TEST_MANIFESTS="$fake_state/manifests"
export KUBECTL_BIN=kubectl
export HELM_BIN=helm
export PREVIEW_GATEWAY_STATE_DIR="$state_dir"
export PREVIEW_GATEWAY_TIMEOUT=1s
export PREVIEW_GATEWAY_CONTEXT=kind-faros-kro
export PREVIEW_GATEWAY_HOSTNAME='*.apps.127.0.0.1.sslip.io'
export PREVIEW_GATEWAY_PORT=10443
export PREVIEW_GATEWAY_SERVICE_PORT=443

"$script_dir/configure-tilt-preview-gateway.sh" apply
test -s "$state_dir/tls.crt"
test -s "$state_dir/tls.key"
openssl x509 -in "$state_dir/tls.crt" -noout -checkhost 'foo.apps.127.0.0.1.sslip.io' >/dev/null
openssl x509 -in "$state_dir/tls.crt" -noout -ext basicConstraints | grep -q 'CA:FALSE'
grep -F 'helm pull oci://docker.io/envoyproxy/gateway-crds-helm' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'helm template envoy-gateway-crds' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'crds.gatewayAPI.enabled=false' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'crds.envoyGateway.enabled=true' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'helm upgrade --install envoy-gateway' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F -- '--kube-context kind-faros-kro' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'kubectl --context kind-faros-kro' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'crds.enabled=false' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'apply --server-side --recursive -f' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'hostname: "*.apps.127.0.0.1.sslip.io"' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'port: 10443' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'kind: EnvoyProxy' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'type: ClusterIP' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'name: app-studio-preview-proxy' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'name: https-public-443' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'port: 443' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null
grep -F 'targetPort: 10443' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null

certificate_digest="$(openssl x509 -in "$state_dir/tls.crt" -outform DER | openssl dgst -sha256)"
"$script_dir/configure-tilt-preview-gateway.sh" apply
test "$certificate_digest" = "$(openssl x509 -in "$state_dir/tls.crt" -outform DER | openssl dgst -sha256)"

: >"$PREVIEW_GATEWAY_TEST_MANIFESTS"
export PREVIEW_GATEWAY_SERVICE_PORT=10443
"$script_dir/configure-tilt-preview-gateway.sh" apply
if grep -F 'name: https-public-' "$PREVIEW_GATEWAY_TEST_MANIFESTS" >/dev/null; then
  echo 'same-port configuration unexpectedly rendered a Service alias' >&2
  exit 1
fi
export PREVIEW_GATEWAY_SERVICE_PORT=443

"$script_dir/configure-tilt-preview-gateway.sh" cleanup
grep -F 'delete gateway app-studio-preview' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'delete envoyproxy app-studio-preview-proxy' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
grep -F 'delete secret app-studio-preview-tls' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null
if grep -F 'uninstall envoy-gateway' "$PREVIEW_GATEWAY_TEST_LOG" >/dev/null; then
  echo 'cleanup unexpectedly uninstalled the shared Envoy controller' >&2
  exit 1
fi

export PREVIEW_GATEWAY_SERVICE_PORT=0
if "$script_dir/configure-tilt-preview-gateway.sh" apply >"$fake_state/invalid-port.out" 2>&1; then
  echo 'apply unexpectedly accepted an invalid HTTPS Service port' >&2
  exit 1
fi
grep -F 'invalid HTTPS Service port: 0' "$fake_state/invalid-port.out" >/dev/null
export PREVIEW_GATEWAY_SERVICE_PORT=443

touch "$PREVIEW_GATEWAY_TEST_RESOURCES/secret"
export PREVIEW_GATEWAY_TEST_OWNERSHIP='another-owner|shared-gateway'
if "$script_dir/configure-tilt-preview-gateway.sh" apply >"$fake_state/unowned.out" 2>&1; then
  echo 'apply unexpectedly adopted an unowned preview resource' >&2
  exit 1
fi
grep -F 'refusing to adopt secret/app-studio-preview-tls' "$fake_state/unowned.out" >/dev/null
