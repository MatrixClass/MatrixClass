$ErrorActionPreference = "Stop"

$forbidden = @("GPL", "LGPL", "AGPL", "GNU GENERAL PUBLIC", "GNU LESSER GENERAL PUBLIC", "GNU AFFERO")
$mods = go list -m -f "{{.Path}}|{{.Dir}}" all
$violations = @()

foreach ($line in $mods) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $parts = $line -split "\|", 2
    $modPath = $parts[0]
    $modDir = $parts[1]
    if ([string]::IsNullOrWhiteSpace($modDir) -or -not (Test-Path $modDir)) { continue }

    $licenseFiles = Get-ChildItem -Path $modDir -File -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -match "^(LICENSE|LICENCE|COPYING)"
    }
    if (-not $licenseFiles) {
        $licDir = Join-Path $modDir "LICENSES"
        if (Test-Path $licDir) {
            $licenseFiles = Get-ChildItem -Path $licDir -File -ErrorAction SilentlyContinue
        }
    }
    foreach ($file in $licenseFiles) {
        $head = (Get-Content -Path $file.FullName -TotalCount 25 -ErrorAction SilentlyContinue) -join "`n"
        foreach ($kw in $forbidden) {
            if ($head -match [regex]::Escape($kw)) {
                $violations += "$modPath ($($file.Name)) matched forbidden keyword: $kw"
            }
        }
    }
}

if ($violations.Count -gt 0) {
    Write-Error ("Forbidden copyleft license detected:`n" + ($violations -join "`n"))
    exit 1
}

Write-Output "License audit passed: 0 GPL/LGPL/AGPL dependencies found across $($mods.Count) modules."
