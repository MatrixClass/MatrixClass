param(
    [string]$OutputDir = "dist/darwin",
    [switch]$SkipBuild,
    [switch]$SkipDmg,
    [string]$Version = "0.1.0"
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$outputPath = if ([System.IO.Path]::IsPathRooted($OutputDir)) {
    [System.IO.Path]::GetFullPath($OutputDir)
} else {
    Join-Path $repoRoot $OutputDir
}
$packagingPath = Join-Path $repoRoot "packaging/darwin"

if (-not $SkipBuild) {
    & (Join-Path $PSScriptRoot "build-darwin.ps1") -OutputDir $OutputDir
}

function New-AppBundle {
    param(
        [string]$Architecture,
        [string]$Role,
        [string]$DisplayName,
        [string]$Executable,
        [string]$BundleIdentifier
    )

    $stageRoot = Join-Path $outputPath "apps/$Architecture"
    $appPath = Join-Path $stageRoot "$DisplayName.app"
    $contentsPath = Join-Path $appPath "Contents"
    $macosPath = Join-Path $contentsPath "MacOS"
    $resourcesPath = Join-Path $contentsPath "Resources"
    New-Item -ItemType Directory -Force -Path $macosPath, $resourcesPath | Out-Null

    Copy-Item (Join-Path $outputPath $Executable) (Join-Path $macosPath $Role) -Force
    Copy-Item (Join-Path $packagingPath "README.md") (Join-Path $resourcesPath "README.md") -Force
    Copy-Item (Join-Path $packagingPath "permissions.md") (Join-Path $resourcesPath "permissions.md") -Force

    $plist = @"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDisplayName</key><string>$DisplayName</string>
    <key>CFBundleExecutable</key><string>$Role</string>
    <key>CFBundleIdentifier</key><string>$BundleIdentifier</string>
    <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
    <key>CFBundleName</key><string>$DisplayName</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>$Version</string>
    <key>CFBundleVersion</key><string>$Version</string>
    <key>LSMinimumSystemVersion</key><string>12.3</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSMicrophoneUsageDescription</key><string>MatrixClass 使用麦克风进行课堂音频通信。</string>
    <key>NSScreenCaptureUsageDescription</key><string>MatrixClass 使用屏幕录制权限进行课堂屏幕广播和监控。</string>
</dict>
</plist>
"@
    Set-Content -Path (Join-Path $contentsPath "Info.plist") -Value $plist -Encoding utf8
    return $appPath
}

$architectures = @("amd64", "arm64")
foreach ($architecture in $architectures) {
    $teacher = New-AppBundle $architecture "matrixclass-teacher" "MatrixClass Teacher" "matrixclass-teacher-darwin-$architecture" "com.matrixclass.teacher"
    $student = New-AppBundle $architecture "matrixclass-student" "MatrixClass Student" "matrixclass-student-darwin-$architecture" "com.matrixclass.student"
    Write-Host "Created unsigned apps for darwin/${architecture}: $teacher; $student"
}

$universalCreated = $false
$lipo = Get-Command lipo -ErrorAction SilentlyContinue
if ($null -ne $lipo) {
    $universalRoot = Join-Path $outputPath "apps/universal"
    New-Item -ItemType Directory -Force -Path $universalRoot | Out-Null
    foreach ($role in @("teacher", "student")) {
        $displayName = if ($role -eq "teacher") { "MatrixClass Teacher" } else { "MatrixClass Student" }
        $executable = "matrixclass-$role"
        $appPath = Join-Path $universalRoot "$displayName.app"
        $contentsPath = Join-Path $appPath "Contents"
        $macosPath = Join-Path $contentsPath "MacOS"
        $resourcesPath = Join-Path $contentsPath "Resources"
        New-Item -ItemType Directory -Force -Path $macosPath, $resourcesPath | Out-Null
        & $lipo.Source -create (Join-Path $outputPath "matrixclass-$role-darwin-amd64") (Join-Path $outputPath "matrixclass-$role-darwin-arm64") -output (Join-Path $macosPath $executable)
        if ($LASTEXITCODE -ne 0) { throw "lipo failed for $role" }
        $sourceApp = Join-Path $outputPath "apps/amd64/$displayName.app/Contents"
        Copy-Item (Join-Path $sourceApp "Info.plist") (Join-Path $contentsPath "Info.plist") -Force
        Copy-Item (Join-Path $sourceApp "Resources/*") $resourcesPath -Force
    }
    $universalCreated = $true
    Write-Host "Created unsigned Universal 2 apps."
} else {
    Write-Host "lipo not found; skipping Universal 2 apps. Run this step on macOS or with lipo available."
}

$readme = Join-Path $packagingPath "README.md"
Copy-Item $readme (Join-Path $outputPath "README-macOS.md") -Force
Copy-Item (Join-Path $packagingPath "permissions.md") (Join-Path $outputPath "permissions-macOS.md") -Force

if (-not $SkipDmg) {
    $hdiutil = Get-Command hdiutil -ErrorAction SilentlyContinue
    if ($null -ne $hdiutil -and $universalCreated) {
        $dmgRoot = Join-Path $outputPath "dmg"
        if (Test-Path $dmgRoot) { Remove-Item -Recurse -Force $dmgRoot }
        New-Item -ItemType Directory -Force -Path $dmgRoot | Out-Null
        Copy-Item (Join-Path $outputPath "apps/universal/*") $dmgRoot -Recurse -Force
        Copy-Item (Join-Path $outputPath "README-macOS.md") $dmgRoot -Force
        Copy-Item (Join-Path $outputPath "permissions-macOS.md") $dmgRoot -Force
        & $hdiutil.Source create -volname "MatrixClass" -srcfolder $dmgRoot -ov -format UDZO (Join-Path $outputPath "MatrixClass-macOS-unsigned.dmg")
        if ($LASTEXITCODE -ne 0) { throw "hdiutil failed" }
        Write-Host "Created unsigned DMG."
    } else {
        Write-Host "hdiutil or Universal 2 apps unavailable; skipping DMG."
    }
}

Write-Host "Darwin packaging completed in $outputPath"
