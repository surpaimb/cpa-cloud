[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$InstallerChecksum,
    [Parameter(Mandatory = $true)][string]$SetupInstaller
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-Condition([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-ChildPath([string]$Path, [string]$Parent, [string]$Label) {
    $resolvedPath = [IO.Path]::GetFullPath($Path)
    $resolvedParent = [IO.Path]::GetFullPath($Parent).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($resolvedParent, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Unsafe $Label path: $resolvedPath"
    }
    return $resolvedPath
}

function Invoke-MsiExec([string]$Arguments, [int]$TimeoutSeconds = 180) {
    $process = Start-Process -FilePath "$env:SystemRoot\System32\msiexec.exe" -ArgumentList $Arguments -PassThru -WindowStyle Hidden
    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "msiexec timed out after $TimeoutSeconds seconds."
    }
    $process.Refresh()
    return $process.ExitCode
}

function Invoke-BoundedProcess([string]$FilePath, [string[]]$Arguments, [int]$TimeoutSeconds = 180) {
    $process = Start-Process -FilePath $FilePath -ArgumentList $Arguments -PassThru -WindowStyle Hidden
    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "$FilePath timed out after $TimeoutSeconds seconds."
    }
    $process.Refresh()
    return $process.ExitCode
}

if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
    throw 'This destructive MSI acceptance test is restricted to an ephemeral GitHub-hosted Actions runner.'
}

$installerPath = [IO.Path]::GetFullPath($Installer)
$checksumPath = [IO.Path]::GetFullPath($InstallerChecksum)
$setupPath = [IO.Path]::GetFullPath($SetupInstaller)
Assert-Condition (Test-Path -LiteralPath $installerPath -PathType Leaf) "MSI does not exist: $installerPath"
Assert-Condition (Test-Path -LiteralPath $checksumPath -PathType Leaf) "MSI checksum does not exist: $checksumPath"
Assert-Condition (Test-Path -LiteralPath $setupPath -PathType Leaf) "Setup installer does not exist: $setupPath"
$checksumLine = (Get-Content -LiteralPath $checksumPath -Raw).Trim()
$checksumMatch = [regex]::Match($checksumLine, '^([a-fA-F0-9]{64})\s{2}([^\r\n]+)$')
Assert-Condition $checksumMatch.Success 'MSI checksum has an unexpected format.'
Assert-Condition ($checksumMatch.Groups[2].Value -eq [IO.Path]::GetFileName($installerPath)) 'MSI checksum names a different file.'
Assert-Condition ((Get-FileHash -Algorithm SHA256 -LiteralPath $installerPath).Hash -eq $checksumMatch.Groups[1].Value) 'MSI checksum does not match.'

$localAppData = [IO.Path]::GetFullPath([Environment]::GetFolderPath('LocalApplicationData'))
$appData = [IO.Path]::GetFullPath([Environment]::GetFolderPath('ApplicationData'))
$desktop = [IO.Path]::GetFullPath([Environment]::GetFolderPath('DesktopDirectory'))
$installRoot = Assert-ChildPath (Join-Path $localAppData 'Programs\CPA Cloud') $localAppData 'install root'
$dataRoot = Assert-ChildPath (Join-Path $localAppData 'CPACloud\data') $localAppData 'data root'
$startMenuRoot = Assert-ChildPath (Join-Path $appData 'Microsoft\Windows\Start Menu\Programs\CPA Cloud') $appData 'Start menu root'
$desktopShortcut = Assert-ChildPath (Join-Path $desktop 'CPA Cloud.lnk') $desktop 'desktop shortcut'
$appRegistry = 'HKCU:\Software\CPACloud'
$nsisUninstallRegistry = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CPACloud.Launcher'

foreach ($path in @($installRoot, $dataRoot, $startMenuRoot, $desktopShortcut)) {
    Assert-Condition (-not (Test-Path -LiteralPath $path)) "Runner is not clean; refusing to use existing path: $path"
}
Assert-Condition (-not (Test-Path -LiteralPath $appRegistry)) "Runner is not clean; refusing to use existing registry key: $appRegistry"
Assert-Condition (-not (Test-Path -LiteralPath $nsisUninstallRegistry)) "Runner is not clean; refusing to use existing registry key: $nsisUninstallRegistry"

$dataSentinel = Join-Path $dataRoot 'msi-acceptance-user-data.txt'
$installSentinel = Join-Path $installRoot 'msi-acceptance-non-payload.txt'
$testOwnsPaths = $true
try {
    New-Item -ItemType Directory -Path $dataRoot | Out-Null
    Set-Content -LiteralPath $dataSentinel -Value 'preserve-user-data' -Encoding ASCII

    New-Item -ItemType Directory -Path $nsisUninstallRegistry -Force | Out-Null
    New-ItemProperty -LiteralPath $nsisUninstallRegistry -Name InstallLocation -Value $installRoot -PropertyType String | Out-Null
    $blockedMsiExit = Invoke-MsiExec "/i `"$installerPath`" /qn /norestart"
    Assert-Condition ($blockedMsiExit -eq 1603) "MSI did not reject the simulated Setup.exe installation; exit code was $blockedMsiExit."
    Assert-Condition (-not (Test-Path -LiteralPath $installRoot)) 'Blocked MSI created the installation directory.'
    Remove-Item -LiteralPath $nsisUninstallRegistry -Recurse -Force

    $installExit = Invoke-MsiExec "/i `"$installerPath`" /qn /norestart"
    Assert-Condition ($installExit -eq 0) "Silent MSI install failed with exit code $installExit."
    foreach ($required in @(
        (Join-Path $installRoot 'CPACloud.Launcher.exe'),
        (Join-Path $installRoot 'cpa-cloud.exe'),
        (Join-Path $installRoot 'web\index.html'),
        (Join-Path $startMenuRoot 'CPA Cloud.lnk'),
        (Join-Path $startMenuRoot 'Uninstall CPA Cloud.lnk')
    )) {
        Assert-Condition (Test-Path -LiteralPath $required -PathType Leaf) "MSI payload is missing: $required"
    }
    Assert-Condition (-not (Test-Path -LiteralPath $desktopShortcut)) 'MSI unexpectedly created a desktop shortcut.'
    Assert-Condition ((Get-Content -LiteralPath $dataSentinel -Raw).Trim() -eq 'preserve-user-data') 'MSI install changed user data.'

    $launcherHashBeforeBlockedSetup = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $installRoot 'CPACloud.Launcher.exe')).Hash
    $blockedSetupExit = Invoke-BoundedProcess $setupPath @('/S')
    Assert-Condition ($blockedSetupExit -eq 16) "Setup.exe did not reject the MSI-managed installation; exit code was $blockedSetupExit."
    Assert-Condition ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $installRoot 'CPACloud.Launcher.exe')).Hash -eq $launcherHashBeforeBlockedSetup) 'Blocked Setup.exe changed the MSI-managed launcher.'

    Set-Content -LiteralPath $installSentinel -Value 'preserve-non-payload' -Encoding ASCII
    $repairExit = Invoke-MsiExec "/i `"$installerPath`" /qn /norestart"
    Assert-Condition ($repairExit -eq 0) "Silent MSI reinstall failed with exit code $repairExit."
    Assert-Condition ((Get-Content -LiteralPath $installSentinel -Raw).Trim() -eq 'preserve-non-payload') 'MSI reinstall removed a non-payload file.'

    $uninstallExit = Invoke-MsiExec "/x `"$installerPath`" /qn /norestart"
    Assert-Condition ($uninstallExit -eq 0) "Silent MSI uninstall failed with exit code $uninstallExit."
    foreach ($removed in @(
        (Join-Path $installRoot 'CPACloud.Launcher.exe'),
        (Join-Path $installRoot 'cpa-cloud.exe'),
        (Join-Path $installRoot 'web\index.html'),
        $startMenuRoot
    )) {
        Assert-Condition (-not (Test-Path -LiteralPath $removed)) "MSI uninstall left a managed path: $removed"
    }
    Assert-Condition ((Get-Content -LiteralPath $dataSentinel -Raw).Trim() -eq 'preserve-user-data') 'MSI uninstall removed or changed user data.'
    Assert-Condition ((Get-Content -LiteralPath $installSentinel -Raw).Trim() -eq 'preserve-non-payload') 'MSI uninstall removed the non-payload sentinel.'
    Write-Output 'Windows MSI lifecycle acceptance passed.'
} finally {
    if ($testOwnsPaths) {
        if (Test-Path -LiteralPath $desktopShortcut) {
            Remove-Item -LiteralPath $desktopShortcut -Force -ErrorAction SilentlyContinue
        }
        if (Test-Path -LiteralPath $appRegistry) {
            Remove-Item -LiteralPath $appRegistry -Recurse -Force -ErrorAction SilentlyContinue
        }
        if (Test-Path -LiteralPath $nsisUninstallRegistry) {
            Remove-Item -LiteralPath $nsisUninstallRegistry -Recurse -Force -ErrorAction SilentlyContinue
        }
        foreach ($path in @($startMenuRoot, $dataRoot, $installRoot)) {
            if (Test-Path -LiteralPath $path) {
                Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction SilentlyContinue
            }
        }
        $dataParent = Split-Path -Parent $dataRoot
        if (Test-Path -LiteralPath $dataParent) {
            Remove-Item -LiteralPath $dataParent -ErrorAction SilentlyContinue
        }
    }
}
