[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$PortableArchive,
    [Parameter(Mandatory = $true)][string]$PortableChecksum,
    [Parameter(Mandatory = $true)][string]$Output,
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][ValidateSet('amd64', 'arm64')][string]$Arch,
    [Parameter(Mandatory = $true)][string]$NSISRoot,
    [string]$DotNetExecutable = 'dotnet'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Resolve-ExistingFile([string]$Path, [string]$Label) {
    $resolved = [IO.Path]::GetFullPath($Path)
    if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "$Label does not exist: $resolved"
    }
    return $resolved
}

function Resolve-ExistingDirectory([string]$Path, [string]$Label) {
    $resolved = [IO.Path]::GetFullPath($Path)
    if (-not (Test-Path -LiteralPath $resolved -PathType Container)) {
        throw "$Label does not exist: $resolved"
    }
    return $resolved
}

function Copy-DirectoryContents([string]$From, [string]$To) {
    New-Item -ItemType Directory -Path $To -Force | Out-Null
    Get-ChildItem -LiteralPath $From -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $To -Recurse -Force
    }
}

function Copy-RequiredFile([string]$From, [string]$To) {
    if (-not (Test-Path -LiteralPath $From -PathType Leaf)) {
        throw "Required notice is missing: $From"
    }
    $parent = Split-Path -Parent $To
    New-Item -ItemType Directory -Path $parent -Force | Out-Null
    Copy-Item -LiteralPath $From -Destination $To -Force
}

function Get-PeMachine([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    $reader = [IO.BinaryReader]::new($stream)
    try {
        if ($stream.Length -lt 64) {
            throw "PE file is too small: $Path"
        }
        $stream.Position = 0x3c
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0 -or $peOffset -gt ($stream.Length - 6)) {
            throw "PE header offset is invalid: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "PE signature is invalid: $Path"
        }
        return $reader.ReadUInt16()
    } finally {
        $reader.Dispose()
        $stream.Dispose()
    }
}

if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$') {
    throw "Version '$Version' must match vMAJOR.MINOR.PATCH-preview.N"
}

$root = Resolve-ExistingDirectory $Source 'Source root'
$archive = Resolve-ExistingFile $PortableArchive 'Portable archive'
$checksum = Resolve-ExistingFile $PortableChecksum 'Portable checksum'
$nsis = Resolve-ExistingDirectory $NSISRoot 'NSIS root'
$makensis = Resolve-ExistingFile (Join-Path $nsis 'makensis.exe') 'NSIS compiler'
$installerScript = Resolve-ExistingFile (Join-Path $root 'packaging/windows/CPACloud.nsi') 'NSIS script'
$out = [IO.Path]::GetFullPath($Output)
New-Item -ItemType Directory -Path $out -Force | Out-Null

$checksumLine = (Get-Content -LiteralPath $checksum -Raw).Trim()
$checksumMatch = [regex]::Match($checksumLine, '^([a-fA-F0-9]{64})\s{2}([^\r\n]+)$')
if (-not $checksumMatch.Success) {
    throw "Portable checksum has an unexpected format: $checksum"
}
if ($checksumMatch.Groups[2].Value -ne [IO.Path]::GetFileName($archive)) {
    throw 'Portable checksum names a different archive.'
}
$actualArchiveHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash
if ($actualArchiveHash -ne $checksumMatch.Groups[1].Value) {
    throw 'Portable archive SHA-256 does not match its sidecar.'
}

$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$stageRoot = [IO.Path]::GetFullPath((Join-Path $tempBase ("cpa-cloud-windows-installer-" + [guid]::NewGuid().ToString('N'))))
if (-not $stageRoot.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Unsafe temporary path: $stageRoot"
}
New-Item -ItemType Directory -Path $stageRoot | Out-Null

try {
    $portableExtract = Join-Path $stageRoot 'portable'
    Expand-Archive -LiteralPath $archive -DestinationPath $portableExtract
    $portableRoots = @(Get-ChildItem -LiteralPath $portableExtract -Directory -Force)
    if ($portableRoots.Count -ne 1) {
        throw 'Portable archive must contain exactly one root directory.'
    }
    $portableRoot = $portableRoots[0].FullName
    foreach ($relative in @('cpa-cloud.exe', 'web/index.html', 'THIRD_PARTY_NOTICES.md', 'THIRD-PARTY-LICENSES')) {
        if (-not (Test-Path -LiteralPath (Join-Path $portableRoot $relative))) {
            throw "Portable archive is incomplete: missing $relative"
        }
    }
    $expectedMachine = if ($Arch -eq 'amd64') { 0x8664 } else { 0xaa64 }
    $serverPath = Join-Path $portableRoot 'cpa-cloud.exe'
    if ((Get-PeMachine $serverPath) -ne $expectedMachine) {
        throw "Portable server architecture does not match windows/$Arch."
    }

    $projects = @(Get-ChildItem -LiteralPath (Join-Path $root 'desktop/windows') -Filter '*.csproj' -File -Recurse)
    if ($projects.Count -ne 1) {
        throw "Expected exactly one Windows launcher project under desktop/windows; found $($projects.Count)."
    }
    $rid = if ($Arch -eq 'amd64') { 'win-x64' } else { 'win-arm64' }
    $launcherOutput = Join-Path $stageRoot 'launcher'
    & $DotNetExecutable publish $projects[0].FullName -c Release -r $rid --self-contained true -p:PublishSingleFile=true -p:PublishTrimmed=false -o $launcherOutput
    if ($LASTEXITCODE -ne 0) {
        throw 'Windows launcher publish failed.'
    }
    $launcher = Resolve-ExistingFile (Join-Path $launcherOutput 'CPACloud.Launcher.exe') 'Published launcher'
    if ((Get-PeMachine $launcher) -ne $expectedMachine) {
        throw "Published launcher architecture does not match windows/$Arch."
    }

    $payload = Join-Path $stageRoot 'payload'
    Copy-DirectoryContents $launcherOutput $payload
    Copy-Item -LiteralPath $serverPath -Destination (Join-Path $payload 'cpa-cloud.exe') -Force
    Copy-Item -LiteralPath (Join-Path $portableRoot 'web') -Destination (Join-Path $payload 'web') -Recurse -Force
    Copy-Item -LiteralPath (Join-Path $portableRoot 'THIRD-PARTY-LICENSES') -Destination (Join-Path $payload 'THIRD-PARTY-LICENSES') -Recurse -Force
    foreach ($name in @('THIRD_PARTY_NOTICES.md', 'GO-DEPENDENCIES.txt', 'FRONTEND-DEPENDENCIES.txt')) {
        $sourceNotice = Join-Path $portableRoot $name
        if (Test-Path -LiteralPath $sourceNotice -PathType Leaf) {
            Copy-Item -LiteralPath $sourceNotice -Destination (Join-Path $payload $name) -Force
        }
    }

    $dotnetCommand = Get-Command $DotNetExecutable -ErrorAction Stop
    $dotnetRoot = Split-Path -Parent $dotnetCommand.Source
    $dotnetNoticeRoot = Join-Path $payload 'THIRD-PARTY-LICENSES/dotnet-self-contained'
    Copy-RequiredFile (Join-Path $dotnetRoot 'LICENSE.txt') (Join-Path $dotnetNoticeRoot 'LICENSE.txt')
    Copy-RequiredFile (Join-Path $dotnetRoot 'ThirdPartyNotices.txt') (Join-Path $dotnetNoticeRoot 'ThirdPartyNotices.txt')

    $sdkVersion = (& $DotNetExecutable --version).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($sdkVersion)) {
        throw 'Unable to determine the .NET SDK version.'
    }
    $windowsDesktopSDK = Join-Path $dotnetRoot "sdk/$sdkVersion/Sdks/Microsoft.NET.Sdk.WindowsDesktop"
    Copy-RequiredFile (Join-Path $windowsDesktopSDK 'LICENSE.TXT') (Join-Path $dotnetNoticeRoot 'WindowsDesktop-SDK-LICENSE.TXT')
    Copy-RequiredFile (Join-Path $windowsDesktopSDK 'THIRD-PARTY-NOTICES.TXT') (Join-Path $dotnetNoticeRoot 'WindowsDesktop-SDK-THIRD-PARTY-NOTICES.TXT')
    Copy-RequiredFile (Join-Path $nsis 'COPYING') (Join-Path $payload 'THIRD-PARTY-LICENSES/installer-tooling/NSIS-COPYING.txt')

    $dotnetInfo = & $DotNetExecutable --info | Out-String
    $buildInfo = @(
        "Version: $Version"
        "Target: windows/$Arch ($rid)"
        "Launcher: self-contained .NET Windows Desktop"
        "Dotnet SDK: $sdkVersion"
        'Installer compiler: NSIS 3.12'
        'Installer scope: current user; no elevation requested'
        'Code signing: not performed'
        'Runtime UI validation: requires the matching GitHub-hosted runner and end-user test machine'
        ''
        $dotnetInfo.TrimEnd()
    ) -join "`r`n"
    Set-Content -LiteralPath (Join-Path $payload 'DESKTOP-BUILD-INFO.txt') -Value $buildInfo -Encoding UTF8

    # Generate exact, compile-time path checks and uninstall commands. No
    # recursive deletion is permitted: extra entries are left in place, and
    # reparse points in any target or ancestor cause setup to stop.
    $manifest = Join-Path $stageRoot 'uninstall-files.nsh'
    $manifestLines = [Collections.Generic.List[string]]::new()
    $manifestLines.Add('!macro CheckPayloadPathsNotReparse CHECK_FUNCTION')
    $payloadPrefix = $payload.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $payloadFiles = @(Get-ChildItem -LiteralPath $payload -File -Recurse | Sort-Object FullName)
    foreach ($file in $payloadFiles) {
        $relative = $file.FullName.Substring($payloadPrefix.Length).Replace('/', '\')
        if ($relative.IndexOfAny([char[]]@('"', '$', "`r", "`n")) -ge 0) {
            throw "Payload path cannot be represented safely in NSIS: $relative"
        }
        $manifestLines.Add(('  Push "$INSTDIR\{0}"' -f $relative))
        $manifestLines.Add('  Call ${CHECK_FUNCTION}')
    }
    $payloadDirectories = @(Get-ChildItem -LiteralPath $payload -Directory -Recurse | ForEach-Object {
        $relative = $_.FullName.Substring($payloadPrefix.Length).Replace('/', '\')
        [pscustomobject]@{ Relative = $relative; Depth = $relative.Split('\').Count }
    } | Sort-Object @{ Expression = 'Depth'; Descending = $true }, @{ Expression = 'Relative'; Descending = $true })
    foreach ($directory in $payloadDirectories) {
        $relative = $directory.Relative
        if ($relative.IndexOfAny([char[]]@('"', '$', "`r", "`n")) -ge 0) {
            throw "Payload path cannot be represented safely in NSIS: $relative"
        }
        $manifestLines.Add(('  Push "$INSTDIR\{0}"' -f $relative))
        $manifestLines.Add('  Call ${CHECK_FUNCTION}')
    }
    $manifestLines.Add('!macroend')
    $manifestLines.Add('!macro RemovePayloadFiles')
    foreach ($file in $payloadFiles) {
        $relative = $file.FullName.Substring($payloadPrefix.Length).Replace('/', '\')
        $manifestLines.Add(('  Delete "$INSTDIR\{0}"' -f $relative))
    }
    foreach ($directory in $payloadDirectories) {
        $relative = $directory.Relative
        $manifestLines.Add(('  RMDir "$INSTDIR\{0}"' -f $relative))
    }
    $manifestLines.Add('!macroend')
    [IO.File]::WriteAllLines($manifest, $manifestLines, [Text.UTF8Encoding]::new($false))

    $expectedInstaller = Join-Path $out "cpa-cloud_${Version}_windows_${Arch}_Setup.exe"
    if (Test-Path -LiteralPath $expectedInstaller) {
        throw "Refusing to overwrite existing installer: $expectedInstaller"
    }
    $defines = @(
        '/V4'
        '/INPUTCHARSET'
        'UTF8'
        "/DAPP_VERSION=$Version"
        "/DTARGET_ARCH=$Arch"
        "/DPAYLOAD_DIR=$payload"
        "/DOUTPUT_DIR=$out"
        "/DUNINSTALL_MANIFEST=$manifest"
        $installerScript
    )
    & $makensis @defines
    if ($LASTEXITCODE -ne 0) {
        throw 'NSIS compilation failed.'
    }
    $null = Resolve-ExistingFile $expectedInstaller 'Compiled installer'
    $installerHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $expectedInstaller).Hash.ToLowerInvariant()
    Set-Content -LiteralPath ($expectedInstaller + '.sha256') -Value "$installerHash  $([IO.Path]::GetFileName($expectedInstaller))" -Encoding ASCII

    Write-Output "Windows installer: $expectedInstaller"
    Write-Output "Launcher payload: $launcher"
} finally {
    if (Test-Path -LiteralPath $stageRoot) {
        $resolvedStage = [IO.Path]::GetFullPath($stageRoot)
        if (-not $resolvedStage.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -or -not ([IO.Path]::GetFileName($resolvedStage)).StartsWith('cpa-cloud-windows-installer-')) {
            throw "Refusing to remove unsafe temporary path: $resolvedStage"
        }
        Remove-Item -LiteralPath $resolvedStage -Recurse -Force
    }
}
