<#
.SYNOPSIS
  Fails if any dependency shipped in MatrixClass uses a GPL/LGPL/AGPL license.
.DESCRIPTION
  By default audits every module linked into ./cmd/teacher and ./cmd/student for
  each release target (windows/linux/darwin x amd64/arm64), using
  `go list -deps`, which downloads missing modules so nothing is silently
  skipped on a fresh module cache. -All audits every module in the build graph
  (`go mod download` + `go list -m all`), including test/tool-only modules.

  A shipped module whose license file cannot be found is an error. Each
  license is classified (MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, ...)
  and printed with -Verbose / -ShowTable.
#>
param(
    [switch]$All,
    [switch]$ShowTable
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot
try {
    $forbidden = @("GNU GENERAL PUBLIC", "GNU LESSER GENERAL PUBLIC", "GNU LIBRARY GENERAL PUBLIC", "GNU AFFERO",
        "\bGPL\b", "\bLGPL\b", "\bAGPL\b", "GPL-[0-9]", "LGPL-[0-9]", "AGPL-[0-9]")
    $targets = @("windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")

    $modules = @{}
    $saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
    try {
        $env:CGO_ENABLED = "0"
        if ($All) {
            Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
            go mod download all
            if ($LASTEXITCODE -ne 0) { throw "go mod download failed" }
            foreach ($line in (go list -m -f "{{if not .Main}}{{.Path}}@{{.Version}}|{{.Dir}}{{end}}" all)) {
                if ($line) { $p = $line -split "\|", 2; $modules[$p[0]] = $p[1] }
            }
            if ($LASTEXITCODE -ne 0) { throw "go list -m all failed" }
        } else {
            foreach ($t in $targets) {
                $env:GOOS, $env:GOARCH = $t -split "/"
                $lines = go list -deps -tags mygo_nowebview -f "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}|{{.Dir}}{{end}}{{end}}" ./cmd/teacher ./cmd/student
                if ($LASTEXITCODE -ne 0) { throw "go list -deps failed for $t" }
                foreach ($line in $lines) {
                    if ($line) { $p = $line -split "\|", 2; $modules[$p[0]] = $p[1] }
                }
            }
        }
    }
    finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }

    function Get-LicenseKind([string]$text) {
        switch -Regex ($text) {
            "GNU AFFERO" { return "AGPL" }
            "GNU (LESSER|LIBRARY) GENERAL PUBLIC" { return "LGPL" }
            "GNU GENERAL PUBLIC" { return "GPL" }
            "Apache License" { return "Apache-2.0" }
            "Mozilla Public License" { return "MPL-2.0" }
            "Permission is hereby granted, free of charge" { return "MIT" }
            "Permission to use, copy, modify, and(/or)? distribute this software for any purpose" { return "ISC" }
            "Neither the name|names of its\s+contributors" { return "BSD-3-Clause" }
            "Redistribution and use in source and binary forms" { return "BSD-2-Clause" }
            "This is free and unencumbered software released into the public domain" { return "Unlicense" }
            "Is Public Domain|dedicated to the public\s+domain" { return "PublicDomain" }
            "^# Third-Party Licenses|^https?://\S+\s*$" { return "notice" }
            default { return "unknown" }
        }
    }

    $violations = @()
    $missing = @()
    $rows = @()
    foreach ($mod in ($modules.Keys | Sort-Object)) {
        $dir = $modules[$mod]
        if ([string]::IsNullOrWhiteSpace($dir) -or -not (Test-Path -LiteralPath $dir)) { $missing += "$mod (module not downloaded)"; continue }
        $licenseFiles = @(Get-ChildItem -LiteralPath $dir -File -ErrorAction SilentlyContinue | Where-Object { $_.Name -match "^(LICEN[CS]E|COPYING|UNLICENSE)" })
        if ($licenseFiles.Count -eq 0 -and (Test-Path -LiteralPath (Join-Path $dir "LICENSES"))) {
            $licenseFiles = @(Get-ChildItem -LiteralPath (Join-Path $dir "LICENSES") -File)
        }
        if ($licenseFiles.Count -eq 0) { $missing += "$mod (no LICENSE/COPYING file in $dir)"; continue }
        $kinds = @()
        foreach ($file in $licenseFiles) {
            $text = (Get-Content -LiteralPath $file.FullName -TotalCount 40 -ErrorAction SilentlyContinue) -join "`n"
            $kind = Get-LicenseKind $text
            $kinds += $kind
            foreach ($kw in $forbidden) {
                if ($text -cmatch $kw) { $violations += "$mod ($($file.Name)) matched forbidden pattern: $kw"; break }
            }
        }
        $rows += [PSCustomObject]@{ Module = $mod; License = (($kinds | Select-Object -Unique) -join ",") }
    }

    if ($ShowTable -or $VerbosePreference -ne "SilentlyContinue") { $rows | Format-Table -AutoSize | Out-String | Write-Host }
    $unknown = @($rows | Where-Object { $_.License -match "unknown" })
    foreach ($u in $unknown) { Write-Warning "Unrecognized license text, review manually: $($u.Module)" }

    if ($violations.Count -gt 0) {
        Write-Error ("Forbidden copyleft license detected:`n" + ($violations -join "`n"))
        exit 1
    }
    if ($missing.Count -gt 0) {
        Write-Error ("License could not be verified for:`n" + ($missing -join "`n"))
        exit 1
    }
    $scope = if ($All) { "all modules in the build graph" } else { "modules linked into teacher/student for $($targets -join ', ')" }
    $summary = ($rows | Group-Object License | Sort-Object Count -Descending | ForEach-Object { "$($_.Name)=$($_.Count)" }) -join " "
    Write-Output "License audit passed: 0 GPL/LGPL/AGPL across $($rows.Count) $scope. [$summary]"
}
finally {
    Pop-Location
}
