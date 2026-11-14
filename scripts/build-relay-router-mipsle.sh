#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${OUT:-$ROOT/dist/relay-router-mipsle}"
VERSION="${VERSION:-dev}"

cd "$ROOT"
mkdir -p "$(dirname "$OUT")"
echo "[RelayProxy Router] Building linux/mipsle (softfloat)"
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.Version=$VERSION" \
  -o "$OUT" ./cmd/relay-router

chmod 0755 "$OUT"
size="$(wc -c < "$OUT" | tr -d ' ')"
echo "Built: $OUT"
awk -v s="$size" 'BEGIN { printf "Size: %.2f MiB (%d bytes)", s/1048576, s }'
echo
