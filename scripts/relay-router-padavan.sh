#!/bin/sh
# Padavan launcher for RelayProxy Router.
# Configure the four RELAY_* variables in started_script.sh or export them
# before invoking this script. The Go binary itself has no config-file mode.
set -eu

BIN="${RELAY_ROUTER_BIN:-/tmp/relay-router}"
DATA_DIR="${RELAY_ROUTER_DATA_DIR:-/etc/storage/relayproxy}"
PID_FILE="${RELAY_ROUTER_PID_FILE:-/tmp/relay-router.pid}"
LOG_FILE="${RELAY_ROUTER_LOG_FILE:-/tmp/relay-router.log}"

start() {
    : "${RELAY_SERVER:?Set RELAY_SERVER to host:port or host}"
    : "${RELAY_IDENTITY_ID:?Set RELAY_IDENTITY_ID to the server-issued identity ID}"
    : "${RELAY_DEVICE_NAME:?Set RELAY_DEVICE_NAME to the router name}"
    if [ ! -x "$BIN" ]; then
        echo "RelayProxy Router binary is missing or not executable: $BIN" >&2
        return 1
    fi
    if [ -f "$PID_FILE" ]; then
        old_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
        if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
            echo "RelayProxy Router already running (pid $old_pid)"
            return 0
        fi
        rm -f "$PID_FILE"
    fi

    mkdir -p "$DATA_DIR"
    set -- --server "$RELAY_SERVER" --identity-id "$RELAY_IDENTITY_ID" --name "$RELAY_DEVICE_NAME" --data-dir "$DATA_DIR" --max-connections "${RELAY_MAX_CONNECTIONS:-64}"
    if [ -n "${RELAY_SERVER_NAME:-}" ]; then
        set -- "$@" --server-name "$RELAY_SERVER_NAME"
    fi
    if [ -n "${RELAY_CA_FILE:-}" ]; then
        set -- "$@" --ca-file "$RELAY_CA_FILE"
    fi
    "$BIN" "$@" >>"$LOG_FILE" 2>&1 </dev/null &
    pid=$!
    echo "$pid" >"$PID_FILE"
    sleep 1
    if ! kill -0 "$pid" 2>/dev/null; then
        rm -f "$PID_FILE"
        echo "RelayProxy Router failed to start; see $LOG_FILE" >&2
        tail -n 40 "$LOG_FILE" 2>/dev/null || true
        return 1
    fi
    echo "RelayProxy Router started (pid $pid)"
}

stop() {
    if [ ! -f "$PID_FILE" ]; then
        echo "RelayProxy Router is not running"
        return 0
    fi
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        kill -TERM "$pid"
        n=0
        while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 10 ]; do
            sleep 1
            n=$((n + 1))
        done
        if kill -0 "$pid" 2>/dev/null; then
            echo "RelayProxy Router did not stop within 10 seconds" >&2
            return 1
        fi
    fi
    rm -f "$PID_FILE"
    echo "RelayProxy Router stopped"
}

status() {
    if [ -f "$PID_FILE" ]; then
        pid="$(cat "$PID_FILE" 2>/dev/null || true)"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            echo "RelayProxy Router running (pid $pid)"
            return 0
        fi
    fi
    echo "RelayProxy Router stopped"
    return 1
}

case "${1:-}" in
    start) start ;;
    stop) stop ;;
    restart) stop; start ;;
    status) status ;;
    *) echo "Usage: $0 {start|stop|restart|status}" >&2; exit 2 ;;
esac
