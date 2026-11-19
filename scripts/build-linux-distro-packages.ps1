param(
    [string]$OutputDir = "dist/linux",
    [string]$Architecture = ""
)

$ErrorActionPreference = "Stop"
if (-not $Architecture) {
    $Architecture = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
}
if ($Architecture -notin @("amd64", "arm64", "loong64")) { throw "Unsupported architecture: $Architecture" }

if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Host "Skipping .deb/.rpm generation: WSL is unavailable on this host."
    exit 0
}

$script = (Resolve-Path (Join-Path $PSScriptRoot "build-linux-distro-packages.sh")).Path.Replace('\', '/')
$output = (Resolve-Path (Join-Path (Get-Location) $OutputDir)).Path.Replace('\', '/')
wsl sh -lc "cd '$((Get-Location).Path.Replace('\', '/'))' && sh '$script' '$output' '$Architecture'"
if ($LASTEXITCODE -ne 0) { throw "Linux distro package build failed" }
