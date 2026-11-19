param(
    [string]$InputDir = "dist/darwin",
    [switch]$RequireUniversal,
    [switch]$RequireDmg
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$inputPath = if ([System.IO.Path]::IsPathRooted($InputDir)) { [System.IO.Path]::GetFullPath($InputDir) } else { Join-Path $repoRoot $InputDir }
if (-not (Test-Path -LiteralPath $inputPath -PathType Container)) { throw "Darwin build directory not found: $inputPath" }

$expected = @(
    "matrixclass-teacher-darwin-amd64",
    "matrixclass-student-darwin-amd64",
    "matrixclass-teacher-darwin-arm64",
    "matrixclass-student-darwin-arm64"
)
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

$universalApps = @(
    (Join-Path $inputPath "apps/universal/MatrixClass Teacher.app/Contents/MacOS/matrixclass-teacher"),
    (Join-Path $inputPath "apps/universal/MatrixClass Student.app/Contents/MacOS/matrixclass-student")
)
$hasUniversal = ($universalApps | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }).Count -eq $universalApps.Count
if ($RequireUniversal -and -not $hasUniversal) { throw "Universal 2 app binaries are required but were not found under $inputPath/apps/universal" }

$dmgPath = Join-Path $inputPath "MatrixClass-macOS-unsigned.dmg"
if ($RequireDmg -and -not (Test-Path -LiteralPath $dmgPath -PathType Leaf)) { throw "Unsigned DMG is required but was not found: $dmgPath" }

$checksumPath = Join-Path $inputPath "SHA256SUMS.txt"
$records | ForEach-Object { "{0}  {1}" -f $_.SHA256, $_.File } | Set-Content -LiteralPath $checksumPath -Encoding utf8
Write-Host "Darwin artifact validation passed: $inputPath"
$records | Format-Table -AutoSize
Write-Host "SHA-256 manifest: $checksumPath"
if ($hasUniversal) { Write-Host "Universal 2 app binaries: present" } else { Write-Host "Universal 2 app binaries: not present (run on macOS with lipo)" }
if (Test-Path -LiteralPath $dmgPath -PathType Leaf) { Write-Host "Unsigned DMG: present" } else { Write-Host "Unsigned DMG: not present (run on macOS with hdiutil)" }
