<#
.SYNOPSIS
  Builds and packages every supported MatrixClass target into dist/.
.DESCRIPTION
  Release targets (native mygo UI backend):
    windows/amd64, windows/arm64     -> dist/windows/matrixclass-windows-<arch>.zip
    linux/amd64,   linux/arm64       -> dist/linux/matrixclass-linux-<arch>.tar.gz (+ .deb/.rpm with -DistroPackages)
    darwin/amd64,  darwin/arm64, universal -> dist/darwin/matrixclass-macos-<arch>.zip (+ .dmg on macOS)
  All release files are copied to dist/release together with SHA256SUMS.

  -IncludeCompileOnly additionally cross-compiles windows/386 and
  linux/{loong64,riscv64,386,arm} into dist/compile-only. Those binaries build
  but mygo has no UI backend for them, so they are never added to dist/release.
.EXAMPLE
  ./scripts/build-all.ps1 -Version 1.0.0
#>
param(
    [string]$OutputDir = "dist",
    [string]$Version = "",
    [switch]$DistroPackages,
    [switch]$SignAdHoc,
    [switch]$IncludeCompileOnly
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
$dist = Resolve-RepoPath $OutputDir
$release = Join-Path $dist "release"
$started = Get-Date
Write-Host "MatrixClass build-all: version $Version -> $dist"

& (Join-Path $PSScriptRoot "build-windows.ps1") -OutputDir (Join-Path $dist "windows") -Architectures $script:SupportedTargets.windows -Version $Version
& (Join-Path $PSScriptRoot "build-linux-package.ps1") -OutputDir (Join-Path $dist "linux") -Architectures $script:SupportedTargets.linux -Version $Version
if ($DistroPackages) {
    foreach ($arch in $script:SupportedTargets.linux) {
        & (Join-Path $PSScriptRoot "build-linux-distro-packages.ps1") -OutputDir (Join-Path $dist "linux") -Architecture $arch -Version $Version
    }
}
& (Join-Path $PSScriptRoot "build-darwin-package.ps1") -OutputDir (Join-Path $dist "darwin") -Version $Version -SignAdHoc:$SignAdHoc

if ($IncludeCompileOnly) {
    $co = Join-Path $dist "compile-only"
    New-Item -ItemType Directory -Force -Path $co | Out-Null
    foreach ($goos in @("windows", "linux")) {
        foreach ($arch in $script:CompileOnlyTargets[$goos]) {
            $ext = if ($goos -eq "windows") { ".exe" } else { "" }
            $extra = if ($goos -eq "windows") { @("-H", "windowsgui") } else { @() }
            foreach ($app in @("teacher", "student")) {
                Invoke-GoBuild -Goos $goos -Goarch $arch -App $app -Version $Version -ExtraLdflags $extra `
                    -Output (Join-Path $co "matrixclass-$app-$goos-$arch$ext")
            }
        }
    }
    Write-Host "Compile-only targets built in $co (not released: mygo reports 'not supported yet' at startup)."
}

if (Test-Path $release) { Remove-Item -Recurse -Force $release }
New-Item -ItemType Directory -Force -Path $release | Out-Null
$files = @()
foreach ($arch in $script:SupportedTargets.windows) { $files += Join-Path $dist "windows/matrixclass-windows-$arch.zip" }
foreach ($arch in $script:SupportedTargets.linux) { $files += Join-Path $dist "linux/matrixclass-linux-$arch.tar.gz" }
foreach ($arch in @("amd64", "arm64", "universal")) { $files += Join-Path $dist "darwin/matrixclass-macos-$arch.zip" }
foreach ($f in $files) {
    if (-not (Test-Path -LiteralPath $f)) { throw "Expected release artifact missing: $f" }
    Copy-Item -LiteralPath $f $release
}
$optional = @(Get-ChildItem -Path (Join-Path $dist "linux") -File -Filter "matrixclass-$Version-*.deb" -ErrorAction SilentlyContinue) +
    @(Get-ChildItem -Path (Join-Path $dist "linux") -File -Filter "matrixclass-$Version-1.*.rpm" -ErrorAction SilentlyContinue) +
    @(Get-ChildItem -Path (Join-Path $dist "darwin") -File -Filter "matrixclass-macos-universal.dmg" -ErrorAction SilentlyContinue)
foreach ($f in $optional) { Copy-Item -LiteralPath $f.FullName $release }

Write-Sha256Sums $release
Write-Host ""
Write-Host "Release artifacts ($([int]((Get-Date) - $started).TotalSeconds)s):"
Get-ChildItem -LiteralPath $release -File | Sort-Object Name | ForEach-Object {
    Write-Host ("  {0,-45} {1,12:N0} bytes" -f $_.Name, $_.Length)
}
