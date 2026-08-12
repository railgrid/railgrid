#!/usr/bin/env bash
# Render the stable local-only TLS Secret used by Tiltfile.cluster. Certificate
# material lives under ignored .kcp state so re-rendering the Helm chart cannot
# silently rotate the hub identity out from under App Studio runtimes.

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <namespace> <secret-name> <state-dir>" >&2
  exit 2
fi

namespace="$1"
secret_name="$2"
state_dir="$3"
ca_file="${state_dir}/ca.crt"
cert_file="${state_dir}/tls.crt"
key_file="${state_dir}/tls.key"

valid_material() {
  [[ -s "${ca_file}" && -s "${cert_file}" && -s "${key_file}" ]] || return 1
  openssl x509 -in "${ca_file}" -noout -checkend 86400 >/dev/null 2>&1 || return 1
  openssl x509 -in "${cert_file}" -noout -checkend 86400 >/dev/null 2>&1 || return 1
  openssl x509 -in "${cert_file}" -noout -checkhost "faros-hub.${namespace}.svc" >/dev/null 2>&1 || return 1
  openssl x509 -in "${cert_file}" -noout -checkhost localhost >/dev/null 2>&1 || return 1
  openssl x509 -in "${cert_file}" -noout -checkip 127.0.0.1 >/dev/null 2>&1 || return 1
  openssl verify -CAfile "${ca_file}" "${cert_file}" >/dev/null 2>&1 || return 1

  cert_public="$({ openssl x509 -in "${cert_file}" -pubkey -noout |
    openssl pkey -pubin -outform DER; } 2>/dev/null | openssl dgst -sha256)"
  key_public="$({ openssl pkey -in "${key_file}" -pubout -outform DER; } 2>/dev/null |
    openssl dgst -sha256)"
  [[ -n "${cert_public}" && "${cert_public}" == "${key_public}" ]]
}

if ! valid_material; then
  mkdir -p "$(dirname "${state_dir}")"
  work_dir="$(mktemp -d "${state_dir}.tmp.XXXXXX")"
  trap 'rm -rf "${work_dir}"' EXIT

  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${work_dir}/ca.key" >/dev/null 2>&1
  openssl req -x509 -new -sha256 -days 365 \
    -key "${work_dir}/ca.key" \
    -subj '/CN=faros-hub-ca' \
    -out "${work_dir}/ca.crt"

  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${work_dir}/tls.key" >/dev/null 2>&1
  openssl req -new -sha256 \
    -key "${work_dir}/tls.key" \
    -subj '/CN=faros-hub' \
    -out "${work_dir}/tls.csr"
  printf '%s\n' \
    '[v3_req]' \
    'basicConstraints=critical,CA:FALSE' \
    'keyUsage=critical,digitalSignature,keyEncipherment' \
    'extendedKeyUsage=serverAuth' \
    "subjectAltName=DNS:localhost,DNS:faros-hub,DNS:faros-hub-dex,DNS:faros-hub.${namespace}.svc,DNS:faros-hub-dex.${namespace}.svc,IP:127.0.0.1" \
    >"${work_dir}/tls.ext"
  openssl x509 -req -sha256 -days 365 \
    -in "${work_dir}/tls.csr" \
    -CA "${work_dir}/ca.crt" \
    -CAkey "${work_dir}/ca.key" \
    -CAcreateserial \
    -extfile "${work_dir}/tls.ext" \
    -extensions v3_req \
    -out "${work_dir}/tls.crt" >/dev/null 2>&1

  chmod 0600 "${work_dir}/tls.key"
  chmod 0644 "${work_dir}/ca.crt" "${work_dir}/tls.crt"
  mkdir -p "${state_dir}"
  mv "${work_dir}/ca.crt" "${work_dir}/tls.crt" "${work_dir}/tls.key" "${state_dir}/"
fi

kubectl create secret generic "${secret_name}" \
  --namespace "${namespace}" \
  --type kubernetes.io/tls \
  --from-file=ca.crt="${ca_file}" \
  --from-file=tls.crt="${cert_file}" \
  --from-file=tls.key="${key_file}" \
  --dry-run=client \
  -o yaml
