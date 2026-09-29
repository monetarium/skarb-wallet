# Skarb Wallet for macOS

Download: [Skarb Wallet 0.1.3](https://github.com/monetarium/skarb-wallet/releases/download/v0.1.3/Skarb-Wallet-0.1.3.dmg)

Universal binary (Apple Silicon + Intel).

## Install

1. Open `Skarb-Wallet-0.1.3.dmg`.
2. Drag **Skarb Wallet** into `/Applications`.
3. First launch: right-click → **Open**. Confirm Open in the dialog.

The app is not signed with an Apple Developer ID. Gatekeeper may warn on first open.

If macOS says the app is damaged:

```bash
xattr -cr "/Applications/Skarb Wallet.app"
```

That removes the quarantine flag added on download.
