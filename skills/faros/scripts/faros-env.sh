# shellcheck shell=bash
# Source this file to resolve every identifier a faros shell session needs:
#
#   . faros-env.sh            # uses the kubeconfig context "faros"
#
# Exports HUB, CLUSTER, ORG, WS, TOKEN, AS (App Studio base URL),
# MCP_URL, MCP_TOKEN, and defines three helpers:
#
#   fc  <curl args…>          REST call with bearer + tenant headers
#   mcp <tool> <json|@file>   call an aggregate MCP tool; prints the tool's
#                             JSON result (or its text) and returns 1 when
#                             the tool reported an error
#   faros_refresh_token       re-mint TOKEN (OIDC tokens expire; MCP_TOKEN does not)
#
# Needs: kubectl, curl, jq, and either `faros` or `kubectl faros` on PATH.
# Works in bash and zsh. Prints nothing on success except one summary line.

_faros_ctx=${FAROS_CONTEXT:-faros}

_faros_server=$(kubectl config view --raw -o \
  "jsonpath={.clusters[?(@.name==\"$(kubectl config view --raw -o "jsonpath={.contexts[?(@.name==\"$_faros_ctx\")].context.cluster}")\")].cluster.server}")
if [ -z "$_faros_server" ]; then
  echo "faros-env: no kubeconfig context '$_faros_ctx'; run 'faros login' first" >&2
  return 1 2>/dev/null || exit 1
fi
HUB=${FAROS_HUB_URL:-${_faros_server%%/clusters/*}}
CLUSTER=${_faros_server##*/clusters/}

faros_refresh_token() {
  local static healthz issuer client cli
  static=$(kubectl config view --raw -o \
    "jsonpath={.users[?(@.name==\"$(kubectl config view --raw -o "jsonpath={.contexts[?(@.name==\"$_faros_ctx\")].context.user}")\")].user.token}")
  if [ -n "$static" ]; then TOKEN=$static; return 0; fi
  healthz=$(curl -s "$HUB/healthz")
  issuer=$(printf '%s' "$healthz" | jq -r '.issuerUrl // empty')
  client=$(printf '%s' "$healthz" | jq -r '.clientId // empty')
  if command -v faros >/dev/null 2>&1; then cli=faros; else cli="kubectl faros"; fi
  TOKEN=$($cli get-token --oidc-issuer-url "$issuer" --oidc-client-id "$client" | jq -r .status.token)
  [ -n "$TOKEN" ] && [ "$TOKEN" != null ]
}
faros_refresh_token || { echo "faros-env: could not mint a token; run 'faros login'" >&2; return 1 2>/dev/null || exit 1; }

# Resolve org and workspace UUIDs from the cluster ID the kubeconfig points at.
if [ -z "${ORG:-}" ] || [ -z "${WS:-}" ]; then
  for _o in $(curl -s "$HUB/api/orgs" -H "Authorization: Bearer $TOKEN" | jq -r '.items[].uuid'); do
    _w=$(curl -s "$HUB/api/orgs/$_o/workspaces" -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $_o" \
      | jq -r --arg c "$CLUSTER" '.items[] | select(.clusterName == $c) | .uuid')
    if [ -n "$_w" ]; then ORG=$_o; WS=$_w; break; fi
  done
fi
if [ -z "${ORG:-}" ]; then
  echo "faros-env: cluster $CLUSTER is not a workspace of any org you belong to" >&2
  return 1 2>/dev/null || exit 1
fi
AS=$HUB/services/providers/app-studio

fc() {
  curl -s -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" "$@"
}

_faros_connect=$(fc "$HUB/api/orgs/$ORG/workspaces/$WS/mcpservers/default/connect")
MCP_URL=$(printf '%s' "$_faros_connect" | jq -r '.endpointURL // empty')
MCP_TOKEN=$(printf '%s' "$_faros_connect" | jq -r '.token // empty')

# mcp <tool> <json args | @file>. Replies are SSE; a failing tool is still
# HTTP 200 with isError in the result, so this checks the body, not the status.
mcp() {
  local tool=$1 args=${2:-'{}'} req out
  [ "${args#@}" != "$args" ] && args=$(cat "${args#@}")
  req=$(mktemp)
  jq -n --arg n "$tool" --argjson a "$args" \
    '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}' > "$req" || { rm -f "$req"; return 2; }
  out=$(curl -s -X POST "$MCP_URL" -H "Authorization: Bearer $MCP_TOKEN" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    --data-binary @"$req" | sed -n 's/^data: //p' | tail -n 1)
  rm -f "$req"
  printf '%s' "$out" | jq -e '.error' >/dev/null 2>&1 && { printf '%s\n' "$out" | jq -c .error >&2; return 1; }
  printf '%s' "$out" | jq -r '.result.structuredContent // (.result.content[0].text | (fromjson? // .))'
  printf '%s' "$out" | jq -e '.result.isError == true' >/dev/null 2>&1 && return 1
  return 0
}

export HUB CLUSTER ORG WS TOKEN AS MCP_URL MCP_TOKEN
echo "faros-env: hub=$HUB cluster=$CLUSTER org=$ORG ws=$WS mcp=$([ -n "$MCP_TOKEN" ] && echo ready || echo unavailable)" >&2
unset _faros_server _faros_connect _o _w
