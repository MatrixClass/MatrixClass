<#
.SYNOPSIS
  Builds MatrixClass Windows GUI executables and zip packages.
.DESCRIPTION
  Produces, for each architecture, under dist/windows:
    matrixclass-teacher-windows-<arch>.exe, matrixclass-student-windows-<arch>.exe
    matrixclass-windows-<arch>.zip (matrixclass-teacher.exe, matrixclass-student.exe, README.md)
  Binaries are linked with -H windowsgui (no console window), -trimpath, -s -w
  and -X main.version=<Version>.
.EXAMPLE
  ./scripts/build-windows.ps1
  ./scripts/build-windows.ps1 -Architectures amd64 -Version 1.2.3 -SkipPackage
#>
param(
    [string]$OutputDir = "dist/windows",
    [string[]]$Architectures = @("amd64", "arm64"),
    [string]$Version = "",
    [switch]$SkipPackage
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
Assert-Architectures "windows" $Architectures
$outputPath = Resolve-RepoPath $OutputDir
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

foreach ($arch in $Architectures) {
    Write-Host "Building windows/$arch (version $Version)"
    foreach ($app in @("teacher", "student")) {
        Invoke-GoBuild -Goos windows -Goarch $arch -App $app -Version $Version `
            -Output (Join-Path $outputPath "matrixclass-$app-windows-$arch.exe") -ExtraLdflags @("-H", "windowsgui")
    }
    if ($SkipPackage) { continue }

    $stageName = "matrixclass-windows-$arch"
    $stage = Join-Path $outputPath $stageName
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    foreach ($app in @("teacher", "student")) {
        Copy-Item (Join-Path $outputPath "matrixclass-$app-windows-$arch.exe") (Join-Path $stage "matrixclass-$app.exe")
    }
    Copy-Item (Join-Path $script:RepoRoot "packaging/windows/README.md") (Join-Path $stage "README.md")
    New-ReleaseArchive -Output (Join-Path $outputPath "$stageName.zip") -BaseDir $outputPath -Entries @($stageName)
}

Write-Host "Windows builds completed: $($Architectures -join ', ') -> $outputPath"
