# CPA Cloud

[简体中文](README.md) | **English**

A self-hosted AI access platform for internal enterprise use. Administrators manage upstreams, models, and employee keys through a web console; employees use standard APIs without WeChat or a dedicated client. Authentication, permission checks, and upstream requests all run in the same Go service process.

> This project is currently a development preview, not a production release. All six initial preview packages passed Go tests, builds, and executable help checks on matching GitHub runner architectures. The downloaded Windows amd64 package also passed a process-level mock-upstream test. Complete deployment validation on all end-user hosts remains outstanding.

## Features and boundaries

| Implemented | Not yet implemented or validated |
| --- | --- |
| Web console, administrator sessions, employee enable/disable, model permissions | Multi-tenancy, SSO, administrator password-reset command |
| Multiple keys per employee, no expiration by default, optional expiration, revocation | ChatGPT/Codex, Claude, and Gemini membership authorization or import |
| OpenAI-compatible API-key upstreams and manual model mapping | Responses, Anthropic Messages, and native Gemini protocols |
| `/v1/models`, non-streaming and SSE Chat Completions | Complete compatibility testing with CC Switch and real AI tools |
| SQLite persistence, encrypted upstream credentials, restart recovery | Account pooling, reliable billing usage, automated backup/migration, production key management |

Employee keys remain valid across normal restarts. Revocation, employee disablement, optional expiration, and permission restrictions still take effect. Employees never need the upstream provider key.

## 1. Download and installation

**Current download: [v0.1.0-preview.2](https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.2)**. The table links directly to these versioned assets.

See preview versions and their attachments on [GitHub Releases](https://github.com/surpaimb/cpa-cloud/releases). A file is downloadable only when it actually appears under the Assets section of that Release. If a Release has no attachments yet, build from source as described below. Preview versions may not appear through GitHub's `latest` link.

| System | Asset filename suffix |
| --- | --- |
| Standard Intel/AMD Windows computer | [windows_amd64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_windows_amd64.zip) |
| Windows on ARM computer | [windows_arm64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_windows_arm64.zip) |
| Intel/AMD Linux cloud server | [linux_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_linux_amd64.tar.gz) |
| ARM Linux server | [linux_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_linux_arm64.tar.gz) |
| Intel-based Mac | [macos_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_macos_amd64.tar.gz) |
| Apple Silicon Mac (M series) | [macos_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_macos_arm64.tar.gz) |

An archive should contain the executable, the `web/` directory, and third-party notices. Extract everything into a dedicated directory. Go and Bun are not required to run a packaged build. Keep all notice files. Store the data directory outside the extracted application directory so it is easier to preserve during upgrades.

After downloading, verify the file against the SHA256 manifest attached to the Release. On Windows, run `Get-FileHash <downloaded-file> -Algorithm SHA256`; on Linux, run `sha256sum <downloaded-file>`; on macOS, run `shasum -a 256 <downloaded-file>`. Compare the result with the corresponding manifest entry.

All following commands are run from the **extracted application directory**. The executable is named `cpa-cloud.exe` on Windows and `cpa-cloud` on Linux/macOS. If the Unix executable bit is missing, run `chmod +x ./cpa-cloud`. Windows code signing and macOS notarization are not currently promised. Evaluate provenance and signing requirements under your organization's policy; do not disable operating-system security globally.

### Desktop installers

Windows Setup and macOS DMG packages are published. All ten artifacts were built automatically by [GitHub Actions](https://github.com/surpaimb/cpa-cloud/actions/runs/35726220674). Both Windows architectures passed installation, same-package upgrade/reinstall, uninstall, mutex refusal, and data-preservation checks.

| System | Package | Installation |
| --- | --- | --- |
| Intel/AMD Windows | [windows_amd64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_windows_amd64_Setup.exe) | Run Setup for the current user, then launch from the Start menu |
| Windows on ARM | [windows_arm64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_windows_arm64_Setup.exe) | Use the ARM64 installer with the same steps |
| Intel Mac (macOS 13 or newer) | [macos_amd64.dmg](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_macos_amd64.dmg) | Open the DMG, drag `CPA Cloud.app` into Applications, then launch it from Applications |
| Apple Silicon Mac (macOS 13 or newer) | [macos_arm64.dmg](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.2/cpa-cloud_v0.1.0-preview.2_macos_arm64.dmg) | Use the ARM64 DMG with the same steps |

The Windows installer includes the .NET runtime. On first launch, set and confirm the administrator password in the native dialog (12–72 UTF-8 bytes). Once the service is ready, the launcher opens `http://127.0.0.1:8787`; the username is `admin`. Installer users can skip sections 2 and 3 and continue with web configuration in section 4.

The Windows tray or macOS menu bar provides open console, start, stop, and quit actions. Closing the browser leaves the service running; quitting the launcher stops its owned service. If port 8787 is occupied, resolve the conflict before starting. The launcher binds to loopback only. For cloud or LAN deployment, use a portable package and the HTTPS instructions below.

Data lives outside the installation directory: `%LOCALAPPDATA%\CPACloud\data` on Windows, or `~/Library/Application Support/CPACloud/data` on macOS. Before upgrading, quit the launcher and back up this data, then install the new version. Uninstalling or deleting the Mac app does not intentionally remove this data directory. The launcher does not import existing CLI data, add a login/startup entry, or download updates automatically.

The installer preview has no publisher code signing or Apple notarization; the operating system may block first launch. Verify provenance under your organization's policy. Current validation includes Windows installation lifecycle checks, native builds, automated tests, and DMG mount/readback checks, not complete desktop interaction testing.

READMEs inside portable archives are fixed to the release source and may retain prepublication “next preview” wording. This page and the Release assets provide the current download status.

## 2. First-time initialization from the command line

The initial username is always `admin`. The password is supplied through stdin and must be **12–72 UTF-8 bytes** long (not 12–72 Chinese characters). After successful initialization, the process exits. Initialization is required only once.

Installers normally initialize through the first-run dialog. Command-line initialization must use the same data directory as the launcher. Portable examples use `../cpa-cloud-data` next to the executable directory; always use the same directory for initialization and startup.

### Windows PowerShell

**Windows installer (Setup.exe)**: run the complete block below from any PowerShell directory. Close the initialization dialog first. Copy only the code, without terminal prompts or Markdown fences. A missing executable is detected before asking for a password.

```powershell
& {
    $exe = "$env:LOCALAPPDATA\Programs\CPA Cloud\cpa-cloud.exe"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe. Check the installation or portable path."
    }
    $secret = Read-Host 'Administrator password (12-72 UTF-8 bytes)' -AsSecureString
    $ptr = [IntPtr]::Zero
    $savedEncoding = $OutputEncoding
    try {
        $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
        $OutputEncoding = [Text.UTF8Encoding]::new($false)
        [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) |
            & $exe --data-dir $data --init
        if ($LASTEXITCODE -ne 0) { throw 'Initialization failed; see the message above.' }
    } finally {
        $OutputEncoding = $savedEncoding
        if ($ptr -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
        }
        if ($null -ne $secret) { $secret.Dispose() }
    }
}
```

**Windows portable package (ZIP)**: replace the `$exe` and `$data` lines above with the example below, edit the executable path to your actual extracted location, then run the complete modified block. No other data directory is selected automatically.

```powershell
$exe = "C:\Tools\CPACloud\cpa-cloud.exe"
$data = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $exe) "..\cpa-cloud-data"))
```

After success, installer users reopen CPA Cloud from the Start menu; portable users continue with section 3. Installer data is in `%LOCALAPPDATA%\CPACloud\data`; do not substitute the portable relative directory.

### Linux / macOS Bash

```bash
umask 077
read -r -s -p 'Administrator password (12-72 UTF-8 bytes): ' CPA_ADMIN_PASSWORD
printf '\n'
printf '%s' "$CPA_ADMIN_PASSWORD" | ./cpa-cloud --data-dir ../cpa-cloud-data --init
unset CPA_ADMIN_PASSWORD
```

The success message is `Initialized administrator admin.` If the directory has already been initialized, start the service using that existing directory. Do not delete data to reset the password. There is currently no administrator password-reset command.

## 3. Local startup

**Windows installer**: normally launch CPA Cloud from the Start menu. For foreground command-line operation, quit the tray launcher first to avoid a port conflict. Run this complete block from any directory; it uses the existing installer data.

```powershell
& {
    $appDir = "$env:LOCALAPPDATA\Programs\CPA Cloud"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    $exe = Join-Path $appDir 'cpa-cloud.exe'
    $web = Join-Path $appDir 'web'
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $web 'index.html') -PathType Leaf)) {
        throw "CPA Cloud web files not found: $web"
    }
    & $exe --data-dir $data --listen 127.0.0.1:8787 --web-dir $web
    if ($LASTEXITCODE -ne 0) { throw 'CPA Cloud failed to start; see the message above.' }
}
```

**Windows portable package**: replace `$appDir` and `$data` above with the following paths, edit the extracted location, then run the complete block. The data directory must match initialization.

```powershell
$appDir = "C:\Tools\CPACloud"
$data = [IO.Path]::GetFullPath((Join-Path $appDir "..\cpa-cloud-data"))
```

Linux / macOS:

```bash
./cpa-cloud --data-dir ../cpa-cloud-data --listen 127.0.0.1:8787 --web-dir ./web
```

After the service starts successfully, click [Open the local admin console](http://127.0.0.1:8787/) and sign in as `admin`.

Alternatively, copy this address into your browser's address bar:

```text
http://127.0.0.1:8787/
```

This address points to the current computer. Open it on the computer running CPA Cloud and keep the service running. The `/healthz` health check returns `{"status":"ok"}`. In command-line mode, the process runs in the foreground; press Ctrl+C to stop it. Closing the terminal will usually stop the service as well.

## 4. Web-console configuration

### Add an upstream

| Field | Example / description |
| --- | --- |
| Name | `Company API`, or another recognizable name |
| Type | Currently only `openai-compatible` |
| Endpoint | `https://api.example.com/v1`, replaced with the provider's actual base URL |
| API Key | The upstream key issued by the provider, not an employee key |

Do not enter a full `/chat/completions` URL. An Endpoint ending in `/v1` has `/chat/completions` appended; other base paths have `/v1/chat/completions` appended. Use a public HTTPS address reachable from the service host. Private, link-local, and CGNAT addresses and redirects are currently rejected. The service does not use `HTTP_PROXY` or `HTTPS_PROXY`.

**Deploying the platform on an internal network does not mean internal model upstreams are supported.** Employees on the internal network may access this platform, but the upstream restrictions still apply. `--allow-loopback-upstream` is only for local mock testing; it is not a switch that permits arbitrary internal addresses.

### Add a model

1. Set the model ID visible to employees, for example `company-chat`.
2. Select the upstream you just added.
3. Enter an upstream model ID that the provider actually supports and that the current provider key is authorized to use.

The model catalog is configured by an administrator and is not synchronized automatically from a provider. Each model currently maps to one route.

### Create employees and keys

1. Create an employee with a name and optional department and notes.
2. Grant access to all models or to an explicit model list.
3. Create and name a key for its intended use. Leaving the expiration empty makes it permanent.
4. Immediately copy the key, which is displayed only once, and deliver it through a secure channel approved by your organization.
5. Deliver the service Base URL and model ID at the same time. If a key is lost, create a replacement and revoke the old key.

New requests are rejected after revocation. Do not assume that a stream which has already started will always be interrupted immediately.

## 5. Employee configuration and API verification

Use a client that supports **OpenAI Chat Completions**:

| Setting | Local example | Company deployment example |
| --- | --- | --- |
| Base URL | `http://127.0.0.1:8787/v1` | `https://ai.example.com:8787/v1` |
| API Key | Employee key generated by an administrator | Employee key generated by an administrator |
| Model | `company-chat` | Model ID published by the administrator |

A remote employee cannot use `127.0.0.1` to reach an administrator's computer; that address refers to the employee's own machine.

CC Switch may help configure tools, but the tool making the final request must support the protocol currently implemented. **Do not treat this endpoint as already compatible with Codex Responses, Claude Messages, or the native Gemini API.** Complete real-device and real-tool validation remains pending. See [Employee access](docs/employee-access.md).

Bash + curl test example (the key is entered interactively, and the request header is passed through stdin):

```bash
CPA_BASE_URL='http://127.0.0.1:8787'
read -r -s -p 'Employee Key: ' CPA_EMPLOYEE_KEY
printf '\n'
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- "$CPA_BASE_URL/v1/models"
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- -H 'Content-Type: application/json' \
  "$CPA_BASE_URL/v1/chat/completions" \
  --data '{"model":"company-chat","messages":[{"role":"user","content":"Hello"}],"stream":false}'
unset CPA_EMPLOYEE_KEY
```

For streaming, change `stream` to `true` and add `-N` to curl. Calls to a real upstream may incur provider charges.

## 6. HTTPS on cloud servers and company networks

The service does not require a graphical desktop; administrators operate it remotely through a browser. **A non-loopback listener requires both a TLS certificate and a private key.** Automatic certificate issuance is not currently available.

Prepare a certificate that matches the service domain and is trusted by employee devices. Configure DNS and the firewall rule for the chosen port. Install the files, initialize using the same data directory that will be used at runtime, and then start the service. Linux example:

```bash
/opt/cpa-cloud/cpa-cloud \
  --data-dir /var/lib/cpa-cloud \
  --web-dir /opt/cpa-cloud/web \
  --listen 0.0.0.0:8787 \
  --tls-cert /etc/cpa-cloud/fullchain.pem \
  --tls-key /etc/cpa-cloud/privkey.pem
```

Open `https://ai.example.com:8787`; the employee Base URL is `https://ai.example.com:8787/v1`. Windows and macOS use the same flags with platform-appropriate paths.

The service account must be able to read the executable, web files, and certificates and write the data directory. Protect the TLS private key and data directory from other unprivileged users. Use one data directory for one service instance; do not run multiple processes against the same SQLite database.

This example uses TLS provided directly by the service. Trusted reverse-proxy configuration is not currently implemented. Do not terminate HTTPS at a proxy and forward plain HTTP without a supported configuration, because Origin validation and secure cookies may no longer match. The repository does not yet provide systemd, Windows Service, or Docker Compose installation. Complete a foreground startup test before configuring long-running operation.

## 7. Flags, data, and maintenance

| Flag | Default / purpose |
| --- | --- |
| `--data-dir` | `cpa-cloud` under the system user's configuration directory; explicitly specifying an absolute path is recommended |
| `--listen` | `127.0.0.1:8787` |
| `--web-dir` | Empty; the web console is unavailable when this flag is not set |
| `--init` | Initialize from stdin and then exit |
| `--tls-cert` / `--tls-key` | Must be set together; required for a non-loopback listener |
| `--allow-loopback-upstream` | Disabled by default; local development testing only |

Run `cpa-cloud --help` to see the binary flags.

The data directory contains `cpa-cloud.db`, possible WAL/SHM files, and **`master.key`**. Employee keys are stored as keyed digests, and upstream credentials are encrypted. The host administrator can still access secrets used by the running service. Losing or replacing `master.key` makes existing encrypted credentials unusable.

There is currently no automated backup or restore command and no upgrade-migration guarantee. Before a manual upgrade, stop the service, copy the **entire data directory** to a protected location, and retain the previous executable and web files. To restore, stop the service, restore one complete data snapshot with the corresponding application version, and test it in an isolated environment first. Do not copy only the main database file while the service is running. This manual maintenance procedure has not yet received complete automated restore validation.

## 8. Build from source

Git, Go 1.26 or a newer compatible version, and Bun must be installed and available on PATH. The currently validated tool versions are Go 1.26.6 and Bun 1.3.14.

```sh
git clone https://github.com/surpaimb/cpa-cloud.git
cd cpa-cloud
```

Windows PowerShell:

```powershell
Push-Location web
bun install --frozen-lockfile
Pop-Location
.\scripts\build.ps1
cd .\dist\windows-amd64
```

Initialize and start the service from that output directory using the earlier instructions. The script accepts `-GoExecutable` and `-BunExecutable` to specify tool paths, plus `-TargetOS windows|linux|darwin` and `-TargetArch amd64|arm64`. `-SkipWeb` builds only the service and does not package the web console.

Linux / macOS Bash (build the host architecture from the repository root):

```bash
(cd web && bun install --frozen-lockfile && bun run build)
mkdir -p dist/local
go build -trimpath -o dist/local/cpa-cloud ./cmd/cpa-cloud
mkdir -p dist/local/web
cp -R web/dist/. dist/local/web/
cd dist/local
```

A source build is not automatically a distributable package. External distribution must also include the original third-party notices and the corresponding version information.

## 9. Troubleshooting

| Problem | What to check |
| --- | --- |
| Web page returns 404 | `--web-dir` must directly contain `index.html`; build the web console first |
| Initialization is requested / key file is missing | Initialization and startup must use the same data directory and operating-system account |
| Password is rejected | It must be 12–72 UTF-8 bytes; a trailing newline is removed |
| Remote listener fails | Supply a valid matching TLS certificate and private key |
| Login or write operation fails | The browser Origin must match the service domain, port, and scheme |
| Upstream address is rejected | Check HTTPS, DNS, public address, and path; the testing flag permits loopback only |
| Model list is empty | Check the model route, upstream enabled state, and employee permissions |
| Employee receives 401 / 403 | Check the key, revocation/expiration, employee state, and model permissions |
| Upstream request fails | Check the provider key, quota, model ID, network, and certificates; never paste secrets into a support report |
| Codex / Claude Code request fails | Responses, Messages, and other protocols are currently missing; the key is not necessarily the problem |

## 10. Development validation

```bash
go test ./... -count=1 -timeout=2m
go vet ./...
(cd web && bun run test && bun run build)
```

After installing Node.js, run the mock-upstream acceptance test using absolute paths for both arguments:

```text
node scripts/smoke-preview.mjs <absolute-executable-path> <absolute-web-directory>
```

It covers initialization, the web entry point, management APIs, permanent keys, non-streaming/SSE, credential isolation, restart recovery, and persistent revocation without requiring real credentials. Race testing requires a CGO-capable toolchain and has not been run in the current Windows validation.

## Documentation and licenses

- [Integration evidence](docs/integration-status.md) · [Development plan](docs/development-plan.md) · [API contract](docs/preview-contract.md)
- [Product plan](docs/product-plan.md) · [Core design](docs/core-design.md) · [Acceptance matrix](docs/acceptance-matrix.md)
- [Membership integration research](docs/research/membership-feasibility.md) · [Protocol sources](docs/protocol-sources.md)
- [Independent implementation statement](docs/independent-implementation.md) · [Contribution guide](CONTRIBUTING.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md) · [Dependency inventory](docs/research/dependency-notices.md)

This project is independently implemented from public protocol documentation. Reference source code had previously been seen, so this project does not claim strict clean-room development. An MIT license is currently proposed for original project code, but a formal LICENSE file has not yet been added; the project does not currently claim that an MIT license has been granted. Third-party dependencies remain subject to their respective licenses, and their required notices must be retained in distributions.
