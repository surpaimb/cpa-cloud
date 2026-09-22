[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$PortableArchive,
    [Parameter(Mandatory = $true)][string]$PortableChecksum,
    [Parameter(Mandatory = $true)][string]$Output,
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][ValidateSet('amd64', 'arm64')][string]$Arch,
    [Parameter(Mandatory = $true)][string]$NSISRoot,
    [string]$WixExecutable = '',
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

function Get-StableWixId([string]$Prefix, [string]$Value) {
    $bytes = [Text.Encoding]::UTF8.GetBytes($Value.ToLowerInvariant())
    $hash = [Security.Cryptography.SHA256]::HashData($bytes)
    return $Prefix + ([Convert]::ToHexString($hash).Substring(0, 24))
}

function Write-WixDirectoryTree([Xml.XmlWriter]$Writer, [string]$PayloadRoot, [string]$RelativeDirectory, [hashtable]$DirectoryIds) {
    $directoryPath = if ([string]::IsNullOrEmpty($RelativeDirectory)) { $PayloadRoot } else { Join-Path $PayloadRoot $RelativeDirectory }
    foreach ($directory in @(Get-ChildItem -LiteralPath $directoryPath -Directory -Force | Sort-Object Name)) {
        $childRelative = if ([string]::IsNullOrEmpty($RelativeDirectory)) { $directory.Name } else { Join-Path $RelativeDirectory $directory.Name }
        $directoryId = Get-StableWixId 'dir_' $childRelative
        $DirectoryIds[$childRelative] = $directoryId
        $Writer.WriteStartElement('Directory')
        $Writer.WriteAttributeString('Id', $directoryId)
        $Writer.WriteAttributeString('Name', $directory.Name)
        Write-WixDirectoryTree $Writer $PayloadRoot $childRelative $DirectoryIds
        $Writer.WriteEndElement()
    }
}

function Write-WixSource([string]$Path, [string]$PayloadRoot, [string]$MsiVersion, [string]$UpgradeCode, [string]$Platform) {
    $settings = [Xml.XmlWriterSettings]::new()
    $settings.Encoding = [Text.UTF8Encoding]::new($false)
    $settings.Indent = $true
    $writer = [Xml.XmlWriter]::Create($Path, $settings)
    $namespace = 'http://wixtoolset.org/schemas/v4/wxs'
    $directoryIds = @{ '' = 'INSTALLFOLDER' }
    try {
        $writer.WriteStartDocument()
        $writer.WriteStartElement('Wix', $namespace)
        $writer.WriteStartElement('Package')
        $writer.WriteAttributeString('Name', 'CPA Cloud')
        $writer.WriteAttributeString('Manufacturer', 'CPA Cloud Contributors')
        $writer.WriteAttributeString('Version', $MsiVersion)
        $writer.WriteAttributeString('UpgradeCode', $UpgradeCode)
        $writer.WriteAttributeString('Scope', 'perUser')
        $writer.WriteAttributeString('InstallerVersion', '500')
        $writer.WriteAttributeString('Compressed', 'yes')

        $writer.WriteStartElement('MajorUpgrade')
        $writer.WriteAttributeString('AllowSameVersionUpgrades', 'yes')
        $writer.WriteAttributeString('DowngradeErrorMessage', 'A newer version of CPA Cloud is already installed.')
        $writer.WriteAttributeString('Schedule', 'afterInstallInitialize')
        $writer.WriteEndElement()
        $writer.WriteStartElement('MediaTemplate')
        $writer.WriteAttributeString('EmbedCab', 'yes')
        $writer.WriteEndElement()
        foreach ($property in @('ARPNOMODIFY', 'ARPNOREPAIR')) {
            $writer.WriteStartElement('Property')
            $writer.WriteAttributeString('Id', $property)
            $writer.WriteAttributeString('Value', '1')
            $writer.WriteEndElement()
        }
        $writer.WriteStartElement('Property')
        $writer.WriteAttributeString('Id', 'NSISINSTALLDETECTED')
        $writer.WriteStartElement('RegistrySearch')
        $writer.WriteAttributeString('Id', 'FindNsisInstall')
        $writer.WriteAttributeString('Root', 'HKCU')
        $writer.WriteAttributeString('Key', 'Software\Microsoft\Windows\CurrentVersion\Uninstall\CPACloud.Launcher')
        $writer.WriteAttributeString('Name', 'InstallLocation')
        $writer.WriteAttributeString('Type', 'raw')
        $writer.WriteAttributeString('Bitness', 'always32')
        $writer.WriteEndElement()
        $writer.WriteEndElement()
        $writer.WriteStartElement('Launch')
        $writer.WriteAttributeString('Condition', 'Installed OR NOT NSISINSTALLDETECTED')
        $writer.WriteAttributeString('Message', 'CPA Cloud is already managed by Setup.exe. Uninstall that package before installing the MSI.')
        $writer.WriteEndElement()

        $writer.WriteStartElement('StandardDirectory')
        $writer.WriteAttributeString('Id', 'LocalAppDataFolder')
        $writer.WriteStartElement('Directory')
        $writer.WriteAttributeString('Id', 'LocalProgramsFolder')
        $writer.WriteAttributeString('Name', 'Programs')
        $writer.WriteStartElement('Directory')
        $writer.WriteAttributeString('Id', 'INSTALLFOLDER')
        $writer.WriteAttributeString('Name', 'CPA Cloud')
        Write-WixDirectoryTree $writer $PayloadRoot '' $directoryIds
        $writer.WriteEndElement()
        $writer.WriteEndElement()
        $writer.WriteEndElement()

        $writer.WriteStartElement('StandardDirectory')
        $writer.WriteAttributeString('Id', 'ProgramMenuFolder')
        $writer.WriteStartElement('Directory')
        $writer.WriteAttributeString('Id', 'ApplicationProgramsFolder')
        $writer.WriteAttributeString('Name', 'CPA Cloud')
        $writer.WriteEndElement()
        $writer.WriteEndElement()

        $writer.WriteStartElement('ComponentGroup')
        $writer.WriteAttributeString('Id', 'PayloadComponents')
        $payloadPrefix = $PayloadRoot.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
        foreach ($file in @(Get-ChildItem -LiteralPath $PayloadRoot -File -Recurse -Force | Sort-Object FullName)) {
            $relative = $file.FullName.Substring($payloadPrefix.Length)
            $relativeDirectory = Split-Path -Parent $relative
            if ($relativeDirectory -eq '.') { $relativeDirectory = '' }
            $componentId = Get-StableWixId 'cmp_' $relative
            $fileId = Get-StableWixId 'fil_' $relative
            $writer.WriteStartElement('Component')
            $writer.WriteAttributeString('Id', $componentId)
            $writer.WriteAttributeString('Directory', $directoryIds[$relativeDirectory])
            $writer.WriteAttributeString('Guid', '*')
            $writer.WriteStartElement('File')
            $writer.WriteAttributeString('Id', $fileId)
            $writer.WriteAttributeString('Source', $file.FullName)
            $writer.WriteAttributeString('KeyPath', 'yes')
            $writer.WriteEndElement()
            $writer.WriteEndElement()
        }
        $writer.WriteEndElement()

        $writer.WriteStartElement('Component')
        $writer.WriteAttributeString('Id', 'StartMenuShortcuts')
        $writer.WriteAttributeString('Directory', 'ApplicationProgramsFolder')
        $writer.WriteAttributeString('Guid', '*')
        $writer.WriteAttributeString('Bitness', 'always32')
        foreach ($shortcut in @(
            @{ Id = 'StartMenuLaunch'; Name = 'CPA Cloud'; Target = '[INSTALLFOLDER]CPACloud.Launcher.exe' },
            @{ Id = 'StartMenuUninstall'; Name = 'Uninstall CPA Cloud'; Target = '[SystemFolder]msiexec.exe'; Arguments = '/x [ProductCode]' }
        )) {
            $writer.WriteStartElement('Shortcut')
            $writer.WriteAttributeString('Id', $shortcut.Id)
            $writer.WriteAttributeString('Name', $shortcut.Name)
            $writer.WriteAttributeString('Target', $shortcut.Target)
            if ($shortcut.ContainsKey('Arguments')) { $writer.WriteAttributeString('Arguments', $shortcut.Arguments) }
            $writer.WriteAttributeString('WorkingDirectory', 'INSTALLFOLDER')
            $writer.WriteEndElement()
        }
        $writer.WriteStartElement('RemoveFolder')
        $writer.WriteAttributeString('Id', 'RemoveApplicationProgramsFolder')
        $writer.WriteAttributeString('On', 'uninstall')
        $writer.WriteEndElement()
        $writer.WriteStartElement('RegistryValue')
        $writer.WriteAttributeString('Root', 'HKCU')
        $writer.WriteAttributeString('Key', 'Software\CPACloud')
        $writer.WriteAttributeString('Name', 'MsiInstalled')
        $writer.WriteAttributeString('Type', 'integer')
        $writer.WriteAttributeString('Value', '1')
        $writer.WriteAttributeString('KeyPath', 'yes')
        $writer.WriteEndElement()
        $writer.WriteEndElement()

        $writer.WriteStartElement('Feature')
        $writer.WriteAttributeString('Id', 'MainFeature')
        $writer.WriteAttributeString('Title', 'CPA Cloud')
        $writer.WriteAttributeString('Level', '1')
        $writer.WriteStartElement('ComponentGroupRef')
        $writer.WriteAttributeString('Id', 'PayloadComponents')
        $writer.WriteEndElement()
        $writer.WriteStartElement('ComponentRef')
        $writer.WriteAttributeString('Id', 'StartMenuShortcuts')
        $writer.WriteEndElement()
        $writer.WriteEndElement()

        $writer.WriteEndElement()
        $writer.WriteEndElement()
        $writer.WriteEndDocument()
    } finally {
        $writer.Dispose()
    }
}

if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$') {
    throw "Version '$Version' must match vMAJOR.MINOR.PATCH-preview.N"
}
$versionMatch = [regex]::Match($Version, '^v([0-9]+)\.([0-9]+)\.([0-9]+)-preview\.([0-9]+)$')
$major = [int]$versionMatch.Groups[1].Value
$minor = [int]$versionMatch.Groups[2].Value
$patch = [int]$versionMatch.Groups[3].Value
$preview = [int]$versionMatch.Groups[4].Value
if ($major -gt 255 -or $minor -gt 255 -or $patch -gt 65 -or $preview -gt 999) {
    throw "Version '$Version' cannot be represented safely as a Windows Installer product version."
}
$msiVersion = "$major.$minor.$(($patch * 1000) + $preview)"

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
    if (-not [string]::IsNullOrWhiteSpace($WixExecutable)) {
        Copy-RequiredFile (Join-Path $root 'packaging/windows/WIX-LICENSE.txt') (Join-Path $payload 'THIRD-PARTY-LICENSES/installer-tooling/WIX-LICENSE.txt')
    }

    $dotnetInfo = & $DotNetExecutable --info | Out-String
    $buildInfo = @(
        "Version: $Version"
        "Target: windows/$Arch ($rid)"
        "Launcher: self-contained .NET Windows Desktop"
        "Dotnet SDK: $sdkVersion"
        $(if ([string]::IsNullOrWhiteSpace($WixExecutable)) { 'Installer compiler: NSIS 3.12' } else { 'Installer compilers: NSIS 3.12 and WiX Toolset 4.0.6' })
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

    if (-not [string]::IsNullOrWhiteSpace($WixExecutable)) {
        $wixCommand = Get-Command $WixExecutable -ErrorAction Stop
        $wix = $wixCommand.Source
        $wixSource = Join-Path $stageRoot 'CPACloud.wxs'
        $upgradeCode = if ($Arch -eq 'amd64') { '1D7FCA5F-8D7E-4C66-8B79-7EF60BF64321' } else { 'A1252A49-8690-44DB-9BB0-6AB93BA74472' }
        $wixPlatform = if ($Arch -eq 'amd64') { 'x64' } else { 'arm64' }
        Write-WixSource $wixSource $payload $msiVersion $upgradeCode $wixPlatform
        $expectedMsi = Join-Path $out "cpa-cloud_${Version}_windows_${Arch}.msi"
        if (Test-Path -LiteralPath $expectedMsi) {
            throw "Refusing to overwrite existing MSI: $expectedMsi"
        }
        & $wix build -arch $wixPlatform -pdbtype none -o $expectedMsi $wixSource
        if ($LASTEXITCODE -ne 0) {
            throw 'WiX MSI compilation failed.'
        }
        $null = Resolve-ExistingFile $expectedMsi 'Compiled MSI'
        $msiSignature = Get-AuthenticodeSignature -LiteralPath $expectedMsi
        if ($msiSignature.Status -ne [Management.Automation.SignatureStatus]::NotSigned) {
            throw "Unexpected MSI signature state: $($msiSignature.Status)"
        }
        $msiHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $expectedMsi).Hash.ToLowerInvariant()
        Set-Content -LiteralPath ($expectedMsi + '.sha256') -Value "$msiHash  $([IO.Path]::GetFileName($expectedMsi))" -Encoding ASCII
        Write-Output "Windows MSI: $expectedMsi"
    }

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
