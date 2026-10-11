#!/usr/bin/env bash
# Executes REAL relay-server and relay-agent main packages against each other.
# Unlike compat_probe, this runs the production entry points, config loader,
# isolated SQLite device approval, installation private key and signed login.
# This is Linux CLI binary integration, NOT final Windows/macOS installer QA.
set -euo pipefail
repo="$(git rev-parse --show-toplevel)"
cd "$repo"
base="$(git merge-base origin/main HEAD)"
temp="$(mktemp -d)"
server_pid=""
agent_pid=""
origin_pid=""
cleanup_case() {
  for pid in "$agent_pid" "$server_pid" "$origin_pid"; do
    if [[ -n "$pid" ]]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  agent_pid=""
  server_pid=""
  origin_pid=""
}
cleanup() {
  cleanup_case
  git worktree remove --force "$temp/baseline" >/dev/null 2>&1 || true
  rm -rf "$temp"
}
trap cleanup EXIT
git worktree add --detach "$temp/baseline" "$base"
mkdir -p "$temp/baseline/scripts/compat_seed"
cp "$repo/scripts/compat_seed/main.go" "$temp/baseline/scripts/compat_seed/main.go"
for version in old new; do
  if [[ "$version" == old ]]; then root="$temp/baseline"; else root="$repo"; fi
  if [[ "$version" == new && -n "${RELAYPROXY_PACKAGED_NEW_DIR:-}" ]]; then
    test -s "$RELAYPROXY_PACKAGED_NEW_DIR/relay-server"
    test -s "$RELAYPROXY_PACKAGED_NEW_DIR/relay-agent"
    cp "$RELAYPROXY_PACKAGED_NEW_DIR/relay-server" "$temp/new-server"
    cp "$RELAYPROXY_PACKAGED_NEW_DIR/relay-agent" "$temp/new-agent"
    chmod +x "$temp/new-server" "$temp/new-agent"
    (cd "$root" && go build -trimpath -o "$temp/new-seed" ./scripts/compat_seed)
    echo "[full-bin] Using actual packaged Linux Agent/Server for feature head"
  else
    (
      cd "$root"
      go build -trimpath -o "$temp/$version-server" ./cmd/relay-server
      go build -trimpath -o "$temp/$version-agent" ./cmd/relay-agent
      go build -trimpath -o "$temp/$version-seed" ./scripts/compat_seed
    )
  fi
  "$temp/$version-agent" --version | grep -q 'relay-agent'
done
openssl req -new -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$temp/localhost.key" -out "$temp/localhost.crt" \
  -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  >/dev/null 2>&1

# Every pairing starts from a CLEAN database to avoid migrations from a newer
# server contaminating an old server's enrollment schema.
index=0
for combination in old-old old-new new-old new-new; do
  IFS=- read -r server_version agent_version <<<"$combination"
  for transport in quic_only tcp_only; do
    cleanup_case
    index=$((index + 1))
    quic_port=$((21000 + index * 4))
    tcp_port=$((quic_port + 1))
    admin_port=$((quic_port + 2))
    socks_port=$((quic_port + 3))
    origin_port=$((quic_port + 4))
    case_dir="$temp/$combination-$transport"
    mkdir -p "$case_dir/agent"
    db="$case_dir/lab.db"
    brutal_mbps=0
    if [[ "$combination" == "new-new" && "$transport" == "quic_only" ]]; then
      brutal_mbps=4
    fi
    "$temp/$server_version-seed" \
      -db "$db" -agent-dir "$case_dir/agent" \
      -quic-port "$quic_port" -tcp-port "$tcp_port" \
      -mode "$transport" -socks-port "$socks_port" \
      -brutal-mbps "$brutal_mbps" > "$case_dir/seed.log"

    cat > "$case_dir/server.yaml" <<YAML
server:
  tls_enabled: true
  quic:
    listen: "127.0.0.1:$quic_port"
  tls:
    listen: "127.0.0.1:$tcp_port"
  admin:
    listen: "127.0.0.1:$admin_port"
    tls_enabled: false
  cert_file: "$temp/localhost.crt"
  key_file: "$temp/localhost.key"
database:
  driver: sqlite
  dsn: "$db"
tunnel:
  heartbeat_sec: 5
  max_connections: 64
  max_connections_per_device: 16
p2p:
  enabled: false
direct:
  enabled: false
exit:
  enabled: true
  allow_internet: true
  allow_private_network: true
  allow_loopback: true
  upstream:
    mode: direct
relay_acl:
  allow_internet: true
  allow_private_network: true
  allow_loopback: true
logging:
  level: info
YAML

    echo "[full-bin] server=$server_version agent=$agent_version path=$transport"
    "$temp/$server_version-server" -config "$case_dir/server.yaml" \
      >"$case_dir/server.log" 2>&1 &
    server_pid=$!
    # Server admin listener is TCP regardless of test transport, allowing
    # readiness without causing any tunnel handshake.
    ready=0
    for attempt in $(seq 1 120); do
      if (echo >/dev/tcp/127.0.0.1/"$admin_port") 2>/dev/null; then
        ready=1
        break
      fi
      if ! kill -0 "$server_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if [[ "$ready" != "1" ]]; then
      echo "server failed to start: $combination $transport" >&2
      tail -n 65 "$case_dir/server.log" >&2
      exit 1
    fi
    # A separate HOME isolates desktop autostart state and private keys.
    HOME="$case_dir/agent" "$temp/$agent_version-agent" \
      --config "$case_dir/agent/relay-agent.yaml" --no-gui --no-web \
      >"$case_dir/agent.log" 2>&1 &
    agent_pid=$!
    approved=0
    for attempt in $(seq 1 200); do
      if grep -q '\[Agent\] Device approved. SessionID:' "$case_dir/agent.log"; then
        approved=1
        break
      fi
      if ! kill -0 "$agent_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if [[ "$approved" != "1" ]]; then
      echo "agent not authenticated: $combination $transport" >&2
      tail -n 75 "$case_dir/agent.log" >&2
      tail -n 75 "$case_dir/server.log" >&2
      exit 1
    fi
    # Real application payload: a loopback origin is reached through the
    # Agent SOCKS5 endpoint and the authenticated Server Exit (not a probe).
    mkdir -p "$case_dir/origin"
    printf 'relayproxy-compatibility-data-plane-%s-%s\n' "$combination" "$transport" \
      > "$case_dir/origin/fixture.bin"
    python3 -m http.server "$origin_port" --bind 127.0.0.1 --directory "$case_dir/origin" \
      > "$case_dir/origin.log" 2>&1 &
    origin_pid=$!
    proxy_ready=0
    for attempt in $(seq 1 30); do
      if (echo >/dev/tcp/127.0.0.1/"$socks_port") 2>/dev/null; then
        proxy_ready=1
        break
      fi
      sleep 0.1
    done
    if [[ "$proxy_ready" != "1" ]]; then
      tail -n 55 "$case_dir/agent.log" >&2
      echo "SOCKS5 proxy listener not ready" >&2
      exit 1
    fi
    transfer_ok=0
    for attempt in $(seq 1 15); do
      if curl --disable --noproxy "" --proxy "socks5h://127.0.0.1:$socks_port" \
        --fail --silent --show-error --connect-timeout 2 --max-time 6 \
        "http://127.0.0.1:$origin_port/fixture.bin" -o "$case_dir/downloaded.bin" \
        >> "$case_dir/curl.log" 2>&1 &&
        cmp -s "$case_dir/downloaded.bin" "$case_dir/origin/fixture.bin"; then
        transfer_ok=1
        break
      fi
      sleep 0.2
    done
    if [[ "$transfer_ok" != "1" ]]; then
      echo "real SOCKS5 -> Server Exit -> HTTP origin payload failed: $combination $transport" >&2
      tail -n 55 "$case_dir/agent.log" >&2
      tail -n 55 "$case_dir/server.log" >&2
      tail -n 25 "$case_dir/curl.log" >&2
      exit 1
    fi
    if [[ "$brutal_mbps" != "0" ]]; then
      grep -q 'Relay QUIC upload switched to Brutal' "$case_dir/agent.log" || {
        echo "Agent did not negotiate Brutal upload on new/new QUIC" >&2
        exit 1
      }
      grep -q 'Relay QUIC download switched to Brutal' "$case_dir/server.log" || {
        echo "Server did not install Brutal download on new/new QUIC" >&2
        exit 1
      }
      echo "[full-bin] new/new QUIC Brutal installed on both sending endpoints at 4 Mbps"
    fi
    echo "[full-bin] real proxy HTTP payload verified: $combination $transport"
    if ! kill -0 "$server_pid" 2>/dev/null || ! kill -0 "$agent_pid" 2>/dev/null; then
      echo "process exited immediately after signed authentication" >&2
      exit 1
    fi
    echo "[full-bin] PASS server=$server_version agent=$agent_version transport=$transport"
    cleanup_case
  done
done
echo "[full-bin] All 8 pairings authenticated using production entrypoint binaries"
echo "[full-bin] This does NOT establish complete release installer, GUI, TLS certificate trust, or real-WAN proxy traffic compatibility"
