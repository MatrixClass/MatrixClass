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
    [switch]$SkipPackage,
    [string]$NsisPath = ""
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

function Resolve-NsisCompiler([string]$Path) {
    if ($Path) {
        return (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    }
    $command = Get-Command makensis.exe -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    $cached = Get-ChildItem "$env:LOCALAPPDATA\electron-builder\Cache\nsis" -Filter makensis.exe -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($cached) { return $cached.FullName }
    throw "makensis.exe was not found. Install NSIS or pass -NsisPath <path>."
}

$Version = Get-MatrixClassVersion $Version
Assert-Architectures "windows" $Architectures
$outputPath = Resolve-RepoPath $OutputDir
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
$nsis = if ($SkipPackage) { $null } else { Resolve-NsisCompiler $NsisPath }

foreach ($arch in $Architectures) {
    Write-Host "Building windows/$arch (version $Version)"
    foreach ($app in @("teacher", "student")) {
        $iconPng = Join-Path $script:RepoRoot "packaging/windows/matrixclass-$app.png"
        $iconIco = Join-Path $outputPath "matrixclass-$app-windows-$arch.ico"
        $iconSyso = Join-Path $script:RepoRoot "cmd/$app/rsrc_windows_$arch.syso"
        $iconTool = Get-HostTool "pngico"
        & $iconTool -in $iconPng -out $iconIco
        if ($LASTEXITCODE -ne 0) { throw "icon conversion failed for windows/$arch/$app" }
        & go run github.com/akavel/rsrc@v0.10.2 -arch $arch -ico $iconIco -o $iconSyso
        if ($LASTEXITCODE -ne 0) { throw "PE icon resource generation failed for windows/$arch/$app" }
        try {
            Invoke-GoBuild -Goos windows -Goarch $arch -App $app -Version $Version `
                -Output (Join-Path $outputPath "matrixclass-$app-windows-$arch.exe") -ExtraLdflags @("-H", "windowsgui")
        }
        finally {
            if (Test-Path -LiteralPath $iconSyso) { Remove-Item -LiteralPath $iconSyso -Force }
        }
    }
    if ($SkipPackage) { continue }

    $stageName = "matrixclass-windows-$arch"
    $stage = Join-Path $outputPath $stageName
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    foreach ($app in @("teacher", "student")) {
        Copy-Item (Join-Path $outputPath "matrixclass-$app-windows-$arch.exe") (Join-Path $stage "matrixclass-$app.exe")
        Copy-Item (Join-Path $script:RepoRoot "packaging/windows/matrixclass-$app.png") (Join-Path $stage "matrixclass-$app.png")
        $ico = Join-Path $stage "matrixclass-$app.ico"
        $png = Join-Path $stage "matrixclass-$app.png"
        $tool = Get-HostTool "pngico"
        & $tool -in $png -out $ico
        if ($LASTEXITCODE -ne 0) { throw "icon generation failed for windows/$arch/$app" }
    }
    Copy-Item (Join-Path $script:RepoRoot "packaging/windows/README.md") (Join-Path $stage "README.md")
    New-ReleaseArchive -Output (Join-Path $outputPath "$stageName.zip") -BaseDir $outputPath -Entries @($stageName)
    foreach ($app in @("teacher", "student")) {
        $installer = Join-Path $outputPath "matrixclass-$app-windows-$arch-$Version-setup.exe"
        & $nsis "/DAPP_VERSION=$Version" "/DAPP_ARCH=$arch" "/DAPP_ROLE=$app" "/DSTAGE_DIR=$stage" "/DOUT_FILE=$installer" (Join-Path $script:RepoRoot "packaging/windows/matrixclass-role.nsi")
        if ($LASTEXITCODE -ne 0) { throw "NSIS installer build failed for windows/$arch/$app" }
        Write-Host "  installer $installer"
    }
}

Write-Host "Windows builds completed: $($Architectures -join ', ') -> $outputPath"
