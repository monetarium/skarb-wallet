#!/usr/bin/env bash
# Build the Monetarium Wallet Windows .exe.
#
# RUN THIS ON WINDOWS (in MSYS2/Git-Bash) or on Linux with mingw-w64 installed.
# Cross-compiling Gio from macOS is brittle; on Linux the recipe below works.
#
# Prerequisites on a Linux build host:
#   sudo apt install -y golang-go gcc-mingw-w64-x86-64
#
# On Windows (MSYS2):
#   pacman -S mingw-w64-x86_64-gcc mingw-w64-x86_64-go
#
# Output:
#   ./dist/windows-amd64/monetarium.exe
#
# Install:
#   Just double-click monetarium.exe. SmartScreen may flag the file as
#   "unrecognized" the first time — click 'More info' → 'Run anyway'.
#   For a real installer you'll want NSIS or wix; out of scope here.
set -euo pipefail

cd "$(dirname "$0")"

OUT_DIR="dist/windows-amd64"
APP_NAME="monetarium"

echo "→ Cleaning ${OUT_DIR}"
rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}"

echo "→ Building Go binary"
# CGO is required by Gio. On Linux cross-build set CC explicitly.
if [ "$(uname -s)" = "Linux" ]; then
  export CC=x86_64-w64-mingw32-gcc
  export CGO_ENABLED=1
fi

GOFLAGS="-mod=mod -trimpath" GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -buildid= -H=windowsgui" -buildvcs=false \
        -o "${OUT_DIR}/${APP_NAME}.exe" .

echo "→ Done: $(pwd)/${OUT_DIR}/${APP_NAME}.exe"
ls -la "${OUT_DIR}"
echo
echo "Note: '-H=windowsgui' suppresses the console window. Drop that flag if"
echo "you want stdout/stderr visible for debugging."
