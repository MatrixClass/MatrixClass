param(
    [string]$OutputDir = "dist/linux",
    [string]$Architecture = "",
    [string]$Version = ""
)

. (Join-Path $PSScriptRoot "lib/common.ps1")
$Version = Get-MatrixClassVersion $Version
if (-not $Architecture) {
    $Architecture = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
}
Assert-Architectures "linux" @($Architecture)

if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Host "Skipping .deb/.rpm generation: WSL is unavailable on this host."
    exit 0
}

$script = (Join-Path $PSScriptRoot "build-linux-distro-packages.sh").Replace('\', '/')
$output = (Resolve-RepoPath $OutputDir).Replace('\', '/')
$wslScript = (wsl wslpath -a "$script").Trim()
$wslOutput = (wsl wslpath -a "$output").Trim()
if ($LASTEXITCODE -ne 0 -or -not $wslOutput) { throw "wslpath failed; is a WSL distribution installed?" }
wsl env MATRIXCLASS_VERSION="$Version" sh "$wslScript" "$wslOutput" "$Architecture"
if ($LASTEXITCODE -ne 0) { throw "Linux distro package build failed" }
