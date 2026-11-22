<#
.SYNOPSIS
  Cross-builds MatrixClass Linux binaries (CGO_ENABLED=0).
.DESCRIPTION
  Default architectures are the ones with a native mygo UI backend (amd64, arm64).
  loong64, riscv64, 386 and arm (GOARM=7) are accepted as compile-only targets:
  they build, but mygo reports "not supported yet" at startup.
#>
param(
    [string]$OutputDir = "dist/linux",
    [string[]]$Architectures = @("amd64", "arm64"),
    [string]$Version = ""
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
Assert-Architectures "linux" $Architectures
$outputPath = Resolve-RepoPath $OutputDir
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

foreach ($arch in $Architectures) {
    Write-Host "Building linux/$arch (version $Version)"
    foreach ($app in @("teacher", "student")) {
        Invoke-GoBuild -Goos linux -Goarch $arch -App $app -Version $Version `
            -Output (Join-Path $outputPath "matrixclass-$app-linux-$arch")
    }
}

Write-Host "Linux builds completed: $($Architectures -join ', ')"
