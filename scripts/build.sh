#!/usr/bin/env bash
# RelayProxy 跨平台发布编译脚本
# 产出：Linux/macOS/Windows amd64/arm64 Server + Agent、Wails React Windows GUI、macOS App、完整分发包
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
SYSO_SERVER_AMD64="$ROOT/cmd/relay-server/resource_windows_amd64.syso"
SYSO_SERVER_ARM64="$ROOT/cmd/relay-server/resource_windows_arm64.syso"
SYSO_AGENT_AMD64="$ROOT/cmd/relay-agent/resource_windows_amd64.syso"
SYSO_AGENT_ARM64="$ROOT/cmd/relay-agent/resource_windows_arm64.syso"
SYSO_FILES=(
  "$SYSO_SERVER" "$SYSO_AGENT"
  "$SYSO_SERVER_AMD64" "$SYSO_SERVER_ARM64"
  "$SYSO_AGENT_AMD64" "$SYSO_AGENT_ARM64"
)

hide_syso() {
  for f in "${SYSO_FILES[@]}"; do
    [[ -f "$f" ]] && mv "$f" "${f}.bak"
  done
}
show_syso() {
  for f in "${SYSO_FILES[@]}"; do
    [[ -f "${f}.bak" ]] && mv "${f}.bak" "$f"
  done
}

build_react_frontend() {
  local frontend="$ROOT/agent/gui/frontend"
  local dist="$ROOT/agent/gui/react_dist"

  command -v node >/dev/null 2>&1 || {
    echo "ERROR: node was not found. Install Node.js 22+ before building the Windows React desktop client." >&2
    return 1
  }
  command -v npm >/dev/null 2>&1 || {
    echo "ERROR: npm was not found. Install Node.js 22+ before building the Windows React desktop client." >&2
    return 1
  }

  echo ""
  echo "[BUILD] Wails React frontend"
  if [[ -d "$dist" ]]; then
    find "$dist" -mindepth 1 -maxdepth 1 ! -name README.txt -exec rm -rf {} +
  fi
  (
    cd "$frontend"
    npm install --no-audit --no-fund
    npm test
    npm run build
  )

  [[ -f "$dist/index.html" ]] || {
    echo "ERROR: React frontend build did not produce agent/gui/react_dist/index.html" >&2
    return 1
  }
  [[ -d "$dist/assets" ]] || {
    echo "ERROR: React frontend build did not produce agent/gui/react_dist/assets" >&2
    return 1
  }
  local js_count css_count
  js_count="$(find "$dist/assets" -maxdepth 1 -type f -name '*.js' | wc -l | tr -d ' ')"
  css_count="$(find "$dist/assets" -maxdepth 1 -type f -name '*.css' | wc -l | tr -d ' ')"
  if [[ "$js_count" == "0" || "$css_count" == "0" ]]; then
    echo "ERROR: React frontend bundle is incomplete: expected at least one JS and CSS asset" >&2
    return 1
  fi
  echo "  React bundle OK: index.html + ${js_count} JS + ${css_count} CSS"
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

# Linux + macOS server/agent — hide Windows .syso during cross-compile
hide_syso
trap show_syso EXIT
build_one linux amd64 ./cmd/relay-server "$OUT_DIR/linux-amd64/relay-server"
build_one linux arm64 ./cmd/relay-server "$OUT_DIR/linux-arm64/relay-server"
build_one linux amd64 ./cmd/relay-agent "$OUT_DIR/linux-amd64/relay-agent"
build_one linux arm64 ./cmd/relay-agent "$OUT_DIR/linux-arm64/relay-agent"
build_one darwin amd64 ./cmd/relay-agent "$OUT_DIR/darwin-amd64/relay-agent"
build_one darwin amd64 ./cmd/relay-server "$OUT_DIR/darwin-amd64/relay-server"
build_one darwin arm64 ./cmd/relay-agent "$OUT_DIR/darwin-arm64/relay-agent"
build_one darwin arm64 ./cmd/relay-server "$OUT_DIR/darwin-arm64/relay-server"
show_syso
trap - EXIT

build_macos_desktop_launcher() {
  local arch="$1" out="$2"
  [[ "$(uname -s)" == "Darwin" ]] || return 1
  command -v swiftc >/dev/null 2>&1 || return 1
  command -v xcrun >/dev/null 2>&1 || return 1

  if [[ "$arch" == "universal" ]]; then
    local amd64_launcher="$OUT_DIR/darwin-amd64/RelayProxy.app/Contents/MacOS/RelayProxy"
    local arm64_launcher="$OUT_DIR/darwin-arm64/RelayProxy.app/Contents/MacOS/RelayProxy"
    local amd64_marker="$OUT_DIR/darwin-amd64/RelayProxy.app/Contents/Resources/native-desktop-shell"
    local arm64_marker="$OUT_DIR/darwin-arm64/RelayProxy.app/Contents/Resources/native-desktop-shell"
    [[ -f "$amd64_launcher" && -f "$arm64_launcher" && -f "$amd64_marker" && -f "$arm64_marker" ]] || return 1
    lipo -create "$amd64_launcher" "$arm64_launcher" -output "$out"
    return 0
  fi

  local target
  case "$arch" in
    amd64) target="x86_64-apple-macos11.0" ;;
    arm64) target="arm64-apple-macos11.0" ;;
    *) return 1 ;;
  esac
  local sdk
  sdk="$(xcrun --sdk macosx --show-sdk-path)"
  swiftc -swift-version 5 -O \
    -sdk "$sdk" -target "$target" \
    -framework AppKit -framework WebKit \
    "$ROOT/macos/RelayProxyDesktop/AppDelegate.swift" \
    "$ROOT/macos/RelayProxyDesktop/main.swift" \
    -o "$out"
}

package_macos_app() {
  local arch="$1"
  local dir="$OUT_DIR/darwin-$arch"
  local app="$dir/RelayProxy.app"
  local contents="$app/Contents"
  echo "[PACKAGE] darwin/$arch  RelayProxy.app"
  rm -rf "$app"
  mkdir -p "$contents/MacOS" "$contents/Resources"
  if build_macos_desktop_launcher "$arch" "$contents/MacOS/RelayProxy"; then
    cp "$dir/relay-agent" "$contents/Resources/relay-agent"
    chmod +x "$contents/Resources/relay-agent"
    touch "$contents/Resources/native-desktop-shell"
    echo "  Native AppKit/WKWebView desktop shell enabled"
  else
    echo "  Native desktop compiler unavailable; using browser-launching Agent executable"
    cp "$dir/relay-agent" "$contents/MacOS/RelayProxy"
  fi
  chmod +x "$contents/MacOS/RelayProxy"
  cp "$ROOT/assets/brand/app-icon-rounded.png" "$contents/Resources/AppIcon.png"
  cp "$ROOT/assets/brand/RelayProxy.icns" "$contents/Resources/RelayProxy.icns"
  cat > "$contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
  <key>CFBundleDisplayName</key><string>RelayProxy</string>
  <key>CFBundleExecutable</key><string>RelayProxy</string>
  <key>CFBundleIdentifier</key><string>com.relayproxy.agent</string>
  <key>CFBundleIconFile</key><string>RelayProxy.icns</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>RelayProxy</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppTransportSecurity</key>
  <dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
EOF
  if [[ "$(uname -s)" == "Darwin" ]] && command -v codesign >/dev/null 2>&1; then
    local identity="${MACOS_CODESIGN_IDENTITY:--}"
    local sign_args=(--force --sign "$identity")
    if [[ "$identity" != "-" ]]; then
      sign_args+=(--options runtime --timestamp)
    fi
    if [[ -f "$contents/Resources/relay-agent" ]]; then
      codesign "${sign_args[@]}" "$contents/Resources/relay-agent"
    fi
    codesign "${sign_args[@]}" "$contents/MacOS/RelayProxy"
    codesign "${sign_args[@]}" "$app"
  fi
}

package_macos_app amd64
package_macos_app arm64

build_macos_universal() {
  [[ "$(uname -s)" == "Darwin" ]] || return 0
  command -v lipo >/dev/null 2>&1 || {
    echo "[SKIP] darwin/universal  lipo is unavailable"
    return 0
  }

  local dir="$OUT_DIR/darwin-universal"
  echo "[PACKAGE] darwin/universal  universal Agent + Server + RelayProxy.app"
  mkdir -p "$dir"
  lipo -create "$OUT_DIR/darwin-amd64/relay-agent" "$OUT_DIR/darwin-arm64/relay-agent" -output "$dir/relay-agent"
  lipo -create "$OUT_DIR/darwin-amd64/relay-server" "$OUT_DIR/darwin-arm64/relay-server" -output "$dir/relay-server"
  chmod +x "$dir/relay-agent" "$dir/relay-server"
  package_macos_app universal
}

build_native_macos_app() {
  [[ "$(uname -s)" == "Darwin" ]] || return 0
  if ! command -v xcodebuild >/dev/null 2>&1; then
    echo "[SKIP] macOS NetworkExtension app: xcodebuild is unavailable"
    return 0
  fi
  if ! command -v xcodegen >/dev/null 2>&1; then
    echo "[SKIP] macOS NetworkExtension app: xcodegen is unavailable"
    return 0
  fi
  if [[ -z "${DEVELOPMENT_TEAM:-}" ]]; then
    echo "[SKIP] macOS NetworkExtension app: set DEVELOPMENT_TEAM to build the signed System Extension"
    return 0
  fi

  local project="$ROOT/macos/RelayProxyMac.xcodeproj"
  local derived="$OUT_DIR/.xcode-derived"
  local native_dir="$OUT_DIR/darwin-native"
  local generated=0

  echo "[BUILD] macOS native Host + NetworkExtension"
  if [[ ! -d "$project" ]]; then
    if ! (cd "$ROOT/macos" && xcodegen generate); then
      echo "[WARN] macOS NetworkExtension project generation failed; portable artifacts are still available"
      return 0
    fi
    generated=1
  fi

  rm -rf "$derived"
  if ! xcodebuild \
    -project "$project" \
    -scheme RelayProxyMacHost \
    -configuration Release \
    -derivedDataPath "$derived" \
    DEVELOPMENT_TEAM="$DEVELOPMENT_TEAM" \
    CODE_SIGN_STYLE=Manual \
    build; then
    echo "[WARN] macOS NetworkExtension app build/signing failed; portable artifacts are still available"
    rm -rf "$derived"
    if [[ "$generated" == "1" ]]; then rm -rf "$project"; fi
    return 0
  fi

  mkdir -p "$native_dir"
  rm -rf "$native_dir/RelayProxyMacHost.app"
  cp -R "$derived/Build/Products/Release/RelayProxyMacHost.app" "$native_dir/RelayProxyMacHost.app"

  if command -v ditto >/dev/null 2>&1; then
    ditto -c -k --sequesterRsrc --keepParent \
      "$native_dir/RelayProxyMacHost.app" \
      "$OUT_DIR/RelayProxy-macos-native.zip"
  fi

  rm -rf "$derived"
  if [[ "$generated" == "1" ]]; then rm -rf "$project"; fi
}

build_macos_universal
build_native_macos_app

# Windows client + server (Wails v3 + React assets embedded)
# Build/test Vite before compiling the Windows Agent so go:embed always contains
# the current React UI rather than a stale bundle or the legacy fallback page.
build_react_frontend

echo ""
echo "[TEST] Windows desktop packages (cross-compile compile-check)"
# A Unix/macOS host cannot execute the generated Windows test binaries.
# -exec=true still compiles/links the Windows tests, then delegates execution
# to the host true command so Windows-only compile errors are caught here.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec=true \
  ./agent/divert ./agent/gui ./cmd/relay-agent -run '^$' -count=1

build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent-gui.exe" "-H=windowsgui"
build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent.exe"
build_one windows amd64 ./cmd/relay-server "$OUT_DIR/windows-amd64/relay-server.exe"
# WinDivert is x64-only; ARM64 binaries still support the non-divert modes.
build_one windows arm64 ./cmd/relay-agent "$OUT_DIR/windows-arm64/relay-agent-gui.exe" "-H=windowsgui"
build_one windows arm64 ./cmd/relay-agent "$OUT_DIR/windows-arm64/relay-agent.exe"
build_one windows arm64 ./cmd/relay-server "$OUT_DIR/windows-arm64/relay-server.exe"

# Configs + brand files
TARGETS=(linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64)
[[ -d "$OUT_DIR/darwin-universal" ]] && TARGETS+=(darwin-universal)

for t in "${TARGETS[@]}"; do
  mkdir -p "$OUT_DIR/$t/configs" "$OUT_DIR/$t/brand"
  cp "$ROOT/configs/relay-server.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/configs/relay-agent.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/assets/brand/logo.png" "$OUT_DIR/$t/brand/"
done
cp "$ROOT/assets/brand/icon.ico" "$OUT_DIR/windows-amd64/brand/"
cp "$ROOT/assets/brand/icon.ico" "$OUT_DIR/windows-arm64/brand/"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-arm64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-arm64/brand/icon.png"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-universal/brand/icon.png"
fi
cp "$ROOT/docs/windows-transparent-proxy.md" "$OUT_DIR/windows-amd64/README.md"
cp "$ROOT/docs/windows-transparent-proxy.md" "$OUT_DIR/windows-arm64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-amd64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-arm64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-amd64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-arm64/README.md"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-universal/README.md"
fi
go run ./scripts/fetch-windivert.go \
  -out "$OUT_DIR/windows-amd64/windivert" \
  -agent-zip "$OUT_DIR/RelayProxy-agent-windows-amd64.zip"

package_release_archives() {
  local t archive
  for t in "${TARGETS[@]}"; do
    archive="$OUT_DIR/RelayProxy-${t}.tar.gz"
    echo "[PACKAGE] ${t} -> ${archive}"
    tar -C "$OUT_DIR" -czf "$archive" "$t"
  done
  if [[ -d "$OUT_DIR/darwin-native" ]]; then
    tar -C "$OUT_DIR" -czf "$OUT_DIR/RelayProxy-darwin-native.tar.gz" darwin-native
  fi

  if command -v zip >/dev/null 2>&1; then
    (
      cd "$OUT_DIR"
      zip -qry "RelayProxy-windows-amd64.zip" windows-amd64
      zip -qry "RelayProxy-windows-arm64.zip" windows-arm64
    )
  else
    echo "[SKIP] Windows full ZIP archives: zip command is unavailable; tar.gz archives were created"
  fi
}

package_release_archives

# Checksums
echo ""
echo "[SHA256]"
(
  cd "$OUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-*.zip' -o -name 'RelayProxy-*.tar.gz' \) \
      | sort | while read -r f; do sha256sum "$f"; done | tee SHA256SUMS.txt
  else
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-*.zip' -o -name 'RelayProxy-*.tar.gz' \) \
      | sort | while read -r f; do shasum -a 256 "$f"; done | tee SHA256SUMS.txt
  fi
)

echo ""
echo "=================================================="
echo " Build complete -> ${OUT_DIR}"
echo "=================================================="


echo ""
echo "Buildable artifacts:"
echo "  linux-amd64/   relay-agent + relay-server"
echo "  linux-arm64/   relay-agent + relay-server"
echo "  darwin-amd64/  relay-agent + relay-server + RelayProxy.app"
echo "  darwin-arm64/  relay-agent + relay-server + RelayProxy.app"
echo "  windows-amd64/ relay-agent-gui.exe + relay-agent.exe + relay-server.exe + WinDivert"
echo "  windows-arm64/ relay-agent-gui.exe + relay-agent.exe + relay-server.exe"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  echo "  darwin-universal/ relay-agent + relay-server + RelayProxy.app"
fi
if [[ -d "$OUT_DIR/darwin-native" ]]; then
  echo "  darwin-native/ RelayProxyMacHost.app + embedded NetworkExtension"
fi
echo "  RelayProxy-<target>.tar.gz release archives"
if command -v zip >/dev/null 2>&1; then
  echo "  RelayProxy-windows-{amd64,arm64}.zip full Windows release archives"
fi
echo "  RelayProxy-agent-windows-amd64.zip Agent-only Windows x64 package"
echo "  SHA256SUMS.txt"
 -count=1

build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent-gui.exe" "-H=windowsgui"
build_one windows amd64 ./cmd/relay-agent "$OUT_DIR/windows-amd64/relay-agent.exe"
build_one windows amd64 ./cmd/relay-server "$OUT_DIR/windows-amd64/relay-server.exe"
# WinDivert is x64-only; ARM64 binaries still support the non-divert modes.
build_one windows arm64 ./cmd/relay-agent "$OUT_DIR/windows-arm64/relay-agent-gui.exe" "-H=windowsgui"
build_one windows arm64 ./cmd/relay-agent "$OUT_DIR/windows-arm64/relay-agent.exe"
build_one windows arm64 ./cmd/relay-server "$OUT_DIR/windows-arm64/relay-server.exe"

# Configs + brand files
TARGETS=(linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64)
[[ -d "$OUT_DIR/darwin-universal" ]] && TARGETS+=(darwin-universal)

for t in "${TARGETS[@]}"; do
  mkdir -p "$OUT_DIR/$t/configs" "$OUT_DIR/$t/brand"
  cp "$ROOT/configs/relay-server.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/configs/relay-agent.yaml" "$OUT_DIR/$t/configs/"
  cp "$ROOT/assets/brand/logo.png" "$OUT_DIR/$t/brand/"
done
cp "$ROOT/assets/brand/icon.ico" "$OUT_DIR/windows-amd64/brand/"
cp "$ROOT/assets/brand/icon.ico" "$OUT_DIR/windows-arm64/brand/"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/linux-arm64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-amd64/brand/icon.png"
cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-arm64/brand/icon.png"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  cp "$ROOT/assets/brand/icon-256.png" "$OUT_DIR/darwin-universal/brand/icon.png"
fi
cp "$ROOT/docs/windows-transparent-proxy.md" "$OUT_DIR/windows-amd64/README.md"
cp "$ROOT/docs/windows-transparent-proxy.md" "$OUT_DIR/windows-arm64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-amd64/README.md"
cp "$ROOT/docs/linux-transparent-proxy.md" "$OUT_DIR/linux-arm64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-amd64/README.md"
cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-arm64/README.md"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  cp "$ROOT/agent/divert/macos/README.md" "$OUT_DIR/darwin-universal/README.md"
fi
go run ./scripts/fetch-windivert.go \
  -out "$OUT_DIR/windows-amd64/windivert" \
  -agent-zip "$OUT_DIR/RelayProxy-agent-windows-amd64.zip"

package_release_archives() {
  local t archive
  for t in "${TARGETS[@]}"; do
    archive="$OUT_DIR/RelayProxy-${t}.tar.gz"
    echo "[PACKAGE] ${t} -> ${archive}"
    tar -C "$OUT_DIR" -czf "$archive" "$t"
  done
  if [[ -d "$OUT_DIR/darwin-native" ]]; then
    tar -C "$OUT_DIR" -czf "$OUT_DIR/RelayProxy-darwin-native.tar.gz" darwin-native
  fi

  if command -v zip >/dev/null 2>&1; then
    (
      cd "$OUT_DIR"
      zip -qry "RelayProxy-windows-amd64.zip" windows-amd64
      zip -qry "RelayProxy-windows-arm64.zip" windows-arm64
    )
  else
    echo "[SKIP] Windows full ZIP archives: zip command is unavailable; tar.gz archives were created"
  fi
}

package_release_archives

# Checksums
echo ""
echo "[SHA256]"
(
  cd "$OUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-*.zip' -o -name 'RelayProxy-*.tar.gz' \) \
      | sort | while read -r f; do sha256sum "$f"; done | tee SHA256SUMS.txt
  else
    find . -type f \( -name 'relay-server' -o -name 'relay-server.exe' -o -name 'relay-agent' -o -name 'relay-agent*.exe' -o -name 'WinDivert*.dll' -o -name 'WinDivert*.sys' -o -name 'RelayProxy-*.zip' -o -name 'RelayProxy-*.tar.gz' \) \
      | sort | while read -r f; do shasum -a 256 "$f"; done | tee SHA256SUMS.txt
  fi
)

echo ""
echo "=================================================="
echo " Build complete -> ${OUT_DIR}"
echo "=================================================="


echo ""
echo "Buildable artifacts:"
echo "  linux-amd64/   relay-agent + relay-server"
echo "  linux-arm64/   relay-agent + relay-server"
echo "  darwin-amd64/  relay-agent + relay-server + RelayProxy.app"
echo "  darwin-arm64/  relay-agent + relay-server + RelayProxy.app"
echo "  windows-amd64/ relay-agent-gui.exe + relay-agent.exe + relay-server.exe + WinDivert"
echo "  windows-arm64/ relay-agent-gui.exe + relay-agent.exe + relay-server.exe"
if [[ -d "$OUT_DIR/darwin-universal" ]]; then
  echo "  darwin-universal/ relay-agent + relay-server + RelayProxy.app"
fi
if [[ -d "$OUT_DIR/darwin-native" ]]; then
  echo "  darwin-native/ RelayProxyMacHost.app + embedded NetworkExtension"
fi
echo "  RelayProxy-<target>.tar.gz release archives"
if command -v zip >/dev/null 2>&1; then
  echo "  RelayProxy-windows-{amd64,arm64}.zip full Windows release archives"
fi
echo "  RelayProxy-agent-windows-amd64.zip Agent-only Windows x64 package"
echo "  SHA256SUMS.txt"
