#!/usr/bin/env sh
# Validates darwin artifacts and writes SHA256SUMS.txt.
# Usage: scripts/check-darwin-build.sh [input-dir]   env: REQUIRE_UNIVERSAL=1 REQUIRE_DMG=1
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

input_dir=$(abs_path "${1:-dist/darwin}")
require_universal=${REQUIRE_UNIVERSAL:-0}
require_dmg=${REQUIRE_DMG:-0}
[ -d "$input_dir" ] || { printf 'Darwin build directory not found: %s\n' "$input_dir" >&2; exit 1; }

names="matrixclass-teacher-darwin-amd64 matrixclass-student-darwin-amd64 matrixclass-teacher-darwin-arm64 matrixclass-student-darwin-arm64"
has_universal=0
if [ -f "$input_dir/matrixclass-teacher-darwin-universal" ] && [ -f "$input_dir/matrixclass-student-darwin-universal" ]; then
  has_universal=1
  names="$names matrixclass-teacher-darwin-universal matrixclass-student-darwin-universal"
elif [ "$require_universal" = 1 ]; then
  printf 'Universal 2 binaries are required but were not found in %s\n' "$input_dir" >&2; exit 1
fi

: > "$input_dir/SHA256SUMS.txt"
for name in $names; do
  path="$input_dir/$name"
  [ -f "$path" ] || { printf 'Missing Darwin artifact: %s\n' "$path" >&2; exit 1; }
  hash=$(sha256_of "$path")
  printf '%s  %s\n' "$hash" "$name" >> "$input_dir/SHA256SUMS.txt"
  printf 'validated %-42s %s\n' "$name" "$hash"
done

if [ "$has_universal" = 1 ]; then
  makefat=$(host_tool makefat)
  for role in teacher student; do
    info=$("$makefat" -info "$input_dir/matrixclass-$role-darwin-universal")
    printf '%s\n' "$info"
    case "$info" in *CpuAmd64*CpuArm64*) ;; *) printf 'matrixclass-%s-darwin-universal is not amd64+arm64\n' "$role" >&2; exit 1 ;; esac
    if command -v lipo >/dev/null 2>&1; then lipo -info "$input_dir/matrixclass-$role-darwin-universal"; fi
  done
  for app in "MatrixClass Teacher" "MatrixClass Student"; do
    plist="$input_dir/apps/universal/$app.app/Contents/Info.plist"
    [ -f "$plist" ] || continue
    for key in CFBundleIdentifier CFBundleExecutable LSMinimumSystemVersion NSScreenCaptureUsageDescription NSMicrophoneUsageDescription NSAppleEventsUsageDescription; do
      grep -q "<key>$key</key>" "$plist" || { printf '%s is missing %s\n' "$plist" "$key" >&2; exit 1; }
    done
    if command -v plutil >/dev/null 2>&1; then plutil -lint "$plist"; fi
    if command -v codesign >/dev/null 2>&1; then codesign --verify --deep --strict --verbose=1 "$input_dir/apps/universal/$app.app" || true; fi
  done
  printf 'Universal 2 binaries: present (verified with tools/makefat)\n'
else
  printf 'Universal 2 binaries: not present (run scripts/build-darwin.sh)\n'
fi

dmg_path="$input_dir/matrixclass-macos-universal.dmg"
if [ -f "$dmg_path" ]; then printf 'DMG: present\n';
elif [ "$require_dmg" = 1 ]; then printf 'DMG is required but was not found: %s\n' "$dmg_path" >&2; exit 1
else printf 'DMG: not present (built only on macOS with hdiutil)\n'; fi
printf 'Darwin artifact validation passed: %s\nSHA-256 manifest: %s\n' "$input_dir" "$input_dir/SHA256SUMS.txt"
