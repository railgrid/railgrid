#!/usr/bin/env bash
# Service hooks for tracking running dev services.
# Usage:
#   source hack/scripts/service-hooks.sh
#   service_start dex $$
#   service_stop dex
#   require_service dex "make run-dex"
#   require_service_not_running kcp "make dev-run-kcp"

set -euo pipefail

HOOKS_DIR="${HOOKS_DIR:-.hooks}"

# Ensure hooks directory exists
mkdir -p "$HOOKS_DIR"

# Start a service - records PID and sets up cleanup trap
# Usage: service_start <name> <pid>
service_start() {
    local name="$1"
    local pid="$2"
    local pidfile="$HOOKS_DIR/${name}.pid"

    echo "$pid" > "$pidfile"
    echo "Service '$name' started (PID: $pid, pidfile: $pidfile)"

    # Set up trap to clean up on exit
    trap "service_stop $name" EXIT INT TERM
}

# Stop a service - removes PID file
# Usage: service_stop <name>
service_stop() {
    local name="$1"
    local pidfile="$HOOKS_DIR/${name}.pid"

    if [[ -f "$pidfile" ]]; then
        rm -f "$pidfile"
        echo "Service '$name' stopped (removed $pidfile)"
    fi
}

# Check if a service is running
# Usage: service_is_running <name>
# Returns: 0 if running, 1 if not
service_is_running() {
    local name="$1"
    local pidfile="$HOOKS_DIR/${name}.pid"

    if [[ ! -f "$pidfile" ]]; then
        return 1
    fi

    local pid
    pid=$(cat "$pidfile")

    # Check if process is still running
    if kill -0 "$pid" 2>/dev/null; then
        return 0
    else
        # PID file exists but process is dead - clean up
        rm -f "$pidfile"
        return 1
    fi
}

# Require a service to be running before proceeding
# Usage: require_service <name> <start_hint>
require_service() {
    local name="$1"
    local start_hint="$2"

    if ! service_is_running "$name"; then
        echo ""
        echo "ERROR: Service '$name' is not running."
        echo "       Please start it first with: $start_hint"
        echo ""
        exit 1
    fi

    echo "✓ Service '$name' is running"
}

# Require a service to NOT be running (e.g., when using embedded mode)
# Usage: require_service_not_running <name> <conflict_hint>
require_service_not_running() {
    local name="$1"
    local conflict_hint="$2"

    if service_is_running "$name"; then
        echo ""
        echo "ERROR: Service '$name' is already running."
        echo "       This conflicts with: $conflict_hint"
        echo "       Please stop it first or use a different run mode."
        echo ""
        exit 1
    fi
}

# Clean up stale PID files (processes that are no longer running)
# Usage: cleanup_stale_hooks
cleanup_stale_hooks() {
    shopt -s nullglob
    for pidfile in "$HOOKS_DIR"/*.pid; do
        [[ -f "$pidfile" ]] || continue
        local name
        name=$(basename "$pidfile" .pid)
        if ! service_is_running "$name"; then
            echo "Cleaned up stale pidfile for '$name'"
        fi
    done
    shopt -u nullglob
}

# List all running services
# Usage: list_services
list_services() {
    echo "Running services:"
    local found=false
    shopt -s nullglob
    for pidfile in "$HOOKS_DIR"/*.pid; do
        [[ -f "$pidfile" ]] || continue
        local name
        name=$(basename "$pidfile" .pid)
        if service_is_running "$name"; then
            local pid
            pid=$(cat "$pidfile")
            echo "  - $name (PID: $pid)"
            found=true
        fi
    done
    shopt -u nullglob
    if [[ "$found" == "false" ]]; then
        echo "  (none)"
    fi
}
