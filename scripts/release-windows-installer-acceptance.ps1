[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$InstallerChecksum,
    [Parameter(Mandatory = $true)][string]$Version
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$script:OwnedUninstallerCopies = @{}

function Assert-Condition([bool]$Condition, [string]$Message) {
    if (-not $Condition) {
        throw $Message
    }
}

function Invoke-BoundedProcess([string]$Path, [string]$ArgumentLine, [int]$TimeoutSeconds = 120) {
    $process = Start-Process -FilePath $Path -ArgumentList $ArgumentLine -PassThru -WindowStyle Hidden
    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "Process timed out after $TimeoutSeconds seconds: $Path"
    }
    $process.Refresh()
    return $process.ExitCode
}

function Remove-OwnedFileWithRetry([string]$Path, [string]$ExpectedSha256, [int]$TimeoutSeconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while (Test-Path -LiteralPath $Path) {
        try {
            $actualSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash
        } catch {
            if (-not (Test-Path -LiteralPath $Path)) {
                return
            }
            if ([DateTime]::UtcNow -ge $deadline) {
                throw "Temporary file ownership could not be verified before timeout: $Path"
            }
            Start-Sleep -Milliseconds 200
            continue
        }
        Assert-Condition ($actualSha256 -eq $ExpectedSha256) "Refusing to remove a temporary file whose ownership hash changed: $Path"
        try {
            Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
        } catch {
            if ([DateTime]::UtcNow -ge $deadline) {
                throw "Temporary file was not removed before timeout: $Path"
            }
            Start-Sleep -Milliseconds 200
        }
    }
}

function Invoke-RealNsisUninstaller([string]$InstalledUninstaller, [string]$HarnessDirectory, [string]$InstallDirectory) {
    $sourceSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $InstalledUninstaller).Hash
    $copyName = 'cpa-cloud-uninstaller-acceptance-{0}.exe' -f [Guid]::NewGuid().ToString('N')
    $harnessCopy = Assert-ChildPath (Join-Path $HarnessDirectory $copyName) $HarnessDirectory 'uninstaller harness copy'
    Assert-Condition (-not (Test-Path -LiteralPath $harnessCopy)) "Generated temporary uninstaller path already exists: $harnessCopy"
    $sourceStream = $null
    $destinationStream = $null
    $createdByHarness = $false
    try {
        try {
            $sourceStream = [IO.File]::Open($InstalledUninstaller, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
            $destinationStream = [IO.File]::Open($harnessCopy, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
            $createdByHarness = $true
            $script:OwnedUninstallerCopies[$harnessCopy] = $sourceSha256
            $sourceStream.CopyTo($destinationStream)
        } finally {
            if ($null -ne $destinationStream) {
                $destinationStream.Dispose()
            }
            if ($null -ne $sourceStream) {
                $sourceStream.Dispose()
            }
        }
        $copySha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $harnessCopy).Hash
        Assert-Condition ($copySha256 -eq $sourceSha256) "Temporary uninstaller copy hash does not match its source: $harnessCopy"
        # _?= must be the final, unquoted parameter. It disables another NSIS
        # bootstrap copy and makes this process the real uninstaller whose exit
        # code the harness can observe.
        return Invoke-BoundedProcess $harnessCopy "/S _?=$InstallDirectory"
    } finally {
        if ($createdByHarness) {
            Remove-OwnedFileWithRetry $harnessCopy $sourceSha256
            $script:OwnedUninstallerCopies.Remove($harnessCopy) | Out-Null
        }
    }
}

function Invoke-WithNamedMutex([string]$Name, [scriptblock]$Action) {
    $created = $false
    $mutex = [Threading.Mutex]::new($false, $Name, [ref]$created)
    if (-not $created) {
        $mutex.Dispose()
        throw "Acceptance test could not create mutex: $Name"
    }
    try {
        return & $Action
    } finally {
        $mutex.Dispose()
    }
}

function Assert-ChildPath([string]$Path, [string]$Parent, [string]$Label) {
    $resolvedPath = [IO.Path]::GetFullPath($Path)
    $resolvedParent = [IO.Path]::GetFullPath($Parent).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($resolvedParent, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Unsafe $Label path: $resolvedPath"
    }
    return $resolvedPath
}

function Wait-PathAbsent([string]$Path, [int]$TimeoutSeconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ((Test-Path -LiteralPath $Path) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 200
    }
    Assert-Condition (-not (Test-Path -LiteralPath $Path)) "Path was not removed before timeout: $Path"
}

if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted' -or [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
    throw 'This destructive installation acceptance test is restricted to an ephemeral GitHub-hosted Actions runner.'
}
if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$') {
    throw "Invalid preview version: $Version"
}

$installerPath = [IO.Path]::GetFullPath($Installer)
$checksumPath = [IO.Path]::GetFullPath($InstallerChecksum)
Assert-Condition (Test-Path -LiteralPath $installerPath -PathType Leaf) "Installer does not exist: $installerPath"
Assert-Condition (Test-Path -LiteralPath $checksumPath -PathType Leaf) "Installer checksum does not exist: $checksumPath"
$checksumLine = (Get-Content -LiteralPath $checksumPath -Raw).Trim()
$checksumMatch = [regex]::Match($checksumLine, '^([a-fA-F0-9]{64})\s{2}([^\r\n]+)$')
Assert-Condition $checksumMatch.Success 'Installer checksum has an unexpected format.'
Assert-Condition ($checksumMatch.Groups[2].Value -eq [IO.Path]::GetFileName($installerPath)) 'Installer checksum names a different file.'
$actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $installerPath).Hash
Assert-Condition ($actualHash -eq $checksumMatch.Groups[1].Value) 'Installer checksum does not match.'

$localAppData = [IO.Path]::GetFullPath([Environment]::GetFolderPath('LocalApplicationData'))
$appData = [IO.Path]::GetFullPath([Environment]::GetFolderPath('ApplicationData'))
$desktop = [IO.Path]::GetFullPath([Environment]::GetFolderPath('DesktopDirectory'))
$runnerTemp = [IO.Path]::GetFullPath($env:RUNNER_TEMP)
$installRoot = Assert-ChildPath (Join-Path $localAppData 'Programs\CPA Cloud') $localAppData 'install root'
$dataRoot = Assert-ChildPath (Join-Path $localAppData 'CPACloud\data') $localAppData 'data root'
$startMenuRoot = Assert-ChildPath (Join-Path $appData 'Microsoft\Windows\Start Menu\Programs\CPA Cloud') $appData 'Start menu root'
$desktopShortcut = Assert-ChildPath (Join-Path $desktop 'CPA Cloud.lnk') $desktop 'desktop shortcut'
$reparseTarget = Assert-ChildPath (Join-Path $runnerTemp 'cpa-cloud-installer-reparse-target') $runnerTemp 'reparse test target'
$uninstallRegistry = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CPACloud.Launcher'
$appRegistry = 'HKCU:\Software\CPACloud'

foreach ($path in @($installRoot, $dataRoot, $startMenuRoot, $desktopShortcut, $reparseTarget)) {
    Assert-Condition (-not (Test-Path -LiteralPath $path)) "Runner is not clean; refusing to use existing path: $path"
}
foreach ($registryPath in @($uninstallRegistry, $appRegistry)) {
    Assert-Condition (-not (Test-Path -LiteralPath $registryPath)) "Runner is not clean; refusing to use existing registry key: $registryPath"
}

$testOwnsPaths = $true
$dataSentinel = Join-Path $dataRoot 'acceptance-user-data.txt'
$installSentinel = Join-Path $installRoot 'acceptance-non-payload.txt'
$launcher = Join-Path $installRoot 'CPACloud.Launcher.exe'
$uninstaller = Join-Path $installRoot 'Uninstall.exe'
$assetsPath = Join-Path $installRoot 'web\assets'
$assetsBackup = Join-Path $installRoot 'web\assets.acceptance-real'
$launcherMutex = 'Local\CPACloud.Launcher'
$setupMutex = 'Local\CPACloud.Setup.2D7C1C6E-20A9-46F4-9955-24465BD75DCB'

try {
    New-Item -ItemType Directory -Path $dataRoot | Out-Null
    Set-Content -LiteralPath $dataSentinel -Value 'preserve-user-data' -Encoding ASCII

    $installExit = Invoke-BoundedProcess $installerPath '/S'
    Assert-Condition ($installExit -eq 0) "Silent install failed with exit code $installExit."
    foreach ($required in @($launcher, (Join-Path $installRoot 'cpa-cloud.exe'), (Join-Path $installRoot 'web\index.html'), $uninstaller, (Join-Path $installRoot '.cpa-cloud-install'))) {
        Assert-Condition (Test-Path -LiteralPath $required -PathType Leaf) "Installed payload is missing: $required"
    }
    Assert-Condition (Test-Path -LiteralPath (Join-Path $startMenuRoot 'CPA Cloud.lnk') -PathType Leaf) 'Start menu launcher shortcut is missing.'
    Assert-Condition (Test-Path -LiteralPath (Join-Path $startMenuRoot 'Uninstall CPA Cloud.lnk') -PathType Leaf) 'Start menu uninstall shortcut is missing.'
    Assert-Condition (-not (Test-Path -LiteralPath $desktopShortcut)) 'Optional desktop shortcut was created by default.'
    Assert-Condition ((Get-Content -LiteralPath $dataSentinel -Raw).Trim() -eq 'preserve-user-data') 'Install changed the user-data sentinel.'
    Assert-Condition ((Get-ItemProperty -LiteralPath $uninstallRegistry -Name DisplayVersion).DisplayVersion -eq $Version) 'Installed version registry value is incorrect.'

    Set-Content -LiteralPath $installSentinel -Value 'preserve-non-payload' -Encoding ASCII
    $reinstallExit = Invoke-BoundedProcess $installerPath '/S'
    Assert-Condition ($reinstallExit -eq 0) "Silent upgrade/reinstall failed with exit code $reinstallExit."
    Assert-Condition ((Get-Content -LiteralPath $dataSentinel -Raw).Trim() -eq 'preserve-user-data') 'Upgrade/reinstall changed the user-data sentinel.'
    Assert-Condition ((Get-Content -LiteralPath $installSentinel -Raw).Trim() -eq 'preserve-non-payload') 'Upgrade/reinstall removed the non-payload sentinel.'

    $launcherHashBeforeRefusal = (Get-FileHash -Algorithm SHA256 -LiteralPath $launcher).Hash
    $launcherRefusal = Invoke-WithNamedMutex $launcherMutex { Invoke-BoundedProcess $installerPath '/S' }
    Assert-Condition ($launcherRefusal -eq 10) "Installer did not return launcher-running exit code 10; got $launcherRefusal."
    Assert-Condition ((Get-FileHash -Algorithm SHA256 -LiteralPath $launcher).Hash -eq $launcherHashBeforeRefusal) 'Launcher-running refusal changed installed files.'

    $setupRefusal = Invoke-WithNamedMutex $setupMutex { Invoke-BoundedProcess $installerPath '/S' }
    Assert-Condition ($setupRefusal -eq 11) "Installer did not return setup-running exit code 11; got $setupRefusal."
    Assert-Condition ((Get-FileHash -Algorithm SHA256 -LiteralPath $launcher).Hash -eq $launcherHashBeforeRefusal) 'Setup-running refusal changed installed files.'

    Move-Item -LiteralPath $assetsPath -Destination $assetsBackup
    New-Item -ItemType Directory -Path $reparseTarget | Out-Null
    $reparseSentinel = Join-Path $reparseTarget 'outside-sentinel.txt'
    Set-Content -LiteralPath $reparseSentinel -Value 'do-not-traverse' -Encoding ASCII
    New-Item -ItemType Junction -Path $assetsPath -Target $reparseTarget | Out-Null
    $reparseRefusal = Invoke-BoundedProcess $installerPath '/S'
    Assert-Condition ($reparseRefusal -eq 14) "Installer did not return reparse-point exit code 14; got $reparseRefusal."
    $outsideEntries = @(Get-ChildItem -LiteralPath $reparseTarget -Force)
    Assert-Condition ($outsideEntries.Count -eq 1 -and $outsideEntries[0].FullName -eq $reparseSentinel) 'Installer traversed the reparse-point test target.'
    Remove-Item -LiteralPath $assetsPath -Force
    Move-Item -LiteralPath $assetsBackup -Destination $assetsPath

    # NSIS uninstallers normally bootstrap a temporary child, so the original
    # process cannot report the child's error level. Mirror NSIS's documented
    # copy process, then use final _?= to wait for the real process.
    $uninstallRefusal = Invoke-WithNamedMutex $launcherMutex { Invoke-RealNsisUninstaller $uninstaller $runnerTemp $installRoot }
    Assert-Condition ($uninstallRefusal -eq 10) "Uninstaller did not return launcher-running exit code 10; got $uninstallRefusal."
    Assert-Condition (Test-Path -LiteralPath $launcher -PathType Leaf) 'Refused uninstall removed the launcher.'

    $uninstallExit = Invoke-RealNsisUninstaller $uninstaller $runnerTemp $installRoot
    Assert-Condition ($uninstallExit -eq 0) "Silent uninstall failed with exit code $uninstallExit."
    Wait-PathAbsent $uninstaller
    foreach ($removed in @($launcher, (Join-Path $installRoot 'cpa-cloud.exe'), (Join-Path $installRoot 'web\index.html'), (Join-Path $installRoot '.cpa-cloud-install'), $startMenuRoot)) {
        Assert-Condition (-not (Test-Path -LiteralPath $removed)) "Uninstall left a managed payload path: $removed"
    }
    Assert-Condition (-not (Test-Path -LiteralPath $uninstallRegistry)) 'Uninstall registry key remains.'
    Assert-Condition (-not (Test-Path -LiteralPath $appRegistry)) 'Application registry key remains.'
    Assert-Condition ((Get-Content -LiteralPath $dataSentinel -Raw).Trim() -eq 'preserve-user-data') 'Uninstall removed or changed user data.'
    Assert-Condition ((Get-Content -LiteralPath $installSentinel -Raw).Trim() -eq 'preserve-non-payload') 'Uninstall removed the non-payload sentinel.'
    Write-Output 'Windows installer lifecycle acceptance passed.'
} finally {
    if ($testOwnsPaths) {
        $safeInstallCleanup = $true
        if (Test-Path -LiteralPath $assetsPath) {
            $assetsItem = Get-Item -LiteralPath $assetsPath -Force
            if (($assetsItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                Remove-Item -LiteralPath $assetsPath -Force -ErrorAction SilentlyContinue
                if (Test-Path -LiteralPath $assetsPath) {
                    $safeInstallCleanup = $false
                }
            }
        }
        if ((Test-Path -LiteralPath $assetsBackup) -and -not (Test-Path -LiteralPath $assetsPath)) {
            Move-Item -LiteralPath $assetsBackup -Destination $assetsPath -ErrorAction SilentlyContinue
        }
        if (Test-Path -LiteralPath $desktopShortcut) {
            Remove-Item -LiteralPath $desktopShortcut -Force -ErrorAction SilentlyContinue
        }
        foreach ($ownedCopy in @($script:OwnedUninstallerCopies.Keys)) {
            try {
                Remove-OwnedFileWithRetry $ownedCopy $script:OwnedUninstallerCopies[$ownedCopy]
                $script:OwnedUninstallerCopies.Remove($ownedCopy) | Out-Null
            } catch {
                Write-Warning $_
            }
        }
        foreach ($registryPath in @($uninstallRegistry, $appRegistry)) {
            if (Test-Path -LiteralPath $registryPath) {
                Remove-Item -LiteralPath $registryPath -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
        foreach ($path in @($startMenuRoot, $dataRoot, $reparseTarget)) {
            if (Test-Path -LiteralPath $path) {
                Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
        if ($safeInstallCleanup -and (Test-Path -LiteralPath $installRoot)) {
            Remove-Item -LiteralPath $installRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
        $dataParent = Split-Path -Parent $dataRoot
        if (Test-Path -LiteralPath $dataParent) {
            Remove-Item -LiteralPath $dataParent -ErrorAction SilentlyContinue
        }
    }
}
