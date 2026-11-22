#!/usr/bin/env sh
# Builds MatrixClass Windows GUI executables (-H windowsgui) and zip packages.
# Usage: scripts/build-windows.sh [output-dir] [arch...]
#   env: MATRIXCLASS_VERSION (default 0.1.0), SKIP_PACKAGE=1
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

output_dir=$(abs_path "${1:-dist/windows}")
[ $# -gt 0 ] && shift
[ $# -eq 0 ] && set -- $supported_windows
version=$(mc_version)
assert_arches windows "$@"
mkdir -p "$output_dir"

for arch in "$@"; do
  printf 'Building windows/%s (version %s)\n' "$arch" "$version"
  for app in teacher student; do
    go_build windows "$arch" "$app" "$output_dir/matrixclass-$app-windows-$arch.exe" "$version" -H windowsgui
  done
  if [ "${SKIP_PACKAGE:-0}" = 1 ]; then continue; fi
  stage_name="matrixclass-windows-$arch"
  stage="$output_dir/$stage_name"
  rm -rf "$stage"
  mkdir -p "$stage"
  for app in teacher student; do
    cp "$output_dir/matrixclass-$app-windows-$arch.exe" "$stage/matrixclass-$app.exe"
  done
  cp "$repo_root/packaging/windows/README.md" "$stage/README.md"
  make_archive "$output_dir/$stage_name.zip" "$output_dir" "$stage_name"
done

printf 'Windows builds completed: %s -> %s\n' "$*" "$output_dir"
