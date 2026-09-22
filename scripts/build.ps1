param(
    [string]$GoExecutable = 'go',
    [string]$BunExecutable = 'bun',
    [ValidateSet('windows','linux','darwin')][string]$TargetOS = 'windows',
    [ValidateSet('amd64','arm64')][string]$TargetArch = 'amd64',
    [switch]$SkipWeb
)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if (-not (Test-Path -LiteralPath (Join-Path $projectRoot 'go.mod'))) {
    throw 'Go service implementation is not present yet.'
}
Push-Location $projectRoot
$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH
try {
    if (-not $SkipWeb) {
        Push-Location (Join-Path $projectRoot 'web')
        try {
            & $BunExecutable run build
            if ($LASTEXITCODE -ne 0) { throw 'Web build failed.' }
        } finally { Pop-Location }
    }
    $env:GOOS = $TargetOS
    $env:GOARCH = $TargetArch
    $extension = if ($TargetOS -eq 'windows') { '.exe' } else { '' }
    $outputDirectory = Join-Path $projectRoot "dist/$TargetOS-$TargetArch"
    New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
    & $GoExecutable build -trimpath -o (Join-Path $outputDirectory "cpa-cloud$extension") ./cmd/cpa-cloud
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
    if (-not $SkipWeb) {
        $webOutput = Join-Path $projectRoot 'web/dist'
        if (-not (Test-Path -LiteralPath $webOutput)) { throw 'Web output missing.' }
        Copy-Item -LiteralPath $webOutput -Destination (Join-Path $outputDirectory 'web') -Recurse -Force
    }
    Write-Output "Build output: $outputDirectory"
} finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
    Pop-Location
}
