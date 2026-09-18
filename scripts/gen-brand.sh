#!/usr/bin/env bash
# Generate brand icons + Windows .syso resource files
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "[1/3] Generate PNG/ICO assets"
go run scripts/genicon.go "$ROOT"

echo "[2/3] Ensure goversioninfo"
if ! command -v goversioninfo >/dev/null 2>&1; then
  go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
  export PATH="$(go env GOPATH)/bin:$PATH"
fi

echo "[3/3] Embed Windows icons into architecture-specific resources"
generate_windows_resources() {
  local package_dir="$1"
  (
    cd "$package_dir"
    rm -f resource_windows.syso
    goversioninfo -64 -arm=false -o resource_windows_amd64.syso versioninfo.json
    goversioninfo -64 -arm=true -o resource_windows_arm64.syso versioninfo.json
  )
}
generate_windows_resources "$ROOT/cmd/relay-server"
generate_windows_resources "$ROOT/cmd/relay-agent"

echo "Brand assets ready."
