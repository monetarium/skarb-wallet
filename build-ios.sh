#!/usr/bin/env bash
# Builds the Skarb Wallet iOS .app.
#
# Analogous to build-android-apk.sh. gogio's stock iOS path only produces
# an x86_64 simulator binary or a signed device .ipa — on Apple Silicon
# we need an arm64 iphonesimulator slice, so this script drives clang
# itself the same way gogio does, then wraps the binary in a .app.
#
# Prerequisites (macOS, one-time):
#   Xcode with the iOS SDK (this Mac already has it)
#   To RUN in the simulator, also download a runtime:
#     xcodebuild -downloadPlatform iOS
#
# Usage:
#   ./build-ios.sh                  # arm64 iOS Simulator .app
#   ./build-ios.sh device           # arm64 iPhone .app (unsigned)
#   TARGET=device ./build-ios.sh
#
# Install on a booted simulator:
#   xcrun simctl install booted "./Skarb-${VERSION}.app"
#   xcrun simctl launch booted com.monetarium.skarb
set -euo pipefail
cd "$(dirname "$0")"

export PATH="$HOME/go/bin:$PATH"

TARGET="${1:-${TARGET:-simulator}}"
case "$TARGET" in
  simulator|sim) TARGET=simulator ;;
  device|iphone|ios) TARGET=device ;;
  *)
    echo "usage: $0 [simulator|device]" >&2
    exit 2
    ;;
esac

VERSION="$(sed -n 's/^\tVersion = "\(.*\)"/\1/p' main.go | head -1)"
: "${VERSION:=0.0.0}"
VERSION_CODE="${VERSION_CODE:-$(date +%Y%m%d)}"
MIN_IOS="${MIN_IOS:-15.0}"
BUNDLE_ID="${BUNDLE_ID:-com.monetarium.skarb}"
DISPLAY_NAME="Skarb Wallet"
EXEC_NAME="Skarb"

if [ "$TARGET" = simulator ]; then
  SDK_NAME=iphonesimulator
  PLATFORM=iphonesimulator
  SUPPORT_PLATFORM=iPhoneSimulator
  CLANG_TARGET="arm64-apple-ios${MIN_IOS}-simulator"
  OUT="Skarb-${VERSION}.app"
else
  SDK_NAME=iphoneos
  PLATFORM=iphoneos
  SUPPORT_PLATFORM=iPhoneOS
  CLANG_TARGET="arm64-apple-ios${MIN_IOS}"
  OUT="Skarb-${VERSION}-iphone.app"
fi

SDKROOT="$(xcrun --sdk "$SDK_NAME" --show-sdk-path)"
CLANG="$(xcrun --sdk "$SDK_NAME" --find clang)"
CLANGXX="$(xcrun --sdk "$SDK_NAME" --find clang++)"

echo "Building ${OUT} (${VERSION}.${VERSION_CODE}, ${TARGET}, ${CLANG_TARGET})…"

# -target is what distinguishes arm64-simulator from arm64-device on
# Apple Silicon. Do not pass -fmodules: it breaks cgo deps that use
# classic #include (gopsutil, blake3, …).
CFLAGS="-isysroot ${SDKROOT} -target ${CLANG_TARGET} -fobjc-arc"
if [ "$TARGET" = simulator ]; then
  CFLAGS="${CFLAGS} -mios-simulator-version-min=${MIN_IOS}"
else
  CFLAGS="${CFLAGS} -miphoneos-version-min=${MIN_IOS}"
fi

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/skarb-ios.XXXXXX")"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

BIN="$WORKDIR/skarb-arm64"

# Gio + cgo (Metal, UIKit, AVFoundation). Match gogio's env.
# -lresolv is required for Go's DNS resolver on Apple platforms.
env -u CGO_CFLAGS -u CGO_LDFLAGS -u CGO_CXXFLAGS \
  GOOS=ios GOARCH=arm64 CGO_ENABLED=1 \
  CC="$CLANG" CXX="$CLANGXX" \
  CGO_CFLAGS="$CFLAGS" \
  CGO_CXXFLAGS="$CFLAGS" \
  CGO_LDFLAGS="-lresolv $CFLAGS" \
  GOFLAGS="-mod=mod -trimpath" \
  go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid=" \
    -o "$BIN" \
    .

file "$BIN"
echo "  archs: $(lipo -archs "$BIN" 2>/dev/null || echo arm64)"

rm -rf "$OUT"
mkdir -p "$OUT"

cp "$BIN" "$OUT/${EXEC_NAME}"
chmod +x "$OUT/${EXEC_NAME}"

PLIST="$OUT/Info.plist"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>${DISPLAY_NAME}</string>
	<key>CFBundleExecutable</key>
	<string>${EXEC_NAME}</string>
	<key>CFBundleIdentifier</key>
	<string>${BUNDLE_ID}</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>${DISPLAY_NAME}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>${VERSION}</string>
	<key>CFBundleVersion</key>
	<string>${VERSION_CODE}</string>
	<key>LSRequiresIPhoneOS</key>
	<true/>
	<key>MinimumOSVersion</key>
	<string>${MIN_IOS}</string>
	<key>UIDeviceFamily</key>
	<array>
		<integer>1</integer>
		<integer>2</integer>
	</array>
	<key>UILaunchScreen</key>
	<dict/>
	<key>UIRequiredDeviceCapabilities</key>
	<array>
		<string>arm64</string>
	</array>
	<key>UISupportedInterfaceOrientations</key>
	<array>
		<string>UIInterfaceOrientationPortrait</string>
		<string>UIInterfaceOrientationLandscapeLeft</string>
		<string>UIInterfaceOrientationLandscapeRight</string>
	</array>
	<key>CFBundleSupportedPlatforms</key>
	<array>
		<string>${SUPPORT_PLATFORM}</string>
	</array>
	<key>DTPlatformName</key>
	<string>${PLATFORM}</string>
	<key>NSAppTransportSecurity</key>
	<dict>
		<key>NSAllowsArbitraryLoads</key>
		<true/>
	</dict>
	<key>NSCameraUsageDescription</key>
	<string>Skarb uses the camera to scan payment addresses.</string>
	<key>NSLocalNetworkUsageDescription</key>
	<string>Skarb looks for nearby Monetarium nodes.</string>
</dict>
</plist>
EOF

# Icons via actool, same sizes gogio uses. Failure is non-fatal: the
# app still launches with a default glyph.
if [ -f appicon.png ]; then
  ASSETS="$WORKDIR/Assets.xcassets"
  ICONSET="$ASSETS/AppIcon.appiconset"
  mkdir -p "$ICONSET"
  sips -z 120 120 appicon.png --out "$ICONSET/ios_2x.png" >/dev/null
  sips -z 180 180 appicon.png --out "$ICONSET/ios_3x.png" >/dev/null
  sips -z 76 76 appicon.png --out "$ICONSET/ipad_1x.png" >/dev/null
  sips -z 152 152 appicon.png --out "$ICONSET/ipad_2x.png" >/dev/null
  sips -z 167 167 appicon.png --out "$ICONSET/ipad_pro.png" >/dev/null
  # App Store 1024 must not contain transparency — composite on white.
  sips -z 1024 1024 appicon.png --out "$WORKDIR/icon1024.png" >/dev/null
  sips -s format png --setProperty formatOptions default \
    "$WORKDIR/icon1024.png" --out "$ICONSET/ios_store.png" >/dev/null || \
    cp "$WORKDIR/icon1024.png" "$ICONSET/ios_store.png"
  cat > "$ICONSET/Contents.json" <<'JSON'
{
  "images": [
    {"size": "60x60", "idiom": "iphone", "filename": "ios_2x.png", "scale": "2x"},
    {"size": "60x60", "idiom": "iphone", "filename": "ios_3x.png", "scale": "3x"},
    {"size": "76x76", "idiom": "ipad", "filename": "ipad_1x.png", "scale": "1x"},
    {"size": "76x76", "idiom": "ipad", "filename": "ipad_2x.png", "scale": "2x"},
    {"size": "83.5x83.5", "idiom": "ipad", "filename": "ipad_pro.png", "scale": "2x"},
    {"size": "1024x1024", "idiom": "ios-marketing", "filename": "ios_store.png", "scale": "1x"}
  ],
  "info": {"version": 1, "author": "xcode"}
}
JSON
  ASSET_PLIST="$WORKDIR/assets.plist"
  # actool --platform iphonesimulator errors out when no simulator
  # runtime is installed, even though it still writes the PNGs.
  # Compile as iphoneos so icon generation does not depend on a runtime.
  set +e
  xcrun actool \
      --compile "$OUT" \
      --platform iphoneos \
      --minimum-deployment-target "${MIN_IOS%.*}" \
      --app-icon AppIcon \
      --output-partial-info-plist "$ASSET_PLIST" \
      --notices --warnings --output-format human-readable-text \
      "$ASSETS" >"$WORKDIR/actool.out" 2>&1
  ACTOOL_RC=$?
  set -e
  if [ -f "$ASSET_PLIST" ]; then
    /usr/libexec/PlistBuddy -c "Merge $ASSET_PLIST" "$PLIST" >/dev/null || true
  fi
  if [ "$ACTOOL_RC" -ne 0 ] && [ ! -f "$OUT/AppIcon60x60@2x.png" ]; then
    echo "warning: actool failed (app will use the default icon):" >&2
    cat "$WORKDIR/actool.out" >&2 || true
  fi
fi

plutil -convert binary1 "$PLIST"

# Simulator (and most device installs) refuse an unsigned bundle.
if [ "$TARGET" = simulator ]; then
  codesign --force --sign - --timestamp=none "$OUT" >/dev/null
elif [ -n "${IOS_SIGN_IDENTITY:-}" ]; then
  codesign --force --sign "$IOS_SIGN_IDENTITY" --timestamp=none "$OUT"
else
  # Ad-hoc: lets us inspect the bundle; a real iPhone still needs a
  # development team (Apple ID in Xcode) before it will launch.
  codesign --force --sign - --timestamp=none "$OUT" >/dev/null || true
fi

echo
echo "Done: $(pwd)/${OUT}"
if [ "$TARGET" = simulator ]; then
  echo "Install on a booted simulator:"
  echo "  xcrun simctl install booted \"$(pwd)/${OUT}\""
  echo "  xcrun simctl launch booted ${BUNDLE_ID}"
  echo "If no simulator runtime is installed yet:"
  echo "  xcodebuild -downloadPlatform iOS"
else
  echo "This is an unsigned (or ad-hoc) iPhone .app. To run on a device,"
  echo "open Xcode → Settings → Accounts, add an Apple ID, then:"
  echo "  codesign --force --sign <IDENTITY> --entitlements entitlements.plist \"${OUT}\""
  echo "  xcrun devicectl device install app --device <udid> \"${OUT}\""
fi
