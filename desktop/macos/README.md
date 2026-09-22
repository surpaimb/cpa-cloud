# CPA Cloud macOS Launcher

This directory contains the native AppKit menu-bar launcher. It is intentionally a small Swift Package with no third-party dependencies. The packaged application is an accessory app, so closing the first-run window does not stop the local service.

## Requirements

- macOS 13 or newer
- Xcode 15 or newer (Swift 5.9 toolchain)

The package can only be compiled and exercised on macOS because it imports AppKit and Darwin.

## Build and test

From the repository root on a macOS runner:

```sh
swift test --package-path desktop/macos
swift build --package-path desktop/macos -c release --product CPACloudLauncher
```

The packaging workflow may build each architecture separately:

```sh
swift build --package-path desktop/macos -c release --arch x86_64 --product CPACloudLauncher
swift build --package-path desktop/macos -c release --arch arm64 --product CPACloudLauncher
```

Use `swift build --package-path desktop/macos -c release --show-bin-path` to locate the release output. The executable product is named `CPACloudLauncher`.

## Bundle contract

The packaging task assembles this layout:

```text
CPA Cloud.app/
  Contents/
    Info.plist                         # copied from desktop/macos/Info.plist
    MacOS/CPACloudLauncher
    Resources/server/cpa-cloud         # executable Go service
    Resources/web/index.html           # built web application
```

The bundle identifier is `com.surpaimb.cpa-cloud.launcher`. The launcher stores data in `~/Library/Application Support/CPACloud/data`, binds only to `127.0.0.1:8787`, and manages only the Go child process that it starts.

The preview artifact is unsigned and unnotarized. Building this package does not imply Apple Developer membership, signing, notarization, or production distribution readiness.
