#!/usr/bin/env bash
# Configure the local kind cluster's CoreDNS to resolve kcp.localhost (and the
# per-shard *.kcp.localhost names) to the in-cluster Envoy gateway, so PROVIDER
# PODS can use the operator-issued kubeconfigs unchanged — the same trick the
# hub pod gets via hostAliases, done once cluster-wide instead of per chart.
# Host-side resolution is untouched (kcp.localhost resolves to loopback for the
# kcp-frontproxy-forward port-forward).
#
# Sibling of configure-tilt-preview-dns.sh with its own managed block.

set -euo pipefail

cleanup=false
if [[ "${1:-}" == "--cleanup" ]]; then
  cleanup=true
  shift
fi
if [[ $# -ne 2 ]]; then
  echo "usage: $0 [--cleanup] <kubectl-context> <gateway-ip>" >&2
  exit 2
fi

context="$1"
gateway_ip="$2"
marker_start="# faros-kcp-dns"
marker_end="# faros-kcp-dns-end"

if [[ ! "$gateway_ip" =~ ^[0-9.]+$ ]]; then
  echo "invalid gateway IP: $gateway_ip" >&2
  exit 2
fi

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

without_managed="$(printf '%s\n' "$corefile" | awk -v start="$marker_start" -v end="$marker_end" '
  $0 == start { skipping = 1; next }
  skipping && $0 == end { skipping = 0; next }
  !skipping { print }
')"

if [[ "$cleanup" == true ]]; then
  if [[ "$without_managed" == "$corefile" ]]; then
    exit 0
  fi
  patch_json="$(jq -cn --arg corefile "$without_managed" '{data:{Corefile:$corefile}}')"
  kubectl --context "$context" -n kube-system patch configmap coredns --type merge -p "$patch_json" >/dev/null 2>&1 || exit 0
  kubectl --context "$context" -n kube-system delete pods -l k8s-app=kube-dns --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

# Apex + one shard label: kcp.localhost, root.kcp.localhost, theseus.kcp.localhost.
block_file="$(mktemp)"
trap 'rm -f "$block_file"' EXIT
cat >"$block_file" <<EOF
${marker_start}
    template IN A {
        match ^([^.]+\\.)?kcp\\.localhost\\.$
        answer "{{ .Name }} 60 IN A ${gateway_ip}"
        fallthrough
    }
${marker_end}
EOF

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
  kubectl --context "$context" -n kube-system delete pods -l k8s-app=kube-dns --ignore-not-found >/dev/null
fi
