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

echo "[3/3] Embed Windows icons into resource_windows.syso"
( cd "$ROOT/cmd/relay-server" && goversioninfo -64 -o resource_windows.syso versioninfo.json )
( cd "$ROOT/cmd/relay-agent" && goversioninfo -64 -o resource_windows.syso versioninfo.json )

echo "Brand assets ready."
