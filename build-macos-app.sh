#!/usr/bin/env bash
# Build a macOS .app bundle for Skarb (Monetarium chain wallet).
# Universal: arm64 + amd64. Wrapped into a DMG for distribution.
#
# Outputs:
#   ./Skarb Wallet.app  — drop into /Applications and double-click
#   ./Skarb Wallet.dmg  — disk image to send/host (preserves bundle attrs)
#
# RECIPIENT INSTRUCTIONS (after they download "Skarb Wallet.dmg"):
#   1. Double-click the .dmg → Finder window opens.
#   2. Drag "Skarb Wallet.app" into the /Applications shortcut shown.
#   3. First launch: macOS may say "cannot be opened because the developer
#      cannot be verified." Right-click "Skarb Wallet.app" → Open → click
#      "Open" in the dialog. From then on it launches normally.
#
#   If macOS says "Skarb Wallet.app is damaged and can't be opened" then run:
#       xattr -cr "/Applications/Skarb Wallet.app"
#   That removes the com.apple.quarantine attribute the browser added.
#
# (The bundle is unsigned by an Apple Developer ID — that costs $99/year and
#  needs a real legal entity. Right-click → Open and the xattr fix are the
#  unsigned-distribution workarounds.)
set -euo pipefail

cd "$(dirname "$0")"

# EXEC_NAME — binary inside Contents/MacOS. Kept space-free for shell
# ergonomics (people will type its name in Activity Monitor, `ps`,
# `lsof` etc.); CFBundleExecutable references it directly.
# DISPLAY_NAME — what macOS shows in the Dock, menu bar, About dialog,
# Finder Get-Info, Cmd-Tab AND the bundle directory itself ("Skarb
# Wallet.app" in /Applications, "Skarb Wallet.dmg" for the disk image).
# Having a space in the bundle path means quoting it when scripting
# (cp -R "Skarb Wallet.app" /Applications/), which is a small price
# for the user-facing name being consistent everywhere.
EXEC_NAME="Skarb"
DISPLAY_NAME="Skarb Wallet"
BUNDLE_ID="io.monetarium.skarb"
APP_DIR="${DISPLAY_NAME}.app"
DMG_FILE="${DISPLAY_NAME}.dmg"
VERSION_LONG="0.1.0"

echo "→ Cleaning previous bundle"
rm -rf "${APP_DIR}" "${DMG_FILE}"

echo "→ Building universal Go binary (arm64 + amd64 → lipo)"
mkdir -p "${APP_DIR}/Contents/MacOS"
TMP_BIN_ARM=$(mktemp)
TMP_BIN_AMD=$(mktemp)
trap "rm -f $TMP_BIN_ARM $TMP_BIN_AMD" EXIT

# arm64 (Apple Silicon native — uses host CGO toolchain).
GOFLAGS="-mod=mod -trimpath" GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags "-s -w -buildid=" -buildvcs=false -o "$TMP_BIN_ARM" .

# amd64 cross from Apple Silicon: Gio needs CGO (OpenGL, AppKit) so we point
# clang at the x86_64 macOS SDK target. Requires Xcode command line tools.
SDKROOT=$(xcrun --sdk macosx --show-sdk-path) \
CGO_ENABLED=1 \
CGO_CFLAGS="-target x86_64-apple-macos11" \
CGO_LDFLAGS="-target x86_64-apple-macos11" \
GOFLAGS="-mod=mod -trimpath" GOOS=darwin GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -buildid=" -buildvcs=false -o "$TMP_BIN_AMD" .

lipo -create "$TMP_BIN_ARM" "$TMP_BIN_AMD" -output "${APP_DIR}/Contents/MacOS/${EXEC_NAME}"
echo "  binary archs: $(lipo -archs "${APP_DIR}/Contents/MacOS/${EXEC_NAME}")"

echo "→ Generating .icns icon from appicon.png"
mkdir -p "${APP_DIR}/Contents/Resources"
if [ -f appicon.png ]; then
  ICONSET=$(mktemp -d)/${EXEC_NAME}.iconset
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
    <string>${DISPLAY_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${DISPLAY_NAME}</string>
    <key>CFBundleExecutable</key>
    <string>${EXEC_NAME}</string>
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
    <string>${EXEC_NAME} can scan QR codes to import wallet addresses.</string>
</dict>
</plist>
PLIST

echo "→ Ad-hoc signing the whole bundle (deep, force)"
# Ad-hoc sign means no Apple Developer ID. We deliberately DO NOT pass
# --options runtime: hardened runtime is meant for notarised apps; on an
# ad-hoc signed app it makes Gatekeeper more aggressive and can produce a
# bare 'cannot be opened' error after a quarantined download.
# The --deep flag re-signs every framework / nested binary so the bundle is
# internally consistent (no 'damaged' error).
codesign --remove-signature "${APP_DIR}" 2>/dev/null || true
chmod +x "${APP_DIR}/Contents/MacOS/${EXEC_NAME}"
codesign --force --deep --sign - --timestamp=none \
    --identifier "${BUNDLE_ID}" \
    "${APP_DIR}"
codesign --verify --deep --strict --verbose=2 "${APP_DIR}" 2>&1 | head -5

echo "→ Stripping any local quarantine bit"
xattr -cr "${APP_DIR}" 2>/dev/null || true

echo "→ Building ${DMG_FILE} (DMGs preserve bundle attrs through download)"
# create-dmg is nicer if installed, but plain hdiutil works everywhere.
TMP_DMG_DIR=$(mktemp -d)
cp -R "${APP_DIR}" "${TMP_DMG_DIR}/"
ln -s /Applications "${TMP_DMG_DIR}/Applications"
hdiutil create -volname "${EXEC_NAME}" \
    -srcfolder "${TMP_DMG_DIR}" \
    -ov -format UDZO \
    "${DMG_FILE}" > /dev/null
rm -rf "${TMP_DMG_DIR}"

echo
echo "✅ Done."
echo "   .app bundle: $(pwd)/${APP_DIR}"
echo "   .dmg image:  $(pwd)/${DMG_FILE}  ← send THIS to people"
echo
echo "Recipient: open the DMG, drag \"${DISPLAY_NAME}.app\" to /Applications,"
echo "then right-click → Open the first time. If macOS says 'damaged',"
echo "they run:  xattr -cr \"/Applications/${DISPLAY_NAME}.app\""
