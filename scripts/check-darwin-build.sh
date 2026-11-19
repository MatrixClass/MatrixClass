#!/usr/bin/env sh
set -eu

input_dir=${1:-dist/darwin}
require_universal=${REQUIRE_UNIVERSAL:-0}
require_dmg=${REQUIRE_DMG:-0}
[ -d "$input_dir" ] || { printf 'Darwin build directory not found: %s\n' "$input_dir" >&2; exit 1; }

: > "$input_dir/SHA256SUMS.txt"
for name in matrixclass-teacher-darwin-amd64 matrixclass-student-darwin-amd64 matrixclass-teacher-darwin-arm64 matrixclass-student-darwin-arm64; do
  path="$input_dir/$name"
  [ -f "$path" ] || { printf 'Missing Darwin artifact: %s\n' "$path" >&2; exit 1; }
  if command -v shasum >/dev/null 2>&1; then hash=$(shasum -a 256 "$path" | awk '{print $1}');
  elif command -v sha256sum >/dev/null 2>&1; then hash=$(sha256sum "$path" | awk '{print $1}');
  else printf 'Neither shasum nor sha256sum is available\n' >&2; exit 1; fi
  printf '%s  %s\n' "$hash" "$name" >> "$input_dir/SHA256SUMS.txt"
  printf 'validated %-42s %s\n' "$name" "$hash"
done

teacher_universal="$input_dir/apps/universal/MatrixClass Teacher.app/Contents/MacOS/matrixclass-teacher"
student_universal="$input_dir/apps/universal/MatrixClass Student.app/Contents/MacOS/matrixclass-student"
if [ -f "$teacher_universal" ] && [ -f "$student_universal" ]; then
  command -v lipo >/dev/null 2>&1 && { lipo -info "$teacher_universal"; lipo -info "$student_universal"; } || true
  printf 'Universal 2 app binaries: present\n'
elif [ "$require_universal" = 1 ]; then
  printf 'Universal 2 app binaries are required but were not found\n' >&2; exit 1
else printf 'Universal 2 app binaries: not present (run on macOS with lipo)\n'; fi

dmg_path="$input_dir/MatrixClass-macOS-unsigned.dmg"
if [ -f "$dmg_path" ]; then printf 'Unsigned DMG: present\n';
elif [ "$require_dmg" = 1 ]; then printf 'Unsigned DMG is required but was not found: %s\n' "$dmg_path" >&2; exit 1
else printf 'Unsigned DMG: not present (run on macOS with hdiutil)\n'; fi
printf 'Darwin artifact validation passed: %s\nSHA-256 manifest: %s\n' "$input_dir" "$input_dir/SHA256SUMS.txt"
