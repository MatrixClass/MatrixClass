# Shared helpers for MatrixClass build scripts. Dot-source from scripts/*.ps1:
#   . (Join-Path $PSScriptRoot "lib/common.ps1")

$ErrorActionPreference = "Stop"
# $PSScriptRoot is scripts/lib when this file is dot-sourced.
$script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$script:BuildTags = "nexlab_nowebview"
$script:OnWindows = ($env:OS -eq "Windows_NT")

# Targets with a native mygo backend (UI starts). Everything else is compile-only.
$script:SupportedTargets = @{
    windows = @("amd64", "arm64")
    linux   = @("amd64", "arm64", "loong64")
    darwin  = @("amd64", "arm64")
}
$script:CompileOnlyTargets = @{
    windows = @("386")
    linux   = @("riscv64", "386", "arm")
    darwin  = @()
}

function Resolve-RepoPath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }
    return [System.IO.Path]::GetFullPath((Join-Path $script:RepoRoot $Path))
}

function Get-MatrixClassVersion([string]$Version) {
    if ($Version) { return $Version.TrimStart("v") }
    if ($env:MATRIXCLASS_VERSION) { return $env:MATRIXCLASS_VERSION.TrimStart("v") }
    return "0.1.0"
}

function Assert-Architectures([string]$Goos, [string[]]$Architectures) {
    foreach ($arch in $Architectures) {
        if ($script:SupportedTargets[$Goos] -contains $arch) { continue }
        if ($script:CompileOnlyTargets[$Goos] -contains $arch) {
            Write-Warning "$Goos/$arch is compile-only: mygo has no native UI backend for it, the app exits with 'not supported yet'. Not for release."
            continue
        }
        throw "Unsupported $Goos architecture '$arch'. Supported: $($script:SupportedTargets[$Goos] -join ', '); compile-only: $($script:CompileOnlyTargets[$Goos] -join ', ')"
    }
}

function Invoke-WithGoEnv([hashtable]$Env, [scriptblock]$Body) {
    $names = @("GOOS", "GOARCH", "GOARM", "GO386", "CGO_ENABLED")
    $saved = @{}
    foreach ($n in $names) { $saved[$n] = [Environment]::GetEnvironmentVariable($n) }
    try {
        foreach ($n in $names) { [Environment]::SetEnvironmentVariable($n, $null) }
        foreach ($k in $Env.Keys) { [Environment]::SetEnvironmentVariable($k, $Env[$k]) }
        & $Body
    }
    finally {
        foreach ($n in $names) { [Environment]::SetEnvironmentVariable($n, $saved[$n]) }
    }
}

# Builds ./cmd/<App> for one target. -X main.version is a no-op until cmd/<app>
# declares `var version string`; it is passed unconditionally so stamping starts
# working as soon as that variable exists.
function Invoke-GoBuild {
    param(
        [string]$Goos, [string]$Goarch, [string]$App, [string]$Output,
        [string]$Version, [string[]]$ExtraLdflags = @()
    )
    $ldflags = @("-s", "-w", "-X", "main.version=$Version") + $ExtraLdflags
    $envs = @{ GOOS = $Goos; GOARCH = $Goarch; CGO_ENABLED = "0" }
    if ($Goarch -eq "arm") { $envs.GOARM = "7" }
    Write-Host "  go build $Goos/$Goarch $App -> $Output"
    Invoke-WithGoEnv $envs {
        Push-Location $script:RepoRoot
        try {
            & go build -trimpath -tags $script:BuildTags -ldflags ($ldflags -join " ") -o $Output "./cmd/$App"
            if ($LASTEXITCODE -ne 0) { throw "$App build failed for $Goos/$Goarch" }
        }
        finally { Pop-Location }
    }
}

# Builds the host-native helper tools (makefat, mkarchive) once per run.
function Get-HostTool([string]$Name) {
    $toolDir = Join-Path $script:RepoRoot "dist/.tools"
    $exe = if ($script:OnWindows) { "$Name.exe" } else { $Name }
    $path = Join-Path $toolDir $exe
    if (-not $script:BuiltTools) { $script:BuiltTools = @{} }
    if (-not $script:BuiltTools[$Name]) {
        New-Item -ItemType Directory -Force -Path $toolDir | Out-Null
        Invoke-WithGoEnv @{ CGO_ENABLED = "0" } {
            Push-Location $script:RepoRoot
            try {
                & go build -trimpath -o $path "./tools/$Name"
                if ($LASTEXITCODE -ne 0) { throw "failed to build tools/$Name" }
            }
            finally { Pop-Location }
        }
        $script:BuiltTools[$Name] = $true
    }
    return $path
}

# Creates a .zip/.tar.gz with correct Unix permissions (see tools/mkarchive).
function New-ReleaseArchive([string]$Output, [string]$BaseDir, [string[]]$Entries) {
    $tool = Get-HostTool "mkarchive"
    & $tool -o $Output -C $BaseDir @Entries
    if ($LASTEXITCODE -ne 0) { throw "mkarchive failed for $Output" }
    Write-Host "  archive $Output"
}

function Write-Sha256Sums([string]$Dir, [string]$FileName = "SHA256SUMS") {
    $files = Get-ChildItem -LiteralPath $Dir -File | Where-Object { $_.Name -ne $FileName } | Sort-Object Name
    $lines = foreach ($f in $files) {
        "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $f.FullName).Hash.ToLowerInvariant(), $f.Name
    }
    $target = Join-Path $Dir $FileName
    # LF line endings and no BOM so `sha256sum -c` / `shasum -a 256 -c` accept it.
    [System.IO.File]::WriteAllText($target, (($lines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    Write-Host "  checksums $target"
}

function Write-Utf8File([string]$Path, [string]$Content) {
    [System.IO.File]::WriteAllText($Path, $Content.Replace("`r`n", "`n"), [System.Text.UTF8Encoding]::new($false))
}
