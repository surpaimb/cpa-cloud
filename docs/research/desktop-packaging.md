# Desktop launcher packaging research

This note records the packaging boundary for the unsigned `v0.1.0-preview.2`
desktop preview. It is an implementation record, not a signing or notarization
claim.

## Release artifact matrix

The release workflow keeps the six existing portable archives and adds four
desktop installers. Every primary artifact has a same-name `.sha256` sidecar,
and a tag release also publishes one aggregate `SHA256SUMS.txt`.

| Operating system | Architecture | Portable | Desktop installer |
| --- | --- | --- | --- |
| Windows | amd64 | `.zip` | per-user `_Setup.exe` |
| Windows | arm64 | `.zip` | per-user `_Setup.exe` |
| Linux | amd64 | `.tar.gz` | future work only |
| Linux | arm64 | `.tar.gz` | future work only |
| macOS | amd64 | `.tar.gz` | drag-and-drop `.dmg` |
| macOS | arm64 | `.tar.gz` | drag-and-drop `.dmg` |

The packages are built on matching native GitHub-hosted runners. A push to
`main` builds, tests, and uploads workflow artifacts using the legal preview
version `v0.1.0-preview.2`, but does not create a GitHub Release. The publish job
is additionally gated to `refs/tags/v*-preview.*`; tag builds preserve the
existing prerelease publishing behavior. Installer jobs consume the already
tested portable artifacts, so they do not duplicate the Go build matrix.

## Windows installer

The Windows launcher project is published for `win-x64` or `win-arm64` as an
untrimmed, single-file, self-contained .NET Windows Desktop executable named
`CPACloud.Launcher.exe`. The installer also carries the matching native
`cpa-cloud.exe`, `web/`, and package notices.

The installer uses [NSIS 3.12 from the official SourceForge project](https://sourceforge.net/projects/nsis/files/NSIS%203/3.12/).
The workflow downloads `nsis-3.12.zip` and requires SHA-256
`56581f90db321581c5381193d796fffcf2d24b2f8fed2160a6c6a3baa67f2c4f` before
executing `makensis.exe`. That value is also published in the NSIS project's
[canonical release-data feed](https://nsis-dev.github.io/release-data/versions.json).
The official SourceForge URL is tried first; the MIT MacPorts distfiles mirror is
an availability fallback, and cannot be executed unless it matches that same
canonical digest. NSIS uses the permissive
[zlib/libpng license](https://nsis.sourceforge.io/License); its `COPYING` file is
included in the installed notices. The script uses NSIS's built-in LZMA
compressor and no separately licensed compression plug-in.

This choice deliberately avoids a commercial-license ambiguity for automated
release builds. Inno Setup 7 is mature, but its
[current official download page](https://jrsoftware.org/isdl.php) states that
commercial users must purchase a license, so it is not used here.

Installation is current-user only (`RequestExecutionLevel user`) at
`%LOCALAPPDATA%\Programs\CPA Cloud`. It always creates Start menu launch and
uninstall shortcuts; the desktop shortcut is an optional component. amd64 and
arm64 installers reject the wrong native Windows architecture.

The launcher owns the named mutex `Local\CPACloud.Launcher`. Install, upgrade,
and uninstall first acquire a setup mutex, then stop with a bilingual message
while the launcher mutex exists. The launcher checks the setup mutex before and
after acquiring its own mutex. This fixed acquisition protocol prevents both
concurrent installer writes and a launcher start racing with replacement.

The install directory is fixed and carries an exact ownership marker; setup
refuses a non-empty unmarked directory. Upgrades overwrite only the new payload
files. The packaging script generates an exact per-build uninstall file list,
and uninstall deletes those files and then attempts only non-recursive directory
removal. Before install or uninstall mutation, every generated payload target
and each of its existing ancestors is checked for the Windows reparse-point
attribute; setup stops instead of traversing one. It never recursively traverses
the install tree, so added files are left in place. User data is outside that
tree at `%LOCALAPPDATA%\CPACloud\data`; uninstall never removes it.

Microsoft documents that a
[self-contained deployment includes the .NET runtime and libraries](https://learn.microsoft.com/dotnet/core/deploying/).
The packaging script therefore copies the .NET distribution `LICENSE.txt` and
`ThirdPartyNotices.txt`, plus the Windows Desktop SDK license and notices, into
`THIRD-PARTY-LICENSES/dotnet-self-contained/`. It also records the exact SDK and
runtime inventory from `dotnet --info` in `DESKTOP-BUILD-INFO.txt`. These notices
are derived from the SDK that produced the artifact rather than from a presumed
runtime version.

## macOS app and DMG

The macOS job builds the SwiftPM executable product `CPACloudLauncher` for the
runner's native architecture and assembles this bundle:

```text
CPA Cloud.app/
  Contents/
    Info.plist
    MacOS/CPACloudLauncher
    Resources/server/cpa-cloud
    Resources/web/
    Resources/notices/
```

The bundle identifier is `com.surpaimb.cpa-cloud.launcher`. The compressed DMG
contains the app and an `/Applications` symlink for the standard drag-and-drop
installation flow. Packaging uses only Apple/Xcode system tools (`swift`,
`plutil`, `lipo`, `otool`, `ditto`, and `hdiutil`), so there is no third-party DMG
builder license to redistribute.

The app does not copy a private Swift runtime. Swift's official
[ABI stability announcement](https://www.swift.org/blog/abi-stability-and-more/)
describes the Swift runtime shipped with Apple operating systems. Rather than
assuming the dependency set, each produced app records the launcher's actual
`otool -L` output in `SWIFT-RUNTIME-DEPENDENCIES.txt`; packaging fails if that
output points into Xcode, a toolchain directory, or the Swift build directory.
Both launcher and server architectures are checked with `lipo`, and the DMG is
verified, mounted read-only, and inspected before its checksum is written.

The preview app and DMG are neither code-signed nor notarized. No workflow step
or release note claims otherwise, and first launch may be blocked or warned by
macOS security policy. Signing, hardened runtime, notarization, stapling, and a
clean-machine Gatekeeper test are separate future release work.

## Validation boundary

The workflow validates locked web dependencies, web tests/build, native Go
tests, launcher unit tests, native architecture, package contents, license
notices, portable checksums, installer readback, and the final ten artifact
sidecars. Windows setup compilation is performed on the corresponding Windows
runner; Swift app/DMG construction is performed on the corresponding macOS
runner.

Each ephemeral GitHub-hosted Windows runner also performs a bounded silent
lifecycle test. Starting from clean product paths and registry keys, it checks
install and same-package upgrade/reinstall, Start menu creation, the default
absence of the optional desktop shortcut, launcher/setup mutex refusal exit
codes, reparse-point refusal with an out-of-tree sentinel, uninstall, exact
payload removal, non-payload-file preservation, and user-data preservation.
Refusal message boxes have explicit silent defaults so the test cannot wait for
UI input. The acceptance script refuses to run outside a GitHub-hosted Actions
runner and cleans only paths that it first proved were absent and then created
itself.

These checks do not replace installation tests on clean end-user machines.
Before promoting beyond preview, test install, upgrade, already-running refusal,
uninstall/data preservation, OS security prompts, and first-run initialization
on clean amd64 and arm64 systems. Linux `.deb` and `.rpm` packages are explicitly
future work and are not produced by this workflow.
