#!/bin/sh
# Padavan supervisor for the headless RelayProxy Router.
# Export RELAY_* configuration before calling this script.
set -eu

BIN="${RELAY_ROUTER_BIN:-/tmp/relay-router}"
DATA_DIR="${RELAY_ROUTER_DATA_DIR:-/etc/storage/relayproxy}"
PID_FILE="${RELAY_ROUTER_PID_FILE:-/tmp/relay-router.pid}"
CHILD_PID_FILE="${RELAY_ROUTER_CHILD_PID_FILE:-/tmp/relay-router.child.pid}"
LOG_FILE="${RELAY_ROUTER_LOG_FILE:-/tmp/relay-router.log}"
MAX_BACKOFF="${RELAY_ROUTER_MAX_BACKOFF:-60}"

log() {
    echo "[RelayProxy Router supervisor] $(date '+%Y-%m-%d %H:%M:%S') $*" >>"$LOG_FILE"
}

rotate_log() {
    [ -f "$LOG_FILE" ] || return 0
    size="$(wc -c <"$LOG_FILE" | tr -d ' ')"
    if [ "${size:-0}" -ge 1048576 ]; then
        mv -f "$LOG_FILE" "$LOG_FILE.1"
    fi
}

watch() {
    # A watch process owns the PID file; the Router child has a separate PID.
    trap 'log "stop requested"; if [ -n "${child:-}" ]; then kill -TERM "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true; fi; rm -f "$CHILD_PID_FILE" "$PID_FILE"; exit 0' INT TERM
    backoff=2
    while :; do
        rotate_log
        if [ ! -x "$BIN" ]; then
            log "binary missing/not executable: $BIN; retrying in ${backoff}s"
            sleep "$backoff"
        else
            # Build arguments in a positional-parameter-safe way.
            set -- --server "$RELAY_SERVER" --identity-id "$RELAY_IDENTITY_ID" --name "$RELAY_DEVICE_NAME" --data-dir "$DATA_DIR" --max-connections "${RELAY_MAX_CONNECTIONS:-64}"
            [ -z "${RELAY_SERVER_NAME:-}" ] || set -- "$@" --server-name "$RELAY_SERVER_NAME"
            [ -z "${RELAY_CA_FILE:-}" ] || set -- "$@" --ca-file "$RELAY_CA_FILE"
            log "starting Router child"
            "$BIN" "$@" >>"$LOG_FILE" 2>&1 </dev/null &
            child=$!
            echo "$child" >"$CHILD_PID_FILE"
            wait "$child" || rc=$?
            rc="${rc:-0}"
            child=""
            rm -f "$CHILD_PID_FILE"
            log "Router child exited (status $rc); restart in ${backoff}s"
            unset rc
            sleep "$backoff"
        fi
        backoff=$((backoff * 2))
        [ "$backoff" -le "$MAX_BACKOFF" ] || backoff="$MAX_BACKOFF"
    done
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print tolower($1)}'
    else
        openssl dgst -sha256 "$1" | sed 's/^.*= //' | tr 'A-F' 'a-f'
    fi
}

verify_router_binary() {
    file="$1"
    expected="$(echo "$2" | tr 'A-F' 'a-f')"
    case "$expected" in *[!0-9a-f]*|"") echo "Invalid SHA-256 value" >&2; return 1 ;; esac
    [ "${#expected}" -eq 64 ] || { echo "SHA-256 must contain 64 hexadecimal characters" >&2; return 1; }
    [ -s "$file" ] || { echo "Binary is empty: $file" >&2; return 1; }
    actual="$(sha256_file "$file")"
    [ "$actual" = "$expected" ] || { echo "SHA-256 mismatch for $file" >&2; return 1; }
    busybox hexdump -C -n 6 "$file" | grep -q '7f 45 4c 46 01 01' || {
        echo "Not a 32-bit little-endian ELF binary (expected linux/mipsle)" >&2
        return 1
    }
}

upgrade() {
    source_file="${2:-}"
    expected_sha="${3:-}"
    [ -n "$source_file" ] && [ -n "$expected_sha" ] || {
        echo "Usage: $0 upgrade <new-binary-file> <sha256>" >&2
        return 2
    }
    [ -f "$source_file" ] || { echo "Upgrade file not found: $source_file" >&2; return 1; }
    verify_router_binary "$source_file" "$expected_sha"
    mkdir -p "$(dirname "$BIN")"
    staged="$BIN.new.$"
    cp "$source_file" "$staged"
    chmod 0755 "$staged"
    verify_router_binary "$staged" "$expected_sha"
    was_running=0
    if status >/dev/null 2>&1; then was_running=1; fi
    if [ "$was_running" -eq 1 ]; then stop; fi
    backup="$BIN.previous"
    [ ! -f "$BIN" ] || cp "$BIN" "$backup"
    mv -f "$staged" "$BIN"
    if [ "$was_running" -eq 1 ]; then
        if ! start; then
            echo "New binary failed to start; rolling back previous binary" >&2
            [ ! -f "$backup" ] || cp "$backup" "$BIN"
            start || true
            return 1
        fi
    fi
    echo "RelayProxy Router binary upgraded. Previous version: $backup"
}

rollback() {
    backup="$BIN.previous"
    [ -x "$backup" ] || { echo "No executable rollback binary at $backup" >&2; return 1; }
    was_running=0
    if status >/dev/null 2>&1; then was_running=1; fi
    if [ "$was_running" -eq 1 ]; then stop; fi
    current="$BIN.rollback"
    [ ! -f "$BIN" ] || cp "$BIN" "$current"
    cp "$backup" "$BIN"
    chmod 0755 "$BIN"
    if [ "$was_running" -eq 1 ]; then
        if ! start; then
            [ ! -f "$current" ] || cp "$current" "$BIN"
            start || true
            return 1
        fi
    fi
    echo "RelayProxy Router rolled back to previous binary"
}

start() {
    # Validate mandatory config before creating a supervisor.
    : "${RELAY_SERVER:?Set RELAY_SERVER to host:port or host}"
    : "${RELAY_IDENTITY_ID:?Set RELAY_IDENTITY_ID to the server-issued identity ID}"
    : "${RELAY_DEVICE_NAME:?Set RELAY_DEVICE_NAME to the router name}"
    if [ -f "$PID_FILE" ]; then
        old_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
        if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
            echo "RelayProxy Router supervisor already running (pid $old_pid)"
            return 0
        fi
        rm -f "$PID_FILE" "$CHILD_PID_FILE"
    fi
    case "$MAX_BACKOFF" in *[!0-9]*|"") echo "RELAY_ROUTER_MAX_BACKOFF must be an integer" >&2; return 1 ;; esac
    [ "$MAX_BACKOFF" -ge 2 ] || { echo "RELAY_ROUTER_MAX_BACKOFF must be at least 2" >&2; return 1; }
    mkdir -p "$DATA_DIR"
    "$0" watch >/dev/null 2>&1 &
    pid=$!
    echo "$pid" >"$PID_FILE"
    sleep 1
    if ! kill -0 "$pid" 2>/dev/null; then
        rm -f "$PID_FILE"
        echo "RelayProxy Router supervisor failed to start; see $LOG_FILE" >&2
        tail -n 40 "$LOG_FILE" 2>/dev/null || true
        return 1
    fi
    echo "RelayProxy Router supervisor started (pid $pid)"
}

stop() {
    [ -f "$PID_FILE" ] || { echo "RelayProxy Router is not running"; return 0; }
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        kill -TERM "$pid"
        n=0
        while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 15 ]; do
            sleep 1
            n=$((n + 1))
        done
        if kill -0 "$pid" 2>/dev/null; then
            echo "RelayProxy Router supervisor did not stop within 15 seconds" >&2
            return 1
        fi
    fi
    rm -f "$PID_FILE" "$CHILD_PID_FILE"
    echo "RelayProxy Router stopped"
}

status() {
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        child="$(cat "$CHILD_PID_FILE" 2>/dev/null || true)"
        if [ -n "$child" ] && kill -0 "$child" 2>/dev/null; then
            echo "RelayProxy Router supervised and running (supervisor $pid, child $child)"
        else
            echo "RelayProxy Router supervisor running (pid $pid); child restarting or waiting"
        fi
        return 0
    fi
    echo "RelayProxy Router stopped"
    return 1
}

case "${1:-}" in
    start) start ;;
    stop) stop ;;
    restart) stop; start ;;
    status) status ;;
    watch) watch ;;
    *) echo "Usage: $0 {start|stop|restart|status}" >&2; exit 2 ;;
esac
