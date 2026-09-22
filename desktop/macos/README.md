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

## Target-machine acceptance

After `swift test`, exercise these lifecycle cases in the assembled app on macOS:

1. During the initial data check, launch the app again and repeatedly click its status item. The menu must remain disabled for conflicting actions, and only one check may run.
2. While the first-run initialization is in progress, launch the app again. The existing setup window should be focused; no second initialization or service process may start.
3. While the service is starting or stopping, launch the app again. The in-flight transition must continue without resetting the menu to “stopped” or spawning another service.
4. Quit during initialization and during service shutdown. Initialization must finish within its bounded timeout before the app exits; a running child must receive stdin EOF and be reaped before exit.
5. Occupy `127.0.0.1:8787` with another process. The launcher must show a port-conflict error and must not open or terminate the occupying process.
