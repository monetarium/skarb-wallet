#!/usr/bin/env bash
# Build a macOS .app bundle for Monetarium Wallet.
#
# Output: ./Monetarium.app — drag into /Applications and double-click to run.
# The bundle is unsigned (no Gatekeeper "Open" prompt: you'll have to right-click
# → Open the first time, or run `xattr -d com.apple.quarantine Monetarium.app`).
#
# Usage:   ./build-macos-app.sh
set -euo pipefail

cd "$(dirname "$0")"

APP_NAME="Monetarium"
BUNDLE_ID="io.monetarium.wallet"
APP_DIR="${APP_NAME}.app"
VERSION_LONG=$(go run ./cmd/version 2>/dev/null || echo "0.1.0")

echo "→ Cleaning previous bundle"
rm -rf "${APP_DIR}"

echo "→ Building Go binary"
mkdir -p "${APP_DIR}/Contents/MacOS"
GOFLAGS="-mod=mod" go build -trimpath -o "${APP_DIR}/Contents/MacOS/${APP_NAME}" .

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

echo "→ Resources"
mkdir -p "${APP_DIR}/Contents/Resources"
if [ -f appicon.png ]; then
  cp appicon.png "${APP_DIR}/Contents/Resources/AppIcon.png"
  # Note: macOS prefers .icns; convert with `iconutil` if you make a real icon set.
fi

echo "→ Done: $(pwd)/${APP_DIR}"
echo "   Drag into /Applications, then right-click → Open the first time"
echo "   (the bundle is unsigned — Gatekeeper will refuse a double-click otherwise)."
