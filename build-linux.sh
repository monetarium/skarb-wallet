#!/usr/bin/env bash
# Build the Monetarium Wallet Linux binary + a runnable .desktop entry.
#
# RUN THIS ON LINUX. Cross-compiling Gio (the GUI framework) from macOS is
# fragile because of CGO + X11/Wayland headers; the easiest path is a Linux
# host or a docker run with the matching dev libs.
#
# Tested target: Ubuntu 22.04+ / Debian 12+ / any X11 or Wayland desktop.
#
# System packages required on the build host:
#   sudo apt install -y golang-go libwayland-dev libx11-dev libx11-xcb-dev \
#       libxkbcommon-dev libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev \
#       libffi-dev libxcursor-dev libvulkan-dev
#
# Output:
#   ./dist/linux-amd64/skarb
#   ./dist/linux-amd64/skarb.desktop
#   ./dist/linux-amd64/icons/256x256/skarb.png
#   ./releases/linux/Skarb-Wallet-<version>-linux-amd64.tar.gz
#
# Install: see releases/linux/README.md.
set -euo pipefail

cd "$(dirname "$0")"

OUT_DIR="dist/linux-amd64"
APP_NAME="skarb"
DISPLAY_NAME="Skarb Wallet"
BUNDLE_ID="io.monetarium.skarb"
VERSION="$(sed -n 's/^\tVersion = "\(.*\)"/\1/p' main.go | head -1)"
: "${VERSION:=0.0.0}"
RELEASE_DIR="releases/linux"
TARBALL="Skarb-Wallet-${VERSION}-linux-amd64.tar.gz"

echo "→ Cleaning ${OUT_DIR}"
rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}/icons/256x256"

echo "→ Building Go binary (stripped, paths trimmed)"
GOFLAGS="-mod=mod -trimpath" GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -buildid= -X main.Version=${VERSION}" -buildvcs=false \
        -o "${OUT_DIR}/${APP_NAME}" .

echo "→ Copying icon"
if [ -f appicon.png ]; then
  cp appicon.png "${OUT_DIR}/icons/256x256/${APP_NAME}.png"
fi

echo "→ Writing .desktop entry"
cat > "${OUT_DIR}/${APP_NAME}.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=${DISPLAY_NAME}
Comment=Monetarium multi-coin (VAR + SKA) wallet
Exec=${APP_NAME}
Icon=${APP_NAME}
Categories=Office;Finance;
Terminal=false
StartupWMClass=${APP_NAME}
DESKTOP

echo "→ Packing ${RELEASE_DIR}/${TARBALL}"
mkdir -p "${RELEASE_DIR}"
STAGE=$(mktemp -d)
BUNDLE="Skarb-Wallet-${VERSION}-linux-amd64"
mkdir -p "${STAGE}/${BUNDLE}/icons/256x256"
cp "${OUT_DIR}/${APP_NAME}" "${STAGE}/${BUNDLE}/"
cp "${OUT_DIR}/${APP_NAME}.desktop" "${STAGE}/${BUNDLE}/"
cp "${OUT_DIR}/icons/256x256/${APP_NAME}.png" "${STAGE}/${BUNDLE}/icons/256x256/"
chmod +x "${STAGE}/${BUNDLE}/${APP_NAME}"
tar -C "${STAGE}" -czf "${RELEASE_DIR}/${TARBALL}" "${BUNDLE}"
rm -rf "${STAGE}"

echo "→ Done. Artifacts in $(pwd)/${OUT_DIR}/"
echo "   download: $(pwd)/${RELEASE_DIR}/${TARBALL}"
ls -la "${OUT_DIR}"
