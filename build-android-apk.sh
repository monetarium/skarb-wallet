#!/usr/bin/env bash
# Builds the Skarb Wallet Android APK with gogio.
#
# Prerequisites (macOS, one-time):
#   brew install openjdk                    # javac + keytool for the Android glue
#   brew install --cask android-ndk         # NDK (symlink it into the SDK, see below)
#   go install gioui.org/cmd/gogio@latest
#   Android SDK at ~/Library/Android/sdk with platforms;android-34 +
#   build-tools;34.0.0, and the NDK linked at ndk/<version>:
#     ln -sfn /opt/homebrew/share/android-ndk ~/Library/Android/sdk/ndk/<rev>
#
# gogio picks the icon up from ./appicon.png automatically.
set -euo pipefail
cd "$(dirname "$0")"

export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}"
export ANDROID_HOME="$ANDROID_SDK_ROOT"
# JDK 17 specifically: javac 26 emits class files d8 (build-tools 34) can't
# parse — the dex step dies with an internal NPE. 17 still targets 1.8,
# which is what gogio compiles the Gio activity glue for. Keg-only brew
# package — put its javac/keytool first in PATH.
export JAVA_HOME="${JAVA_HOME:-/opt/homebrew/opt/openjdk@17}"
export PATH="$JAVA_HOME/bin:$HOME/go/bin:$PATH"

VERSION="$(sed -n 's/^\tVersion = "\(.*\)"/\1/p' main.go | head -1)"
: "${VERSION:=0.0.0}"

OUT="Skarb-${VERSION}.apk"

# gogio v0.7 wants "MAJOR.MINOR.PATCH.VERSIONCODE" (4 numeric parts, no "v").
# The last part is the Android versionCode and must grow for in-place
# upgrades — a YYYYMMDD date stamp does that per daily build.
VERSION_CODE="${VERSION_CODE:-$(date +%Y%m%d)}"
echo "Building ${OUT} (${VERSION}.${VERSION_CODE})…"
# -tags novulkan: Gio v0.7's vulkan cgo bindings don't compile against NDK
# r29's headers ("cannot define new methods on non-local type…") — Gio then
# renders via OpenGL ES/EGL, its standard Android path. 64-bit only: the
# same header clash also breaks gioui.org/cpu on 32-bit arm/x86, and arm64
# covers every Android phone since ~2015 (Play mandates 64-bit anyway);
# amd64 is for the emulator.
gogio \
  -target android \
  -arch arm64,amd64 \
  -tags novulkan \
  -appid com.monetarium.skarb \
  -name "Skarb Wallet" \
  -version "${VERSION}.${VERSION_CODE}" \
  -x \
  -o "${OUT}" \
  .

echo
echo "Done: $(pwd)/${OUT}"
echo "Install on a device (USB debugging on):"
echo "  ~/Library/Android/sdk/platform-tools/adb install -r \"${OUT}\""
