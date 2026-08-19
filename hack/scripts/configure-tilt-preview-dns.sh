#!/usr/bin/env bash
# Configure the local kind cluster's CoreDNS to resolve template app hosts to
# the in-cluster preview gateway. Host-side resolution is intentionally not
# changed: sslip.io continues to resolve *.apps.127.0.0.1.sslip.io to loopback
# for the Tilt port-forward.

set -euo pipefail

cleanup=false
if [[ "${1:-}" == "--cleanup" ]]; then
  cleanup=true
  shift
fi
if [[ $# -ne 3 && $# -ne 5 ]]; then
  echo "usage: $0 [--cleanup] <kubectl-context> <base-domain> <gateway-ip> [<hub-host> <hub-ip>]" >&2
  exit 2
fi

context="$1"
base_domain="$2"
gateway_ip="$3"
hub_host="${4:-}"
hub_ip="${5:-}"
marker_start="# faros-preview-dns"
marker_end="# faros-preview-dns-end"

if [[ ! "$base_domain" =~ ^[a-z0-9.-]+$ ]]; then
  echo "invalid preview base domain: $base_domain" >&2
  exit 2
fi
if [[ ! "$gateway_ip" =~ ^[0-9.]+$ ]]; then
  echo "invalid preview gateway IP: $gateway_ip" >&2
  exit 2
fi
if [[ -n "$hub_host" && ! "$hub_host" =~ ^[a-z0-9.-]+$ ]]; then
  echo "invalid preview hub host: $hub_host" >&2
  exit 2
fi
if [[ -n "$hub_host" && ! "$hub_ip" =~ ^[0-9.]+$ ]]; then
  echo "invalid preview hub IP: $hub_ip" >&2
  exit 2
fi

# A Tilt teardown must never fail because the kind cluster has already gone
# away. Apply mode waits for CoreDNS during startup; cleanup mode probes once.
if [[ "$cleanup" == true ]]; then
  corefile="$(kubectl --context "$context" -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' 2>/dev/null || true)"
else
  for _ in $(seq 1 60); do
    corefile="$(kubectl --context "$context" -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}' 2>/dev/null || true)"
    if [[ -n "$corefile" ]]; then
      break
    fi
    sleep 1
  done
fi
if [[ "$cleanup" == true && -z "${corefile:-}" ]]; then
  exit 0
fi
if [[ -z "${corefile:-}" ]]; then
  echo "CoreDNS ConfigMap did not become available" >&2
  exit 1
fi

# Remove a prior managed block so a changed domain/IP is applied exactly once.
without_managed="$(printf '%s\n' "$corefile" | awk -v start="$marker_start" -v end="$marker_end" '
  $0 == start { skipping = 1; next }
  skipping && $0 == end { skipping = 0; next }
  !skipping { print }
')"

if [[ "$cleanup" == true ]]; then
  # Removing an already absent block is a successful no-op. If the cluster or
  # ConfigMap disappears between the read and patch, teardown remains safe.
  if [[ "$without_managed" == "$corefile" ]]; then
    exit 0
  fi
  patch_json="$(jq -cn --arg corefile "$without_managed" '{data:{Corefile:$corefile}}')"
  kubectl --context "$context" -n kube-system patch configmap coredns --type merge -p "$patch_json" >/dev/null 2>&1 || exit 0
  kubectl --context "$context" -n kube-system delete pods -l k8s-app=kube-dns --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

# CoreDNS matches fully-qualified query names (including the trailing dot).
escaped_domain="${base_domain//./\\.}"
block_file="$(mktemp)"
trap 'rm -f "$block_file"' EXIT
cat >"$block_file" <<EOF
${marker_start}
    template IN A {
        match ^[^.]+\\.${escaped_domain}\\.$
        answer "{{ .Name }} 60 IN A ${gateway_ip}"
        fallthrough
    }
EOF
if [[ -n "$hub_host" ]]; then
  escaped_hub_host="${hub_host//./\\.}"
  cat >>"$block_file" <<EOF
    template IN A {
        match ^${escaped_hub_host}\\.$
        answer "{{ .Name }} 60 IN A ${hub_ip}"
        fallthrough
    }
EOF
fi
printf '%s\n' "$marker_end" >>"$block_file"

patched="$(awk -v block_file="$block_file" '
  BEGIN {
    while ((getline line < block_file) > 0) block = block line "\n"
    close(block_file)
  }
  {
    print
    if (!inserted && $0 ~ /^[[:space:]]*errors[[:space:]]*$/) {
      printf "%s", block
      inserted = 1
    }
  }
  END {
    if (!inserted) exit 42
  }
' <<<"$without_managed")" || {
  echo "CoreDNS Corefile has no errors plugin insertion point" >&2
  exit 1
}
patch_json="$(jq -cn --arg corefile "$patched" '{data:{Corefile:$corefile}}')"
if [[ "$patched" != "$corefile" ]]; then
  kubectl --context "$context" -n kube-system patch configmap coredns --type merge -p "$patch_json" >/dev/null
  # CoreDNS reloads its Corefile, but deleting its pods makes the local dev
  # route deterministic across kind versions and stale reload state.
  kubectl --context "$context" -n kube-system delete pods -l k8s-app=kube-dns --ignore-not-found >/dev/null
fi
