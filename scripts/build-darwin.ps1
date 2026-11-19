param(
    [string]$OutputDir = "dist/darwin",
    [string[]]$Architectures = @("amd64", "arm64"),
    [string]$BuildTags = "mygo_nowebview"
)

$ErrorActionPreference = "Stop"
$validArchitectures = @("amd64", "arm64")
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$outputPath = if ([System.IO.Path]::IsPathRooted($OutputDir)) {
    [System.IO.Path]::GetFullPath($OutputDir)
} else {
    Join-Path $repoRoot $OutputDir
}

foreach ($architecture in $Architectures) {
    if ($validArchitectures -notcontains $architecture) {
        throw "Unsupported Darwin architecture '$architecture'. Use amd64 or arm64."
    }
}

New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousCgo = $env:CGO_ENABLED

try {
    foreach ($architecture in $Architectures) {
        $env:GOOS = "darwin"
        $env:GOARCH = $architecture
        $env:CGO_ENABLED = "0"
        $teacherOutput = Join-Path $outputPath "matrixclass-teacher-darwin-$architecture"
        $studentOutput = Join-Path $outputPath "matrixclass-student-darwin-$architecture"

        Write-Host "Building darwin/${architecture} (CGO_ENABLED=0, tags=$BuildTags)"
        go build -trimpath -tags $BuildTags -o $teacherOutput (Join-Path $repoRoot "cmd/teacher")
        if ($LASTEXITCODE -ne 0) { throw "teacher build failed for darwin/$architecture" }
        go build -trimpath -tags $BuildTags -o $studentOutput (Join-Path $repoRoot "cmd/student")
        if ($LASTEXITCODE -ne 0) { throw "student build failed for darwin/$architecture" }
    }

    $metadata = @(
        "GOOS=darwin",
        "ARCHITECTURES=$($Architectures -join ',')",
        "CGO_ENABLED=0",
        "BUILD_TAGS=$BuildTags",
        "NATIVE_APPLE_FRAMEWORKS=not included in cross-build"
    )
    Set-Content -Path (Join-Path $outputPath "build-info.txt") -Value $metadata -Encoding utf8
    Write-Host "Darwin builds completed: $($Architectures -join ', ')"
}
finally {
    if ($null -eq $previousGoos) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $previousGoos }
    if ($null -eq $previousGoarch) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $previousGoarch }
    if ($null -eq $previousCgo) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $previousCgo }
}
