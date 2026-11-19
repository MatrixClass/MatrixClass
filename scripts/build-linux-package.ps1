param(
    [string]$OutputDir = "dist/linux"
)

$ErrorActionPreference = "Stop"
$architectures = @("amd64", "arm64", "loong64")
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

& (Join-Path $PSScriptRoot "build-linux.ps1") -OutputDir $OutputDir

foreach ($arch in $architectures) {
    $stage = Join-Path $OutputDir "matrixclass-linux-$arch"
    $archive = Join-Path $OutputDir "matrixclass-linux-$arch.tar.gz"
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    if (Test-Path $archive) { Remove-Item -Force $archive }
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    Copy-Item (Join-Path $OutputDir "matrixclass-teacher-linux-$arch") (Join-Path $stage "matrixclass-teacher-linux-$arch")
    Copy-Item (Join-Path $OutputDir "matrixclass-student-linux-$arch") (Join-Path $stage "matrixclass-student-linux-$arch")
    Copy-Item (Join-Path $repoRoot "packaging/linux/matrixclass-teacher.desktop") $stage
    Copy-Item (Join-Path $repoRoot "packaging/linux/matrixclass-student.desktop") $stage
    Copy-Item (Join-Path $repoRoot "packaging/linux/matrixclass.svg") $stage
    Copy-Item (Join-Path $repoRoot "packaging/linux/install.sh") $stage
    Copy-Item (Join-Path $repoRoot "packaging/linux/uninstall.sh") $stage
    Copy-Item (Join-Path $repoRoot "packaging/linux/README.md") $stage
    tar -czf $archive -C $OutputDir "matrixclass-linux-$arch"
    if ($LASTEXITCODE -ne 0) { throw "archive failed for $arch" }
}

Write-Host "Linux packages completed: $($architectures -join ', ')"
