<#
.SYNOPSIS
  Builds Linux binaries and matrixclass-linux-<arch> directory + tar.gz packages.
.DESCRIPTION
  Archives are written with tools/mkarchive so binaries and install scripts keep
  their executable bit even when packaged on Windows.
#>
param(
    [string]$OutputDir = "dist/linux",
    [string[]]$Architectures = @("amd64", "arm64"),
    [string]$Version = "",
    [switch]$SkipBuild
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
$outputPath = Resolve-RepoPath $OutputDir
if (-not $SkipBuild) {
    & (Join-Path $PSScriptRoot "build-linux.ps1") -OutputDir $outputPath -Architectures $Architectures -Version $Version
}

$packaging = Join-Path $script:RepoRoot "packaging/linux"
foreach ($arch in $Architectures) {
    $stageName = "matrixclass-linux-$arch"
    $stage = Join-Path $outputPath $stageName
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    foreach ($app in @("teacher", "student")) {
        Copy-Item (Join-Path $outputPath "matrixclass-$app-linux-$arch") $stage
    }
    foreach ($f in @("matrixclass-teacher.desktop", "matrixclass-student.desktop", "matrixclass.svg",
            "matrixclass-teacher.service", "matrixclass-student.service", "install.sh", "uninstall.sh", "README.md")) {
        Copy-Item (Join-Path $packaging $f) $stage
    }
    New-ReleaseArchive -Output (Join-Path $outputPath "$stageName.tar.gz") -BaseDir $outputPath -Entries @($stageName)
}

Write-Host "Linux packages completed: $($Architectures -join ', ')"
Write-Host "For .deb/.rpm run scripts/build-linux-distro-packages.sh on Linux (or build-linux-distro-packages.ps1 via WSL)."
