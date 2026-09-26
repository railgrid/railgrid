#!/usr/bin/env bash
# Run kcp from an image instead of the server compiled into the hub, for trying
# a kcp change (a PR build, say) without moving this repository's kcp
# dependency. The hub then starts with --external-kcp; see the Tiltfile.
#
#   hack/kcp-external.sh ghcr.io/kcp-dev/kcp-prs:pr-4385-d438e2ed0 .kcp-external
#
# Two things the image needs help with. Its own binary sits at /kcp, so the
# state directory is mounted at /data instead. And kcp writes the address it
# sees itself on into admin.kubeconfig — the container's IP, which nothing on
# the host can reach — so the server URL is rewritten to the published port
# once the file appears. The serving certificate already carries localhost.
set -euo pipefail

image="${1:?usage: kcp-external.sh <image> [root-dir] [port] [container-name]}"
root="${2:-.kcp-external}"
port="${3:-6443}"
# A second instance (the provider e2e runs one on its own port) needs its own
# container name, or starting it would tear down the Tilt one.
name="${4:-railgrid-kcp-external}"

# Refuse to start when something else already owns the port. Docker reports a
# published port even when the bind lost to a host process, so without this the
# container runs, writes a kubeconfig nobody reaches, and the hub fails much
# later against the OTHER server with "certificate signed by unknown authority"
# — which reads like a TLS problem and is not one.
if command -v lsof >/dev/null 2>&1; then
  # lsof exits non-zero when nothing holds the port, which under `set -e` with
  # pipefail would end the script exactly when it should carry on. The `|| true`
  # is what makes "port is free" the success case.
  holder="$(lsof -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {print $1" (pid "$2")"; exit}' || true)"
  if [ -n "$holder" ]; then
    echo "ERROR: ${holder} is already listening on :${port}." >&2
    echo "       kcp cannot take the port, and the hub would talk to that server instead." >&2
    echo "       Stop it first, or run with a different port." >&2
    exit 1
  fi
fi

mkdir -p "$root"
root_abs="$(cd "$root" && pwd)"
docker rm -f "$name" >/dev/null 2>&1 || true

# The kubeconfig is rewritten in the background: the server has to be running
# before it exists, and this script stays in the foreground as Tilt's process.
# Only the host:port is replaced: each context's server keeps its
# /clusters/<path> suffix, which is what tells the hub WHICH workspace a
# context addresses (root, system:admin, ...). kcp writes the file more than
# once while it starts, so the rewrite runs again after a pause; sed is
# idempotent on an already rewritten file.
(
  until [ -f "$root_abs/admin.kubeconfig" ]; do sleep 1; done
  for _ in 1 2 3; do
    sed -i.bak -E "s#server: https://[^/[:space:]]+#server: https://localhost:${port}#" "$root_abs/admin.kubeconfig"
    rm -f "$root_abs/admin.kubeconfig.bak"
    sleep 2
  done
  echo "kcp admin kubeconfig ready at $root_abs/admin.kubeconfig (server https://localhost:${port}, cluster paths kept)"
) &

# Static-token users: the embedded server writes token-auth-file.csv into its
# root directory (pkg/hub/kcp/embedded.go); a caller that wants the same users
# on this server drops the same file into the root directory before starting
# it, and kcp is told to read it. Without it a static token is forwarded by the
# hub unchanged and this server answers 401.
# The same feature gates the hub enables for its embedded server
# (pkg/hub/kcp/embedded.go). CacheAPIs is what makes a custom subresource's
# storage.virtual reference real: without it no ClusterCachedResource is
# created for the referenced endpoint slice, the shard has nowhere to route
# the subresource, and every request to one is a 404 that looks like the
# object is missing.
extra_args=(--feature-gates=CacheAPIs=true,WorkspaceMounts=true)
# KCP_EXTERNAL_EXTRA_ARGS: more kcp start flags, whitespace-separated (e.g. --v=3).
if [ -n "${KCP_EXTERNAL_EXTRA_ARGS:-}" ]; then
  # shellcheck disable=SC2206 # splitting on whitespace is the point
  extra_args+=(${KCP_EXTERNAL_EXTRA_ARGS})
fi
if [ -f "$root_abs/token-auth-file.csv" ]; then
  extra_args+=(--token-auth-file /data/token-auth-file.csv)
fi
exec docker run --rm --name "$name" \
  --user "$(id -u):$(id -g)" \
  -p "${port}:6443" \
  -v "${root_abs}:/data" \
  "$image" \
  start --root-directory /data --secure-port 6443 \
  --batteries-included admin,user,workspace-types \
  "${extra_args[@]}"
