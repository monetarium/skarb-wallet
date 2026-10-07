# Skarb Wallet

Desktop wallet for the [Monetarium](https://github.com/monetarium) network. Built with [Gio](https://gioui.org/).

**Downloads**

* macOS (Apple Silicon + Intel): [Skarb Wallet 0.1.4 — DMG](https://raw.githubusercontent.com/monetarium/skarb-wallet/main/releases/macos/Skarb-Wallet-0.1.4.dmg)
* Windows x64: [Skarb Wallet 0.1.4 — ZIP](https://raw.githubusercontent.com/monetarium/skarb-wallet/main/releases/windows/Skarb-Wallet-0.1.4-windows-amd64.zip)
* Linux x64: [Skarb Wallet 0.1.3 — TAR.GZ](https://raw.githubusercontent.com/monetarium/skarb-wallet/main/releases/linux/Skarb-Wallet-0.1.3-linux-amd64.tar.gz)
* App Store: coming soon
* Google Play: coming soon
* APK: coming soon

Seed restoration now keeps field focus after mouse or trackpad clicks, Clear all resets every field, and the desktop scrollbar redraws while being dragged. [SHA-256 checksums](releases/SHA256SUMS-0.1.4.txt).

**Install on macOS**

1. Open the DMG and drag **Skarb Wallet** into Applications.
2. First launch: right-click the app → **Open** (the build is unsigned).
3. If macOS says the app is damaged:

```bash
xattr -cr "/Applications/Skarb Wallet.app"
```

More detail: [releases/macos](releases/macos/README.md). Release history: [GitHub Releases](https://github.com/monetarium/skarb-wallet/releases).

**Install on Linux**

1. Download [Skarb Wallet 0.1.3](https://raw.githubusercontent.com/monetarium/skarb-wallet/main/releases/linux/Skarb-Wallet-0.1.3-linux-amd64.tar.gz) and unpack it.
2. Copy the binary, launcher, and icon:

```bash
cd Skarb-Wallet-0.1.3-linux-amd64
mkdir -p ~/.local/bin ~/.local/share/applications ~/.local/share/icons/hicolor/256x256/apps
cp skarb ~/.local/bin/
cp skarb.desktop ~/.local/share/applications/
cp icons/256x256/skarb.png ~/.local/share/icons/hicolor/256x256/apps/
```

3. Put `~/.local/bin` on `PATH`, then run `skarb` or launch **Skarb Wallet** from the app menu.

More detail: [releases/linux](releases/linux/README.md).

**Install on Windows**

1. Download [Skarb Wallet 0.1.4](https://raw.githubusercontent.com/monetarium/skarb-wallet/main/releases/windows/Skarb-Wallet-0.1.4-windows-amd64.zip) and extract the ZIP.
2. Open the extracted `Skarb-Wallet-0.1.4-windows-amd64` folder and run `skarb.exe`.

More detail: [releases/windows](releases/windows/README.md).

**Features**

- VAR and SKA on Monetarium (SPV)
- Send, receive, accounts
- Coin control
- Staking (tickets, VSP, auto-buy)
- Governance
- Mainnet and testnet

## Building

Go 1.25 or newer.

```bash
go build -o skarb .
./skarb
```

macOS `.app` + `.dmg`:

```bash
./build-macos-app.sh
```

Linux `.tar.gz`:

```bash
./build-linux.sh
```

Windows `.exe`:

```bash
./build-windows.sh
```

Android / iOS: see [how-to-build-mobile.md](how-to-build-mobile.md). Store listings are coming soon.

By default Skarb runs on mainnet. Testnet:

```bash
./skarb --network=testnet
```

`./skarb -h` lists commands and options.

## Profiling

Skarb uses [pprof](https://github.com/google/pprof). Start a profile server with `--profile` and a port:

```bash
./skarb --profile=6060
curl -O localhost:6060/debug/pprof/profile
```

## Contributing

See [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md).
