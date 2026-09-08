# Skarb Wallet for Linux

Download: [Skarb Wallet 0.1.2](Skarb-Wallet-0.1.2-linux-amd64.tar.gz)

amd64 binary for Ubuntu 22.04+ / Debian 12+ (X11 or Wayland).

## Install

```bash
tar -xzf Skarb-Wallet-0.1.2-linux-amd64.tar.gz
cd Skarb-Wallet-0.1.2-linux-amd64
mkdir -p ~/.local/bin ~/.local/share/applications ~/.local/share/icons/hicolor/256x256/apps
cp skarb ~/.local/bin/
cp skarb.desktop ~/.local/share/applications/
cp icons/256x256/skarb.png ~/.local/share/icons/hicolor/256x256/apps/
```

`~/.local/bin` must be on `PATH`. Then launch **Skarb Wallet** from the app menu, or run `skarb`.
