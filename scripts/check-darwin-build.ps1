param(
    [string]$InputDir = "dist/darwin",
    [switch]$RequireUniversal,
    [switch]$RequireDmg
)

. (Join-Path $PSScriptRoot "lib/common.ps1")
$inputPath = Resolve-RepoPath $InputDir
if (-not (Test-Path -LiteralPath $inputPath -PathType Container)) { throw "Darwin build directory not found: $inputPath" }

$expected = @(
    "matrixclass-teacher-darwin-amd64",
    "matrixclass-student-darwin-amd64",
    "matrixclass-teacher-darwin-arm64",
    "matrixclass-student-darwin-arm64"
)
$universalBins = @("matrixclass-teacher-darwin-universal", "matrixclass-student-darwin-universal")
$hasUniversal = ($universalBins | Where-Object { Test-Path -LiteralPath (Join-Path $inputPath $_) -PathType Leaf }).Count -eq $universalBins.Count
if ($RequireUniversal -and -not $hasUniversal) { throw "Universal 2 binaries are required but were not found under $inputPath" }
if ($hasUniversal) { $expected += $universalBins }

$records = @()
foreach ($name in $expected) {
    $path = Join-Path $inputPath $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing Darwin artifact: $path" }
    $records += [PSCustomObject]@{
        File = $name
        SHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        Size = (Get-Item -LiteralPath $path).Length
    }
}

if ($hasUniversal) {
    $makefat = Get-HostTool "makefat"
    foreach ($name in $universalBins) {
        $info = & $makefat -info (Join-Path $inputPath $name)
        if ($LASTEXITCODE -ne 0) { throw "makefat -info failed for $name" }
        $text = $info -join "`n"
        if ($text -notmatch "CpuAmd64" -or $text -notmatch "CpuArm64") { throw "$name is not a Universal 2 (amd64+arm64) binary:`n$text" }
        Write-Host $text
    }
    foreach ($app in @("MatrixClass Teacher", "MatrixClass Student")) {
        $plist = Join-Path $inputPath "apps/universal/$app.app/Contents/Info.plist"
        if (Test-Path -LiteralPath $plist) {
            $xml = [xml](Get-Content -LiteralPath $plist -Raw)
            $keys = $xml.plist.dict.key
            foreach ($k in @("CFBundleIdentifier", "CFBundleExecutable", "LSMinimumSystemVersion", "NSScreenCaptureUsageDescription", "NSMicrophoneUsageDescription", "NSAppleEventsUsageDescription")) {
                if ($keys -notcontains $k) { throw "$plist is missing $k" }
            }
        }
    }
}

$dmgPath = Join-Path $inputPath "matrixclass-macos-universal.dmg"
if ($RequireDmg -and -not (Test-Path -LiteralPath $dmgPath -PathType Leaf)) { throw "DMG is required but was not found: $dmgPath" }

$checksumPath = Join-Path $inputPath "SHA256SUMS.txt"
Write-Utf8File $checksumPath ((($records | ForEach-Object { "{0}  {1}" -f $_.SHA256, $_.File }) -join "`n") + "`n")
Write-Host "Darwin artifact validation passed: $inputPath"
$records | Format-Table -AutoSize
Write-Host "SHA-256 manifest: $checksumPath"
if ($hasUniversal) { Write-Host "Universal 2 binaries: present (verified with tools/makefat)" } else { Write-Host "Universal 2 binaries: not present (run build-darwin.ps1)" }
if (Test-Path -LiteralPath $dmgPath -PathType Leaf) { Write-Host "DMG: present" } else { Write-Host "DMG: not present (built only on macOS with hdiutil)" }
