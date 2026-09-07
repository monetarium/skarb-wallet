# Skarb Wallet for macOS

Download: [Last version](last-version.dmg)

Universal binary (Apple Silicon + Intel). `last-version.dmg` is always the current build.

## Install

1. Open `last-version.dmg`.
2. Drag **Skarb Wallet** into `/Applications`.
3. First launch: right-click → **Open**. Confirm Open in the dialog.

The app is not signed with an Apple Developer ID. Gatekeeper may warn on first open.

If macOS says the app is damaged:

```bash
xattr -cr "/Applications/Skarb Wallet.app"
```

That removes the quarantine flag added on download.
