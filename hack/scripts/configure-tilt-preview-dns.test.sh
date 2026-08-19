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
state_dir="${FAROS_DNS_TEST_STATE:?}"
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
export FAROS_DNS_TEST_STATE="$state_dir"
export PATH="$fake_bin:$PATH"

printf '%s\n' '.:53 {' 'errors' 'forward . /etc/resolv.conf' '}' >"$state_dir/corefile"
"$script_dir/configure-tilt-preview-dns.sh" fake-context apps.127.0.0.1.sslip.io 10.96.2.2 console.127.0.0.1.sslip.io 172.18.0.1
grep -F '# faros-preview-dns' "$state_dir/corefile" >/dev/null
grep -F '10.96.2.2' "$state_dir/corefile" >/dev/null
grep -F 'console\.127\.0\.0\.1\.sslip\.io' "$state_dir/corefile" >/dev/null
grep -F '172.18.0.1' "$state_dir/corefile" >/dev/null

# Tilt orders kcp-dns after preview-dns because both replace the same CoreDNS
# field. The second helper must retain the first helper's independently managed
# block, and its cleanup must leave the preview route intact.
"$script_dir/configure-tilt-kcp-dns.sh" fake-context 10.96.2.2
grep -F '# faros-preview-dns' "$state_dir/corefile" >/dev/null
grep -F '# faros-kcp-dns' "$state_dir/corefile" >/dev/null
"$script_dir/configure-tilt-kcp-dns.sh" --cleanup fake-context 10.96.2.2
grep -F '# faros-preview-dns' "$state_dir/corefile" >/dev/null
if grep -F '# faros-kcp-dns' "$state_dir/corefile" >/dev/null; then
  echo 'managed kcp CoreDNS block survived cleanup' >&2
  exit 1
fi

first_event_count="$(wc -l <"$state_dir/events")"
"$script_dir/configure-tilt-preview-dns.sh" fake-context apps.127.0.0.1.sslip.io 10.96.2.2 console.127.0.0.1.sslip.io 172.18.0.1
second_event_count="$(wc -l <"$state_dir/events")"
[[ "$first_event_count" == "$second_event_count" ]]

"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
if grep -F '# faros-preview-dns' "$state_dir/corefile" >/dev/null; then
  echo 'managed CoreDNS block survived cleanup' >&2
  exit 1
fi
cleanup_event_count="$(wc -l <"$state_dir/events")"
"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
[[ "$cleanup_event_count" == "$(wc -l <"$state_dir/events")" ]]

rm -f "$state_dir/corefile"
"$script_dir/configure-tilt-preview-dns.sh" --cleanup fake-context apps.127.0.0.1.sslip.io 10.96.2.2
