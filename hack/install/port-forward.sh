#!/usr/bin/env bash
# Local access to the stack: port-forwards for the Envoy gateway (8443 — kcp
# front-proxy, shards and the hub via SNI) and the hub Service (9443 — direct,
# what ${HUB_EXTERNAL_URL} points at).
#
#   hack/install/port-forward.sh start    # background, pidfiles in state dir
#   hack/install/port-forward.sh stop
#
# Each forward runs in a small supervisor loop: kubectl port-forward exits
# whenever its target pod restarts (e.g. right after a helm upgrade), so a
# one-shot forward silently dies. The loop re-resolves the Service and
# re-attaches until stopped.
#
# In production these are replaced by the gateway's LoadBalancer + DNS
# (see 09-cloudflare-dns.sh).

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require kubectl

PIDDIR="${FAROS_INSTALL_STATE_DIR}"
GATEWAY_PID="${PIDDIR}/port-forward-gateway.pid"
HUB_PID="${PIDDIR}/port-forward-hub.pid"

stop_one() {
  local pidfile="$1" pattern="$2"
  if [[ -f "${pidfile}" ]]; then
    kill "$(cat "${pidfile}")" 2>/dev/null || true
    rm -f "${pidfile}"
  fi
  # The supervisor's current kubectl child survives the kill above.
  pkill -f "${pattern}" 2>/dev/null || true
}

# forward_loop <namespace> <service-or-selector> <ports>
# service-or-selector: either "svc/<name>" or "selector:<label-selector>"
# (the Envoy service name is generated, so it must be re-resolved on every
# reconnect — the pod AND service can change during a repave).
forward_loop() {
  local ns="$1" target="$2" ports="$3"
  while true; do
    local svc="${target#svc/}"
    if [[ "${target}" == selector:* ]]; then
      svc="$(kc -n "${ns}" get svc -l "${target#selector:}" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
      if [[ -z "${svc}" ]]; then
        sleep 2
        continue
      fi
    fi
    kc -n "${ns}" port-forward "svc/${svc}" "${ports}" || true
    sleep 2
  done
}

start() {
  stop # idempotent restart

  (forward_loop envoy-gateway-system \
    "selector:gateway.envoyproxy.io/owning-gateway-name=eg" "8443:8443") \
    >"${PIDDIR}/port-forward-gateway.log" 2>&1 &
  echo $! > "${GATEWAY_PID}"
  echo "gateway  → https://${KCP_DOMAIN}:8443 (supervisor pid $(cat "${GATEWAY_PID}"))"

  (forward_loop "${HUB_NAMESPACE}" "svc/faros-hub" "9443:9443") \
    >"${PIDDIR}/port-forward-hub.log" 2>&1 &
  echo $! > "${HUB_PID}"
  echo "hub      → ${HUB_EXTERNAL_URL} (supervisor pid $(cat "${HUB_PID}"))"

  # Give the forwards a moment to establish before callers probe them.
  sleep 2
}

stop() {
  stop_one "${GATEWAY_PID}" "port-forward svc/.* 8443:8443"
  stop_one "${HUB_PID}" "port-forward svc/faros-hub 9443:9443"
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
