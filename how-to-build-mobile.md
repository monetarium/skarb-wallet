# Building Skarb Wallet for mobile

Desktop (macOS / Windows / Linux) is the main product. Mobile uses the
same Gio UI via `gogio` (Android) or `build-ios.sh` (iPhone).

## 1. Android APK

Prerequisites:

1. `go install gioui.org/cmd/gogio@v0.7.0` (match `gioui.org v0.7.0`)
2. Android SDK + NDK (see comments in `build-android-apk.sh`)
3. JDK 17 (`brew install openjdk@17`)

```bash
./build-android-apk.sh
```

Install:

```bash
~/Library/Android/sdk/platform-tools/adb install -r Skarb-0.1.0.apk
```

## 2. iPhone / iOS Simulator

Prerequisites:

1. Xcode with the iOS SDK
2. To *run* in the simulator, an iOS runtime:
   `xcodebuild -downloadPlatform iOS`

Stock `gogio -target ios -o x.app` only builds an x86_64 simulator
slice. This Mac is Apple Silicon, so we compile an arm64 simulator
binary ourselves:

```bash
./build-ios.sh              # Skarb-<version>.app  (simulator, arm64)
./build-ios.sh device       # Skarb-<version>-iphone.app
```

Install on a booted simulator:

```bash
xcrun simctl install booted ./Skarb-0.1.0.app
xcrun simctl launch booted com.monetarium.skarb
```

A real iPhone needs a signing identity (free Apple ID in Xcode →
Settings → Accounts). The device .app from this script is ad-hoc signed
and will not launch on hardware until you re-sign it with that team.
