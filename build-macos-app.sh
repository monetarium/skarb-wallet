#!/usr/bin/env bash
# Build a macOS .app bundle for Monetarium Wallet.
#
# Output: ./Monetarium.app — drag into /Applications and double-click to run.
# The bundle is unsigned (no Gatekeeper "Open" prompt: you'll have to right-click
# → Open the first time, or run `xattr -cr Monetarium.app`).
#
# Usage:   ./build-macos-app.sh
set -euo pipefail

cd "$(dirname "$0")"

APP_NAME="Monetarium"
BUNDLE_ID="io.monetarium.wallet"
APP_DIR="${APP_NAME}.app"
VERSION_LONG="0.1.0"

echo "→ Cleaning previous bundle"
rm -rf "${APP_DIR}"

echo "→ Building Go binary"
mkdir -p "${APP_DIR}/Contents/MacOS"
GOFLAGS="-mod=mod" go build -trimpath -o "${APP_DIR}/Contents/MacOS/${APP_NAME}" .

echo "→ Generating .icns icon from appicon.png"
mkdir -p "${APP_DIR}/Contents/Resources"
if [ -f appicon.png ]; then
  ICONSET=$(mktemp -d)/${APP_NAME}.iconset
  mkdir -p "${ICONSET}"
  for SZ in 16 32 64 128 256 512; do
    sips -z $SZ $SZ appicon.png --out "${ICONSET}/icon_${SZ}x${SZ}.png" > /dev/null
    DOUBLE=$((SZ * 2))
    sips -z $DOUBLE $DOUBLE appicon.png --out "${ICONSET}/icon_${SZ}x${SZ}@2x.png" > /dev/null
  done
  sips -z 1024 1024 appicon.png --out "${ICONSET}/icon_512x512@2x.png" > /dev/null
  iconutil -c icns -o "${APP_DIR}/Contents/Resources/AppIcon.icns" "${ICONSET}"
  cp appicon.png "${APP_DIR}/Contents/Resources/AppIcon.png"
fi

echo "→ Writing Info.plist"
cat > "${APP_DIR}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${APP_NAME} Wallet</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION_LONG}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION_LONG}</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleSupportedPlatforms</key>
    <array><string>MacOSX</string></array>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSAppleScriptEnabled</key>
    <false/>
    <key>NSCameraUsageDescription</key>
    <string>${APP_NAME} can scan QR codes to import wallet addresses.</string>
</dict>
</plist>
PLIST

# Strip any quarantine bit from the freshly built bundle (locally built apps
# don't have it but it's a cheap insurance vs. user copy-paste workflows).
xattr -cr "${APP_DIR}" 2>/dev/null || true

echo "→ Done: $(pwd)/${APP_DIR}"
echo "   Drag into /Applications and double-click."
echo "   First run on a different machine: Gatekeeper may block — right-click → Open,"
echo "   or 'xattr -cr /Applications/${APP_DIR}'."
