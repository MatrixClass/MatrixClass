<#
.SYNOPSIS
  Cross-builds MatrixClass macOS binaries and Universal 2 binaries.
.DESCRIPTION
  Builds darwin/amd64 and darwin/arm64 with CGO_ENABLED=0 (works on Windows and
  Linux hosts) and combines them with tools/makefat, so lipo is not required.
#>
param(
    [string]$OutputDir = "dist/darwin",
    [string[]]$Architectures = @("amd64", "arm64"),
    [string]$Version = ""
)

. (Join-Path $PSScriptRoot "lib/common.ps1")

$Version = Get-MatrixClassVersion $Version
Assert-Architectures "darwin" $Architectures
$outputPath = Resolve-RepoPath $OutputDir
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

foreach ($arch in $Architectures) {
    Write-Host "Building darwin/$arch (CGO_ENABLED=0, tags=$script:BuildTags, version $Version)"
    foreach ($app in @("teacher", "student")) {
        Invoke-GoBuild -Goos darwin -Goarch $arch -App $app -Version $Version `
            -Output (Join-Path $outputPath "matrixclass-$app-darwin-$arch")
    }
}

$universal = ($Architectures -contains "amd64") -and ($Architectures -contains "arm64")
if ($universal) {
    $makefat = Get-HostTool "makefat"
    foreach ($app in @("teacher", "student")) {
        $out = Join-Path $outputPath "matrixclass-$app-darwin-universal"
        & $makefat -o $out (Join-Path $outputPath "matrixclass-$app-darwin-amd64") (Join-Path $outputPath "matrixclass-$app-darwin-arm64")
        if ($LASTEXITCODE -ne 0) { throw "makefat failed for $app" }
        & $makefat -info $out
    }
}

$archList = @($Architectures) + $(if ($universal) { @("universal") } else { @() })
Write-Utf8File (Join-Path $outputPath "build-info.txt") (@(
    "GOOS=darwin",
    "ARCHITECTURES=$($archList -join ',')",
    "VERSION=$Version",
    "CGO_ENABLED=0",
    "BUILD_TAGS=$script:BuildTags",
    "UNIVERSAL_TOOL=tools/makefat"
) -join "`n")
Write-Host "Darwin builds completed: $($archList -join ', ')"
