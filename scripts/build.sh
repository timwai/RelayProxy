#!/usr/bin/env bash
# RelayProxy 跨平台发布编译脚本
# 产出：Linux 服务端/Agent、macOS Agent、Windows 客户端（含图标）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
VERSION="${VERSION:-1.0.0}"
LDFLAGS="-s -w -X main.Version=${VERSION}"

echo "=================================================="
echo " RelayProxy Build  v${VERSION}"
echo " Root:   ${ROOT}"
echo " OutDir: ${OUT_DIR}"
echo " Time:   $(date '+%Y-%m-%d %H:%M:%S')"
echo "=================================================="

cd "$ROOT"

echo "[prep] Generate brand icons + Windows resources"
bash "$ROOT/scripts/gen-brand.sh"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

echo "[prep] Verify and embed the official WinDivert runtime"
go run ./scripts/fetch-windivert.go \
  -out "$OUT_DIR/windows-amd64/windivert" \
  -embed-archive "$ROOT/agent/divert/windivert/WinDivert-2.2.2-A.zip"

SYSO_SERVER="$ROOT/cmd/relay-server/resource_windows.syso"
SYSO_AGENT="$ROOT/cmd/relay-agent/resource_windows.syso"

hide_syso() {
  for f in "$SYSO_SERVER" "$SYSO_AGENT"; do
    [[ -f "$f" ]] && mv "$f" "${f}.bak"
  done
}
show_syso() {
  for f in "$SYSO_SERVER" "$SYSO_AGENT"; do
    [[ -f "${f}.bak" ]] && mv "${f}.bak" "$f"
  done
}

build_one() {
  local goos="$1" goarch="$2" pkg="$3" out="$4"
  local extra_flags="${5:-}"
  mkdir -p "$(dirname "$out")"
  echo ""
  echo "[BUILD] ${goos}/${goarch}  ${pkg} -> ${out}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS $extra_flags" -o "$out" "$pkg"
  local size
  size="$(wc -c < "$out" | tr -d ' ')"
  echo "  OK  $(awk -v s="$size" 'BEGIN{printf "%.2f MB", s/1024/1024}')"
}

# Linux server/agent and macOS agent — hide Windows .syso during cross-compile
hide_syso
trap show_syso EXIT
build_one linux amd64 ./cmd/relay-server "$OUT_DIR/linux-amd64/relay-server"
build_one linux arm64 ./cmd/relay-server "$OUT_DIR/linux-arm64/relay-server"
build_one linux amd64 ./cmd/relay-agent "$OUT_DIR/linux-amd64/relay-agent"
build_one linux arm64 ./cmd/relay-agent "$OUT_DIR/linux-arm64/relay-agent"
build_one darwin amd64 ./cmd/relay-agent "$OUT_DIR/darwin-amd64/relay-agent"
build_one darwin arm64 ./cmd/relay-agent "$OUT_DIR/darwin-arm64/relay-agent"
show_syso
trap - EXIT

# Windows client + server (icons embedded)
build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent-gui.exe" "-H=windowsgui"
build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent.exe"
build_one windows amd64 ./cmd/relay-server "$OUT_DIR/windows-amd64/relay-server.exe"

# Configs + brand files
for t in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do
  mkdir -p "$OUT_DIR/$t/configs" "$OUT_DIR/$t/brand"
  cp "$ROOT/configs/relay-server.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/configs/relay-agent.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/assets/brand/logo.png" "$OUT_DIR/$t/brand/"
done
cp "$ROOT/assets/brand/icon.ico" "$OUT_DIR/windows-amd64/brand/"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-arm64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-arm64/brand/icon.png"
cp "$ROOT/docs/windows-transparent-proxy.md" "$OUT_DIR/windows-amd64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-amd64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-arm64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-amd64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-arm64/README.md"
go run ./scripts/fetch-windivert.go \
  -out "$OUT_DIR/windows-amd64/windivert" \
  -agent-zip "$OUT_DIR/RelayProxy-agent-windows-amd64.zip"

# Checksums
echo ""
echo "[SHA256]"
(
  cd "$OUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-agent-windows-amd64.zip' \) \
      | sort | while read -r f; do sha256sum "$f"; done | tee SHA256SUMS.txt
  else
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-agent-windows-amd64.zip' \) \
      | sort | while read -r f; do shasum -a 256 "$f"; done | tee SHA256SUMS.txt
  fi
)

echo ""
echo "=================================================="
echo " Build complete -> ${OUT_DIR}"
echo "=================================================="
