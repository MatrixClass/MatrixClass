#!/usr/bin/env sh
# Cross-builds MatrixClass Linux binaries (CGO_ENABLED=0).
# Usage: scripts/build-linux.sh [output-dir] [arch...]
#   default arches: amd64 arm64 loong64 (native mygo UI backend)
#   compile-only:   riscv64 386 arm (GOARM=7)
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

output_dir=$(abs_path "${1:-dist/linux}")
[ $# -gt 0 ] && shift
[ $# -eq 0 ] && set -- $supported_linux
version=$(mc_version)
assert_arches linux "$@"
mkdir -p "$output_dir"

for arch in "$@"; do
  printf 'Building linux/%s (version %s)\n' "$arch" "$version"
  go_build linux "$arch" teacher "$output_dir/matrixclass-teacher-linux-$arch" "$version"
  go_build linux "$arch" student "$output_dir/matrixclass-student-linux-$arch" "$version"
done

printf 'Linux builds completed: %s\n' "$*"
