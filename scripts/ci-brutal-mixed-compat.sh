#!/usr/bin/env bash
# Build one probe against pre-Brutal main and another against the current branch.
# The executable links the production Gateway, tunnel and identity handshake.
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
cd "$repo"
base="$(git merge-base origin/main HEAD)"
tmp="$(mktemp -d)"
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  git worktree remove --force "$tmp/baseline" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

git worktree add --detach "$tmp/baseline" "$base"
mkdir -p "$tmp/baseline/scripts/compat_probe"
cp "$repo/scripts/compat_probe/main.go" "$tmp/baseline/scripts/compat_probe/main.go"

(cd "$tmp/baseline" && go build -o "$tmp/old-probe" ./scripts/compat_probe)
(cd "$repo" && go build -o "$tmp/new-probe" ./scripts/compat_probe)

for direction in old-new new-old new-new; do
  case "$direction" in
    old-new) server="$tmp/old-probe"; client="$tmp/new-probe" ;;
    new-old) server="$tmp/new-probe"; client="$tmp/old-probe" ;;
    new-new) server="$tmp/new-probe"; client="$tmp/new-probe" ;;
  esac
  for transport in quic tls; do
    ready="$tmp/ready-$direction-$transport"
    echo "[compat] $direction transport=$transport"
    timeout 30s "$server" -role server -transport "$transport" -ready-file "$ready" \
      >"$tmp/server-$direction-$transport.log" 2>&1 &
    server_pid="$!"
    for _ in $(seq 1 150); do
      if [[ -s "$ready" ]]; then break; fi
      if ! kill -0 "$server_pid" >/dev/null 2>&1; then break; fi
      sleep 0.05
    done
    if [[ ! -s "$ready" ]]; then
      cat "$tmp/server-$direction-$transport.log"
      echo "Server failed to start: $direction $transport" >&2
      exit 1
    fi
    "$client" -role client -transport "$transport" -addr "$(cat "$ready")"
    if ! wait "$server_pid"; then
      cat "$tmp/server-$direction-$transport.log"
      echo "Mixed binary handshake failed: $direction $transport" >&2
      exit 1
    fi
    server_pid=""
    cat "$tmp/server-$direction-$transport.log"
  done
done
echo "[compat] all binary-pair QUIC/TLS handshake probes passed"
