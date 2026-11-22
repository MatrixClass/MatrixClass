<#
.SYNOPSIS
  Creates macOS .app bundles (amd64, arm64, Universal 2) and zip packages.
.DESCRIPTION
  Works on Windows/Linux hosts: Universal 2 binaries come from tools/makefat and
  zips from tools/mkarchive (keeps the executable bit). -SignAdHoc and the DMG
  require macOS (codesign / hdiutil); on other hosts the DMG is skipped.
  Outputs under dist/darwin:
    apps/<arch>/MatrixClass Teacher.app, apps/<arch>/MatrixClass Student.app
    matrixclass-macos-{amd64,arm64,universal}.zip
    matrixclass-macos-universal.dmg (macOS only)
#>
param(
    [string]$OutputDir = "dist/darwin",
    [string]$Version = "",
    [switch]$SkipBuild,
    [switch]$SkipDmg,
    [switch]$SignAdHoc
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
$outputPath = Resolve-RepoPath $OutputDir
$packagingPath = Join-Path $script:RepoRoot "packaging/darwin"

if (-not $SkipBuild) {
    & (Join-Path $PSScriptRoot "build-darwin.ps1") -OutputDir $outputPath -Version $Version
}

$codesign = Get-Command codesign -ErrorAction SilentlyContinue
if ($SignAdHoc -and $null -eq $codesign) { throw "-SignAdHoc requires codesign (macOS)." }

$iconPath = Join-Path $packagingPath "MatrixClass.icns"
$template = [System.IO.File]::ReadAllText((Join-Path $packagingPath "Info.plist.in"))

function New-AppBundle([string]$Architecture, [string]$Role, [string]$DisplayName, [string]$BundleIdentifier) {
    $executable = "matrixclass-$Role"
    $appPath = Join-Path $outputPath "apps/$Architecture/$DisplayName.app"
    if (Test-Path -LiteralPath $appPath) { Remove-Item -LiteralPath $appPath -Recurse -Force }
    $contents = Join-Path $appPath "Contents"
    New-Item -ItemType Directory -Force -Path (Join-Path $contents "MacOS"), (Join-Path $contents "Resources") | Out-Null
    Copy-Item -LiteralPath (Join-Path $outputPath "matrixclass-$Role-darwin-$Architecture") (Join-Path $contents "MacOS/$executable")
    Copy-Item (Join-Path $packagingPath "README.md"), (Join-Path $packagingPath "permissions.md") (Join-Path $contents "Resources")
    $pngName = if ($Role -eq "teacher") { "MatrixClass Teacher.png" } else { "MatrixClass Student.png" }
    Copy-Item (Join-Path $packagingPath $pngName) (Join-Path $contents "Resources/$pngName")
    if (Test-Path $iconPath) { Copy-Item $iconPath (Join-Path $contents "Resources") }
    $iconEntry = "    <key>CFBundleIconFile</key><string>$pngName</string>`n"
    $plist = $template.Replace("@DISPLAY_NAME@", $DisplayName).Replace("@EXECUTABLE@", $executable).Replace("@BUNDLE_ID@", $BundleIdentifier).Replace("@VERSION@", $Version).Replace("@ICON_ENTRY@", $iconEntry)
    Write-Utf8File (Join-Path $contents "Info.plist") $plist
    Write-Utf8File (Join-Path $contents "PkgInfo") "APPL????"
    if (-not $script:OnWindows) { & chmod 0755 (Join-Path $contents "MacOS/$executable") }
    if ($SignAdHoc) {
        & $codesign.Source --force --deep -s - $appPath
        if ($LASTEXITCODE -ne 0) { throw "codesign failed for $appPath" }
        & $codesign.Source --verify --deep --strict $appPath
        if ($LASTEXITCODE -ne 0) { throw "codesign verification failed for $appPath" }
    }
    Write-Host "  app $appPath"
}

Copy-Item (Join-Path $packagingPath "README.md") (Join-Path $outputPath "README-macOS.md") -Force
Copy-Item (Join-Path $packagingPath "permissions.md") (Join-Path $outputPath "permissions-macOS.md") -Force

foreach ($arch in @("amd64", "arm64", "universal")) {
    if (-not (Test-Path (Join-Path $outputPath "matrixclass-teacher-darwin-$arch"))) {
        throw "Missing darwin/$arch binaries in $outputPath (run build-darwin.ps1 first)"
    }
    New-AppBundle $arch "teacher" "MatrixClass Teacher" "com.matrixclass.teacher"
    New-AppBundle $arch "student" "MatrixClass Student" "com.matrixclass.student"
    $stageName = "matrixclass-macos-$arch"
    $stage = Join-Path $outputPath $stageName
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    Copy-Item (Join-Path $outputPath "apps/$arch/*.app") $stage -Recurse
    Copy-Item (Join-Path $outputPath "README-macOS.md"), (Join-Path $outputPath "permissions-macOS.md"), (Join-Path $packagingPath "uninstall-student.sh") $stage
    New-ReleaseArchive -Output (Join-Path $outputPath "$stageName.zip") -BaseDir $outputPath -Entries @($stageName)
}

$hdiutil = Get-Command hdiutil -ErrorAction SilentlyContinue
if (-not $SkipDmg -and $null -ne $hdiutil) {
    $dmgRoot = Join-Path $outputPath "dmg"
    $dmg = Join-Path $outputPath "matrixclass-macos-universal.dmg"
    if (Test-Path $dmgRoot) { Remove-Item -Recurse -Force $dmgRoot }
    if (Test-Path $dmg) { Remove-Item -Force $dmg }
    New-Item -ItemType Directory -Force -Path $dmgRoot | Out-Null
    Copy-Item (Join-Path $outputPath "apps/universal/*.app") $dmgRoot -Recurse
    Copy-Item (Join-Path $outputPath "README-macOS.md"), (Join-Path $outputPath "permissions-macOS.md") $dmgRoot
    New-Item -ItemType SymbolicLink -Path (Join-Path $dmgRoot "Applications") -Target "/Applications" | Out-Null
    & $hdiutil.Source create -volname "MatrixClass $Version" -srcfolder $dmgRoot -ov -format UDZO $dmg
    if ($LASTEXITCODE -ne 0) { throw "hdiutil failed" }
    Write-Host "  dmg $dmg"
} else {
    Write-Host "hdiutil unavailable or -SkipDmg; skipping DMG (built only on macOS)."
}

Write-Host "Darwin packaging completed in $outputPath"
