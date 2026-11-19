param(
    [string]$OutputDir = "dist/linux"
)

$ErrorActionPreference = "Stop"
$architectures = @("amd64", "arm64", "loong64")
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

foreach ($arch in $architectures) {
    $env:GOOS = "linux"
    $env:GOARCH = $arch
    $env:CGO_ENABLED = "0"
    go build -tags mygo_nowebview -o "$OutputDir/matrixclass-teacher-linux-$arch" ./cmd/teacher
    if ($LASTEXITCODE -ne 0) { throw "teacher build failed for linux/$arch" }
    go build -tags mygo_nowebview -o "$OutputDir/matrixclass-student-linux-$arch" ./cmd/student
    if ($LASTEXITCODE -ne 0) { throw "student build failed for linux/$arch" }
}

Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "Linux builds completed: $($architectures -join ', ')"
