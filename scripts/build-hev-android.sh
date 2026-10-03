#!/usr/bin/env bash
set -euo pipefail

VERSION="2.18.0"
SHA256="93b3b33127436b4eab669798f1d50e019008585a27880df8cbdeffe5e70cb665"
ARCHIVE="hev-socks5-tunnel-${VERSION}.tar.xz"
URL="https://github.com/heiher/hev-socks5-tunnel/releases/download/${VERSION}/${ARCHIVE}"
API="android-26"
ABIS="arm64-v8a armeabi-v7a"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
NDK_ROOT="${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}"
if [[ -z "$NDK_ROOT" && -n "$SDK_ROOT" ]]; then
  NDK_ROOT="$SDK_ROOT/ndk/27.2.12479018"
fi
[[ -x "$NDK_ROOT/ndk-build" ]] || {
  echo "Android NDK 27.2.12479018 not found; set ANDROID_NDK_HOME" >&2
  exit 1
}

OUTPUT_DIR="${HEV_ANDROID_LIBS_OUT:-$ROOT/android/app/build/generated/hev-jniLibs}"
CACHE_ROOT="${XDG_CACHE_HOME:-${TMPDIR:-/tmp}}/relayproxy-hev/${VERSION}"
WORK_DIR="$CACHE_ROOT/build"
SOURCE_DIR="$WORK_DIR/hev-socks5-tunnel-${VERSION}"
ARCHIVE_PATH="$CACHE_ROOT/$ARCHIVE"
mkdir -p "$CACHE_ROOT"

if [[ ! -f "$ARCHIVE_PATH" ]] || [[ "$(shasum -a 256 "$ARCHIVE_PATH" | awk '{print $1}')" != "$SHA256" ]]; then
  rm -f "$ARCHIVE_PATH"
  curl -fL "$URL" -o "$ARCHIVE_PATH"
fi
actual_sha="$(shasum -a 256 "$ARCHIVE_PATH" | awk '{print $1}')"
[[ "$actual_sha" == "$SHA256" ]] || {
  echo "Unexpected hev-socks5-tunnel archive checksum: $actual_sha" >&2
  exit 1
}

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"
tar -xJf "$ARCHIVE_PATH" -C "$WORK_DIR"
[[ -f "$SOURCE_DIR/Android.mk" ]] || {
  echo "hev-socks5-tunnel source archive has an unexpected layout" >&2
  exit 1
}
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

"$NDK_ROOT/ndk-build" \
  NDK_PROJECT_PATH=null \
  "APP_BUILD_SCRIPT=$SOURCE_DIR/Android.mk" \
  "NDK_APPLICATION_MK=$SOURCE_DIR/Application.mk" \
  "APP_ABI=$ABIS" \
  "APP_PLATFORM=$API" \
  APP_SUPPORT_FLEXIBLE_PAGE_SIZES=true \
  "NDK_LIBS_OUT=$OUTPUT_DIR" \
  "NDK_OUT=$WORK_DIR/obj"

for abi in arm64-v8a armeabi-v7a; do
  library="$OUTPUT_DIR/$abi/libhev-socks5-tunnel.so"
  [[ -s "$library" ]] || { echo "Missing native library: $library" >&2; exit 1; }
done

case "$(uname -s)" in
  Darwin) host_tag="darwin-x86_64" ;;
  Linux) host_tag="linux-x86_64" ;;
  *) echo "Unsupported NDK host: $(uname -s)" >&2; exit 1 ;;
esac
READELF="$NDK_ROOT/toolchains/llvm/prebuilt/$host_tag/bin/llvm-readelf"
[[ -x "$READELF" ]] || { echo "llvm-readelf not found: $READELF" >&2; exit 1; }
for abi in arm64-v8a armeabi-v7a; do
  library="$OUTPUT_DIR/$abi/libhev-socks5-tunnel.so"
  if ! "$READELF" -l "$library" | awk '$1 == "LOAD" { n++; if ($NF != "0x4000") bad = 1 } END { exit (n == 0 || bad) }'; then
    echo "$library does not have 16 KB ELF LOAD alignment" >&2
    exit 1
  fi
done

echo "Built hev-socks5-tunnel $VERSION for $ABIS (API $API)"
