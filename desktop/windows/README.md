# CPA Cloud Windows launcher

This directory contains the native WinForms tray launcher. It is intentionally a single project so the packaging job can publish one stable `CPACloud.Launcher.exe` entry point.

## Bundle layout

Place these items together in the installed application directory:

```text
CPACloud.Launcher.exe
cpa-cloud.exe
web/
```

The launcher resolves every bundle path relative to its own executable, never the current working directory or `PATH`. Persistent state is stored separately at `%LOCALAPPDATA%\CPACloud\data` with an inheritance-disabled ACL granting full control only to the current user.

## Build and checks

Use a .NET 10 SDK. The end-user publish is self-contained and does not require a system .NET installation:

```powershell
dotnet build .\desktop\windows\CPACloud.Launcher.csproj -c Release
dotnet run --project .\desktop\windows\CPACloud.Launcher.csproj -c Release -- --self-test
dotnet run --project .\desktop\windows\CPACloud.Launcher.csproj -c Release -- --integration-test .\dist\launcher-cli\cpa-cloud.exe .\web\dist
dotnet publish .\desktop\windows\CPACloud.Launcher.csproj -c Release -r win-x64 --self-contained true -p:PublishSingleFile=true -p:PublishTrimmed=false
dotnet publish .\desktop\windows\CPACloud.Launcher.csproj -c Release -r win-arm64 --self-contained true -p:PublishSingleFile=true -p:PublishTrimmed=false
```

The built-in non-UI self-test covers bundle/data path derivation, UTF-8 password boundaries, child CLI construction, exact health-instance matching, occupied-port rejection, ACLs, and the fixed mutex name. The optional integration test uses an isolated temporary data directory and the supplied real Go/web build to exercise initialization, identity-bound readiness, and stdin-EOF shutdown. A manual Windows acceptance pass should still exercise the first-run password dialog, tray menu, browser launch, unexpected child exit, duplicate launch prompt, and logout/shutdown behavior.

## Lifecycle and security behavior

- The exact per-session mutex is `Local\CPACloud.Launcher`.
- Before creating that mutex, the launcher checks `Local\CPACloud.Setup.2D7C1C6E-20A9-46F4-9955-24465BD75DCB` and exits with a bilingual message while an install or uninstall is active.
- First-run initialization checks state through `--check-initialized`; it does not infer success from file existence.
- The password and confirmation stay in native password fields. The password is sent as raw UTF-8 bytes to `cpa-cloud.exe --init` over redirected stdin, which is then closed. It is never placed in command-line arguments or launcher logs.
- Before each service start, the launcher verifies that `127.0.0.1:8787` can be bound. It never kills the port owner and never chooses another port.
- Every start uses a new UUID with `--instance-id`. The browser opens only while the owned child is alive and `/healthz` returns that exact identifier. Health requests do not follow redirects; each headers-plus-body attempt is time-bounded and rejects bodies larger than 4 KiB.
- Child stdout and stderr are continuously read into fixed-size discard buffers, so service-lifetime output is neither logged nor accumulated in launcher memory.
- Normal stop and launcher exit close the child stdin pipe first. Only if the owned child misses the bounded shutdown deadline is that same process tree terminated.
- Unexpected exits are shown as an error and are not restarted automatically.

This preview launcher is unsigned, has no auto-update behavior, and does not add a startup/login entry.

The first-run dialog sizes itself to its content, including validation messages.
The self-test exercises enlarged fonts and 100–200% layout scaling with a
constrained initial client area, checking that both actions remain inside all
ancestor containers. The previous fixed-height dialog fails this regression.
