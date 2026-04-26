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
#   ./dist/linux-amd64/monetarium                — the binary
#   ./dist/linux-amd64/monetarium.desktop        — XDG launcher
#   ./dist/linux-amd64/icons/256x256/monetarium.png  — PNG icon
#
# Install (per-user):
#   cp ./dist/linux-amd64/monetarium ~/.local/bin/
#   cp ./dist/linux-amd64/monetarium.desktop ~/.local/share/applications/
#   mkdir -p ~/.local/share/icons/hicolor/256x256/apps
#   cp ./dist/linux-amd64/icons/256x256/monetarium.png \
#      ~/.local/share/icons/hicolor/256x256/apps/
#   update-desktop-database ~/.local/share/applications/
set -euo pipefail

cd "$(dirname "$0")"

OUT_DIR="dist/linux-amd64"
APP_NAME="monetarium"
DISPLAY_NAME="Monetarium Wallet"
BUNDLE_ID="io.monetarium.wallet"

echo "→ Cleaning ${OUT_DIR}"
rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}/icons/256x256"

echo "→ Building Go binary (stripped, paths trimmed)"
GOFLAGS="-mod=mod -trimpath" GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -buildid=" -buildvcs=false \
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
Comment=Monetarium multi-coin (VAR + SKAn) wallet
Exec=${APP_NAME}
Icon=${APP_NAME}
Categories=Office;Finance;
Terminal=false
StartupWMClass=${APP_NAME}
DESKTOP

echo "→ Done. Artifacts in $(pwd)/${OUT_DIR}/"
ls -la "${OUT_DIR}"
