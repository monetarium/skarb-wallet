# Skarb Wallet

Desktop wallet for the [Monetarium](https://github.com/monetarium) network. Built with [Gio](https://gioui.org/).

**Downloads**

* macOS: [Skarb Wallet 0.1.2](releases/macos/Skarb-Wallet-0.1.2.dmg)
* App Store: coming soon
* Google Play: coming soon
* APK: coming soon
* Linux: [Skarb Wallet 0.1.2](releases/linux/Skarb-Wallet-0.1.2-linux-amd64.tar.gz)
* Windows: coming soon

**Install on macOS**

1. Open the DMG and drag **Skarb Wallet** into Applications.
2. First launch: right-click the app → **Open** (the build is unsigned).
3. If macOS says the app is damaged:

```bash
xattr -cr "/Applications/Skarb Wallet.app"
```

More detail: [releases/macos](releases/macos/README.md).

**Install on Linux**

1. Download [Skarb Wallet 0.1.2](releases/linux/Skarb-Wallet-0.1.2-linux-amd64.tar.gz) and unpack it.
2. Copy the binary, launcher, and icon:

```bash
cd Skarb-Wallet-0.1.2-linux-amd64
mkdir -p ~/.local/bin ~/.local/share/applications ~/.local/share/icons/hicolor/256x256/apps
cp skarb ~/.local/bin/
cp skarb.desktop ~/.local/share/applications/
cp icons/256x256/skarb.png ~/.local/share/icons/hicolor/256x256/apps/
```

3. Put `~/.local/bin` on `PATH`, then run `skarb` or launch **Skarb Wallet** from the app menu.

More detail: [releases/linux](releases/linux/README.md).

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

Windows: `build-windows.sh` (packaged download coming soon).

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
