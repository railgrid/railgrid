#!/usr/bin/env bash
# Fixture test for the local-only CoreDNS helper. It uses a fake kubectl so the
# apply/cleanup/idempotence paths can run without a cluster.

set -euo pipefail

command -v jq >/dev/null 2>&1 || exit 0
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
state_dir="$(mktemp -d)"
fake_bin="$(mktemp -d)"
trap 'rm -rf "$state_dir" "$fake_bin"' EXIT

cat >"$fake_bin/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
state_dir="${RAILGRID_DNS_TEST_STATE:?}"
case " $* " in
  *" get configmap coredns "*)
    [[ -f "$state_dir/corefile" ]] || exit 1
    cat "$state_dir/corefile"
    ;;
  *" patch configmap coredns "*)
    patch=""
    while (($#)); do
      if [[ "$1" == "-p" ]]; then
        patch="$2"
        shift 2
      else
        shift
      fi
    done
    jq -r '.data.Corefile' <<<"$patch" >"$state_dir/corefile"
    printf 'patch\n' >>"$state_dir/events"
    ;;
  *" delete pods "*)
    printf 'delete\n' >>"$state_dir/events"
    ;;
  *)
    exit 1
    ;;
esac
EOF
chmod +x "$fake_bin/kubectl"
export RAILGRID_DNS_TEST_STATE="$state_dir"
export PATH="$fake_bin:$PATH"

printf '%s\n' '.:53 {' 'errors' 'forward . /etc/resolv.conf' '}' >"$state_dir/corefile"
"$script_dir/configure-tilt-preview-dns.sh" fake-context apps.127.0.0.1.sslip.io 10.96.2.2 console.127.0.0.1.sslip.io 172.18.0.1
grep -F '# railgrid-preview-dns' "$state_dir/corefile" >/dev/null
grep -F '10.96.2.2' "$state_dir/corefile" >/dev/null
grep -F 'console\.127\.0\.0\.1\.sslip\.io' "$state_dir/corefile" >/dev/null
grep -F '|host\.docker\.internal)' "$state_dir/corefile" >/dev/null
grep -F '172.18.0.1' "$state_dir/corefile" >/dev/null

# Both IPv4-only hub aliases must answer AAAA with NOERROR and no records.
# NXDOMAIN describes a nonexistent name and can invalidate the concurrent A
# answer in musl clients. Unrelated names must still reach normal resolution.
awk '
  $1 == "template" && $2 == "IN" && $3 == "AAAA" { blocks++; inside = 1; next }
  inside && $1 == "match" {
    if ($2 == "^(console\\.127\\.0\\.0\\.1\\.sslip\\.io|host\\.docker\\.internal)\\.$") matches++
  }
  inside && $1 == "rcode" && $2 == "NOERROR" { success++ }
  inside && $1 == "answer" { answers++ }
  inside && $1 == "fallthrough" { fallback++ }
  inside && $1 == "}" { inside = 0 }
  END { exit !(blocks == 1 && matches == 1 && success == 1 && answers == 0 && fallback == 1) }
' "$state_dir/corefile" || {
  echo 'hub aliases must have an empty successful AAAA response with unrelated-name fallthrough' >&2
  exit 1
}

# Tilt orders kcp-dns after preview-dns because both replace the same CoreDNS
# field. The second helper must retain the first helper's independently managed
# block, and its cleanup must leave the preview route intact.
"$script_dir/configure-tilt-kcp-dns.sh" fake-context 10.96.2.2
grep -F '# railgrid-preview-dns' "$state_dir/corefile" >/dev/null
grep -F '# railgrid-kcp-dns' "$state_dir/corefile" >/dev/null
"$script_dir/configure-tilt-kcp-dns.sh" --cleanup fake-context 10.96.2.2
grep -F '# railgrid-preview-dns' "$state_dir/corefile" >/dev/null
if grep -F '# railgrid-kcp-dns' "$state_dir/corefile" >/dev/null; then
  echo 'managed kcp CoreDNS block survived cleanup' >&2
  exit 1
fi

first_event_count="$(wc -l <"$state_dir/events")"
"$script_dir/configure-tilt-preview-dns.sh" fake-context apps.127.0.0.1.sslip.io 10.96.2.2 console.127.0.0.1.sslip.io 172.18.0.1
second_event_count="$(wc -l <"$state_dir/events")"
[[ "$first_event_count" == "$second_event_count" ]]

"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
if grep -F '# railgrid-preview-dns' "$state_dir/corefile" >/dev/null; then
  echo 'managed CoreDNS block survived cleanup' >&2
  exit 1
fi
cleanup_event_count="$(wc -l <"$state_dir/events")"
"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
[[ "$cleanup_event_count" == "$(wc -l <"$state_dir/events")" ]]

rm -f "$state_dir/corefile"
"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
