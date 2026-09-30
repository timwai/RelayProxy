#!/usr/bin/env bash
set -euo pipefail

CONFIGURATION="Release"
OUT_DIR=""
CLEAN=0
SKIP_TESTS=0
SKIP_SDK_INSTALL=0
SKIP_TOOL_INSTALL=0
BUILD_ARM64=1
BUILD_ARM32=1

SDK_PLATFORM="android-35"
BUILD_TOOLS_VERSION="35.0.0"
NDK_VERSION="27.2.12479018"
GRADLE_VERSION="8.9"
ANDROID_API="26"
COMMANDLINE_TOOLS_ZIP_URL="https://dl.google.com/android/repository/commandlinetools-mac-14742923_latest.zip"
DEFAULT_SDK_ROOT="${HOME}/Library/Android/sdk"

usage() {
  cat <<'USAGE'
RelayProxy Android ARM32 + ARM64 APK build script for Apple Silicon macOS.

Usage:
  ./scripts/build-android-macos.sh [options]

Legacy entrypoint:
  ./scripts/build-android-macos-arm64.sh [options]

Options:
  --debug                  Build Debug APK
  --release                Build Release APK (default)
  --clean                  Remove previous Android outputs first
  --skip-tests             Skip Go test preflight
  --skip-sdk-install       Do not run sdkmanager for pinned packages
  --skip-tool-install      Do not auto-install gomobile/gobind
  --arm64-only             Build only arm64-v8a
  --arm32-only             Build only armeabi-v7a
  --out-dir PATH           Final APK directory (default: dist/android)
  -h, --help               Show this help

Default:
  Build two APKs: arm64-v8a and armeabi-v7a.

Release signing environment variables:
  RELAY_ANDROID_KEYSTORE
  RELAY_ANDROID_KEY_ALIAS
  RELAY_ANDROID_KEYSTORE_PASSWORD
  RELAY_ANDROID_KEY_PASSWORD

If those are unset, the script signs with a local Android debug keystore so the APK can be sideloaded.
USAGE
}

while (($#)); do
  case "$1" in
    --debug) CONFIGURATION="Debug" ;;
    --release) CONFIGURATION="Release" ;;
    --clean) CLEAN=1 ;;
    --skip-tests) SKIP_TESTS=1 ;;
    --skip-sdk-install) SKIP_SDK_INSTALL=1 ;;
    --skip-tool-install) SKIP_TOOL_INSTALL=1 ;;
    --arm64-only) BUILD_ARM64=1; BUILD_ARM32=0 ;;
    --arm32-only) BUILD_ARM64=0; BUILD_ARM32=1 ;;
    --out-dir)
      shift
      [[ $# -gt 0 ]] || { echo "--out-dir requires a path" >&2; exit 2; }
      OUT_DIR="$1"
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ANDROID_DIR="$ROOT/android"
APP_DIR="$ANDROID_DIR/app"
AAR_PATH="$APP_DIR/libs/mobilecore.aar"
OUT_DIR="${OUT_DIR:-$ROOT/dist/android}"

step() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok() { printf '\033[1;32m%s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 not found in PATH"
}

ensure_go_bin_on_path() {
  local gobin gopath
  gobin="$(go env GOBIN 2>/dev/null || true)"
  gopath="$(go env GOPATH 2>/dev/null || true)"
  if [[ -n "$gobin" ]]; then
    export PATH="$gobin:$PATH"
  fi
  if [[ -n "$gopath" ]]; then
    export PATH="${gopath%%:*}/bin:$PATH"
  fi
}

resolve_android_sdk() {
  local candidates=(
    "${ANDROID_SDK_ROOT:-}"
    "${ANDROID_HOME:-}"
    "$DEFAULT_SDK_ROOT"
    "/opt/homebrew/share/android-commandlinetools"
    "/usr/local/share/android-commandlinetools"
  )
  local candidate
  for candidate in "${candidates[@]}"; do
    if [[ -n "$candidate" && -d "$candidate" ]]; then
      (cd "$candidate" && pwd)
      return 0
    fi
  done
  return 1
}

bootstrap_android_sdk() {
  local sdk="$DEFAULT_SDK_ROOT"
  local cache_root="$HOME/Library/Caches/RelayProxy/tools"
  local zip="$cache_root/commandlinetools-mac.zip"
  local tmp
  need_cmd curl
  need_cmd unzip
  step "Bootstrap Android SDK command-line tools"
  mkdir -p "$sdk/cmdline-tools" "$cache_root"
  curl -fL "$COMMANDLINE_TOOLS_ZIP_URL" -o "$zip"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/relayproxy-cmdline-tools.XXXXXX")"
  unzip -q "$zip" -d "$tmp"
  rm -rf "$sdk/cmdline-tools/latest"
  mkdir -p "$sdk/cmdline-tools/latest"
  if [[ -d "$tmp/cmdline-tools/bin" ]]; then
    mv "$tmp/cmdline-tools/"* "$sdk/cmdline-tools/latest/"
  elif [[ -d "$tmp/cmdline-tools/latest/bin" ]]; then
    mv "$tmp/cmdline-tools/latest/"* "$sdk/cmdline-tools/latest/"
  else
    rm -rf "$tmp"
    fail "Unexpected Android command-line tools zip layout"
  fi
  rm -rf "$tmp" "$zip"
  [[ -x "$sdk/cmdline-tools/latest/bin/sdkmanager" ]] || fail "sdkmanager missing after command-line tools bootstrap"
  (cd "$sdk" && pwd)
}

resolve_sdkmanager() {
  local sdk="$1"
  local candidate
  for candidate in \
    "$sdk/cmdline-tools/latest/bin/sdkmanager" \
    "$sdk/cmdline-tools/bin/sdkmanager" \
    "$sdk/tools/bin/sdkmanager"; do
    [[ -x "$candidate" ]] && { echo "$candidate"; return 0; }
  done
  find "$sdk/cmdline-tools" -type f -name sdkmanager -perm -111 2>/dev/null | head -n 1
}

resolve_ndk() {
  local sdk="$1"
  if [[ -n "${ANDROID_NDK_HOME:-}" && -d "$ANDROID_NDK_HOME" ]]; then
    (cd "$ANDROID_NDK_HOME" && pwd)
    return 0
  fi
  if [[ -d "$sdk/ndk/$NDK_VERSION" ]]; then
    (cd "$sdk/ndk/$NDK_VERSION" && pwd)
    return 0
  fi
  local latest
  latest="$(find "$sdk/ndk" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort | tail -n 1 || true)"
  [[ -n "$latest" ]] && { (cd "$latest" && pwd); return 0; }
  return 1
}

resolve_java17() {
  if [[ -n "${JAVA_HOME:-}" && -x "$JAVA_HOME/bin/java" ]]; then
    return 0
  fi
  if [[ -x /usr/libexec/java_home ]]; then
    local jh
    jh="$(/usr/libexec/java_home -v 17 2>/dev/null || true)"
    if [[ -n "$jh" && -x "$jh/bin/java" ]]; then
      export JAVA_HOME="$jh"
      export PATH="$JAVA_HOME/bin:$PATH"
      return 0
    fi
  fi
  for jh in \
    /opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
    /usr/local/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home; do
    if [[ -x "$jh/bin/java" ]]; then
      export JAVA_HOME="$jh"
      export PATH="$JAVA_HOME/bin:$PATH"
      return 0
    fi
  done
  command -v java >/dev/null 2>&1
}

resolve_go_tool() {
  local name="$1"
  local install_pkg="$2"
  if command -v "$name" >/dev/null 2>&1; then
    command -v "$name"
    return 0
  fi
  local gopath candidate
  gopath="$(go env GOPATH)"
  candidate="${gopath%%:*}/bin/$name"
  if [[ -x "$candidate" ]]; then
    echo "$candidate"
    return 0
  fi
  (( SKIP_TOOL_INSTALL == 0 )) || fail "$name not found; install it or omit --skip-tool-install"
  step "Install $name"
  go install "$install_pkg"
  [[ -x "$candidate" ]] || fail "$name installation did not create $candidate"
  echo "$candidate"
}

resolve_gradle() {
  if command -v gradle >/dev/null 2>&1; then
    command -v gradle
    return 0
  fi

  local cache_root="$HOME/Library/Caches/RelayProxy/tools"
  local gradle_home="$cache_root/gradle-$GRADLE_VERSION"
  local gradle_bin="$gradle_home/bin/gradle"
  if [[ -x "$gradle_bin" ]]; then
    echo "$gradle_bin"
    return 0
  fi

  step "Download Gradle $GRADLE_VERSION"
  need_cmd curl
  need_cmd unzip
  mkdir -p "$cache_root"
  local zip="$cache_root/gradle-$GRADLE_VERSION-bin.zip"
  curl -fL "https://services.gradle.org/distributions/gradle-$GRADLE_VERSION-bin.zip" -o "$zip"
  rm -rf "$gradle_home"
  unzip -q "$zip" -d "$cache_root"
  rm -f "$zip"
  [[ -x "$gradle_bin" ]] || fail "Gradle executable not found after extraction"
  echo "$gradle_bin"
}

resolve_apksigner() {
  local sdk="$1"
  if [[ -x "$sdk/build-tools/$BUILD_TOOLS_VERSION/apksigner" ]]; then
    echo "$sdk/build-tools/$BUILD_TOOLS_VERSION/apksigner"
    return 0
  fi
  find "$sdk/build-tools" -type f -name apksigner -perm -111 2>/dev/null | sort | tail -n 1
}

has_release_signing() {
  [[ -n "${RELAY_ANDROID_KEYSTORE:-}" && \
     -n "${RELAY_ANDROID_KEY_ALIAS:-}" && \
     -n "${RELAY_ANDROID_KEYSTORE_PASSWORD:-}" && \
     -n "${RELAY_ANDROID_KEY_PASSWORD:-}" ]]
}

ensure_debug_keystore() {
  local ks="$HOME/.android/debug.keystore"
  if [[ -f "$ks" ]]; then
    echo "$ks"
    return 0
  fi
  need_cmd keytool
  mkdir -p "$HOME/.android"
  keytool -genkeypair \
    -keystore "$ks" \
    -alias androiddebugkey \
    -keyalg RSA \
    -keysize 2048 \
    -validity 10000 \
    -storepass android \
    -keypass android \
    -dname "CN=Android Debug,O=Android,C=US"
  echo "$ks"
}

sign_apk() {
  local source_apk="$1"
  local final_apk="$2"
  local apksigner
  apksigner="$(resolve_apksigner "$SDK_ROOT" || true)"
  [[ -n "$apksigner" ]] || fail "apksigner not found in Android build-tools"
  rm -f "$final_apk"
  if has_release_signing; then
    [[ -f "$RELAY_ANDROID_KEYSTORE" ]] || fail "Keystore not found: $RELAY_ANDROID_KEYSTORE"
    "$apksigner" sign \
      --ks "$RELAY_ANDROID_KEYSTORE" \
      --ks-key-alias "$RELAY_ANDROID_KEY_ALIAS" \
      --ks-pass env:RELAY_ANDROID_KEYSTORE_PASSWORD \
      --key-pass env:RELAY_ANDROID_KEY_PASSWORD \
      --out "$final_apk" \
      "$source_apk"
  else
    local debug_ks
    debug_ks="$(ensure_debug_keystore)"
    "$apksigner" sign \
      --ks "$debug_ks" \
      --ks-key-alias androiddebugkey \
      --ks-pass pass:android \
      --key-pass pass:android \
      --out "$final_apk" \
      "$source_apk"
  fi
  "$apksigner" verify --verbose "$final_apk"
}

[[ "$(uname -s)" == "Darwin" ]] || fail "This script is intended for macOS"
[[ "$(uname -m)" == "arm64" ]] || fail "This script is intended for Apple Silicon (arm64), including Mac mini M4"

need_cmd go
ensure_go_bin_on_path
resolve_java17 || fail "JDK not found. Install JDK 17 (Homebrew: brew install openjdk@17)."

if [[ -n "${ANDROID_SDK_ROOT:-}" && ! -d "$ANDROID_SDK_ROOT" ]]; then
  fail "ANDROID_SDK_ROOT is set but not a directory: $ANDROID_SDK_ROOT"
fi
if [[ -n "${ANDROID_HOME:-}" && ! -d "$ANDROID_HOME" ]]; then
  fail "ANDROID_HOME is set but not a directory: $ANDROID_HOME"
fi
SDK_ROOT="$(resolve_android_sdk || true)"
if [[ -z "$SDK_ROOT" ]]; then
  SDK_ROOT="$(bootstrap_android_sdk)"
fi
export ANDROID_SDK_ROOT="$SDK_ROOT"
export ANDROID_HOME="$SDK_ROOT"

printf '==================================================\n'
printf ' RelayProxy Android APK Build (macOS / Apple Silicon)\n'
printf ' Host:          %s/%s\n' "$(uname -s)" "$(uname -m)"
printf ' Configuration: %s\n' "$CONFIGURATION"
if (( BUILD_ARM64 == 1 && BUILD_ARM32 == 1 )); then
  printf ' Android ABIs:  arm64-v8a + armeabi-v7a\n'
elif (( BUILD_ARM64 == 1 )); then
  printf ' Android ABI:   arm64-v8a\n'
else
  printf ' Android ABI:   armeabi-v7a\n'
fi
printf ' Root:          %s\n' "$ROOT"
printf ' OutDir:        %s\n' "$OUT_DIR"
printf '==================================================\n'

MOD_BACKUP="$(mktemp -t relayproxy-go.mod.XXXXXX)"
SUM_BACKUP="$(mktemp -t relayproxy-go.sum.XXXXXX)"
HAD_GO_SUM=0
cp "$ROOT/go.mod" "$MOD_BACKUP"
if [[ -f "$ROOT/go.sum" ]]; then
  cp "$ROOT/go.sum" "$SUM_BACKUP"
  HAD_GO_SUM=1
fi

cleanup() {
  cp "$MOD_BACKUP" "$ROOT/go.mod" 2>/dev/null || true
  if (( HAD_GO_SUM == 1 )); then
    cp "$SUM_BACKUP" "$ROOT/go.sum" 2>/dev/null || true
  else
    rm -f "$ROOT/go.sum"
  fi
  rm -f "$MOD_BACKUP" "$SUM_BACKUP"
}
trap cleanup EXIT

cd "$ROOT"

step "Validate host toolchain"
go version
java -version
ok "Android SDK: $SDK_ROOT"

if (( SKIP_SDK_INSTALL == 0 )); then
  SDKMANAGER="$(resolve_sdkmanager "$SDK_ROOT" || true)"
  [[ -n "$SDKMANAGER" ]] || fail "sdkmanager not found. Install Android SDK Command-line Tools or use --skip-sdk-install."
  step "Install/verify Android SDK packages"
  "$SDKMANAGER" "platforms;$SDK_PLATFORM" "build-tools;$BUILD_TOOLS_VERSION" "ndk;$NDK_VERSION"
fi

NDK_ROOT="$(resolve_ndk "$SDK_ROOT" || true)"
[[ -n "$NDK_ROOT" ]] || fail "Android NDK not found. Install NDK $NDK_VERSION."
export ANDROID_NDK_HOME="$NDK_ROOT"
export ANDROID_NDK_ROOT="$NDK_ROOT"
ok "Android NDK: $NDK_ROOT"

GOMOBILE="$(resolve_go_tool gomobile golang.org/x/mobile/cmd/gomobile@latest)"
GOBIND="$(resolve_go_tool gobind golang.org/x/mobile/cmd/gobind@latest)"
ok "gomobile: $GOMOBILE"
ok "gobind:   $GOBIND"

step "Prepare gomobile"
go get -tool golang.org/x/mobile/cmd/gobind@latest
"$GOMOBILE" init

if (( SKIP_TESTS == 0 )); then
  step "Run Android core Go tests"
  go test ./mobile/androidcore ./agent/exit
fi

if (( CLEAN == 1 )); then
  step "Clean previous Android outputs"
  rm -rf "$APP_DIR/build"
  rm -f "$AAR_PATH"
fi

GRADLE="$(resolve_gradle)"
ok "Gradle: $GRADLE"

if [[ "$CONFIGURATION" == "Release" ]]; then
  TASK=":app:assembleRelease"
else
  TASK=":app:assembleDebug"
fi

mkdir -p "$OUT_DIR"
FINAL_APKS=()

build_one_arch() {
  local arch_name="$1"
  local android_abi="$2"
  local gomobile_target="$3"

  step "Build $arch_name gomobile AAR ($android_abi)"
  # The AAR and app output are architecture-specific. Remove the previous
  # architecture before each pass so Gradle cannot reuse stale native libs.
  rm -rf "$APP_DIR/build"
  rm -f "$AAR_PATH"
  mkdir -p "$(dirname "$AAR_PATH")"

  "$GOMOBILE" bind \
    -target="$gomobile_target" \
    -androidapi "$ANDROID_API" \
    -javapkg com.relayproxy.core \
    -o "$AAR_PATH" \
    ./mobile/androidcore

  [[ -f "$AAR_PATH" ]] || fail "gomobile did not produce $AAR_PATH for $arch_name"
  ok "AAR: $AAR_PATH"

  step "Build Android $CONFIGURATION APK for $android_abi"
  "$GRADLE" -p "$ANDROID_DIR" -PrelayAbi="$android_abi" --rerun-tasks "$TASK"

  local source_apk
  local final_apk

  if [[ "$CONFIGURATION" == "Debug" ]]; then
    source_apk="$APP_DIR/build/outputs/apk/debug/app-debug.apk"
    final_apk="$OUT_DIR/RelayProxy-Android-$arch_name-debug.apk"
    [[ -f "$source_apk" ]] || fail "Expected APK not found: $source_apk"
    cp -f "$source_apk" "$final_apk"
  else
    source_apk="$APP_DIR/build/outputs/apk/release/app-release-unsigned.apk"
    [[ -f "$source_apk" ]] || fail "Expected APK not found: $source_apk"
    final_apk="$OUT_DIR/RelayProxy-Android-$arch_name-release.apk"
    if has_release_signing; then
      step "Sign $arch_name Release APK"
    else
      step "Sign $arch_name Release APK with local debug keystore"
    fi
    sign_apk "$source_apk" "$final_apk"
  fi

  FINAL_APKS+=("$final_apk")
}

if (( BUILD_ARM64 == 1 )); then
  build_one_arch "arm64" "arm64-v8a" "android/arm64"
fi

if (( BUILD_ARM32 == 1 )); then
  build_one_arch "arm32" "armeabi-v7a" "android/arm"
fi

if [[ "$CONFIGURATION" == "Release" ]] && ! has_release_signing; then
  printf '\nWARNING: Release APKs are signed with the local Android debug keystore. Set RELAY_ANDROID_* for production signing.\n' >&2
fi

printf '\n\033[1;32m==================================================\n'
printf ' Android APK build completed\n'
for final_apk in "${FINAL_APKS[@]}"; do
  SIZE_BYTES="$(stat -f '%z' "$final_apk")"
  SIZE_MB="$(awk -v bytes="$SIZE_BYTES" 'BEGIN { printf "%.2f", bytes / 1024 / 1024 }')"
  SHA256="$(shasum -a 256 "$final_apk" | awk '{print $1}')"
  printf ' APK:    %s\n' "$final_apk"
  printf ' Size:   %s MB\n' "$SIZE_MB"
  printf ' SHA256: %s\n' "$SHA256"
  printf '--------------------------------------------------\n'
done
printf '==================================================\033[0m\n'
