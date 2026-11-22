#!/usr/bin/env sh
# Cross-builds MatrixClass macOS binaries (CGO_ENABLED=0, works on any host)
# and combines them into Universal 2 binaries with tools/makefat (no lipo needed).
# Usage: scripts/build-darwin.sh [output-dir]
#   env: MATRIXCLASS_VERSION (default 0.1.0)
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

output_dir=$(abs_path "${1:-dist/darwin}")
version=$(mc_version)
mkdir -p "$output_dir"

for arch in $supported_darwin; do
  printf 'Building darwin/%s (CGO_ENABLED=0, tags=%s, version %s)\n' "$arch" "$build_tags" "$version"
  for app in teacher student; do
    go_build darwin "$arch" "$app" "$output_dir/matrixclass-$app-darwin-$arch" "$version"
  done
done

makefat=$(host_tool makefat)
for app in teacher student; do
  "$makefat" -o "$output_dir/matrixclass-$app-darwin-universal" \
    "$output_dir/matrixclass-$app-darwin-amd64" "$output_dir/matrixclass-$app-darwin-arm64"
  "$makefat" -info "$output_dir/matrixclass-$app-darwin-universal"
done

cat > "$output_dir/build-info.txt" <<EOF
GOOS=darwin
ARCHITECTURES=amd64,arm64,universal
VERSION=$version
CGO_ENABLED=0
BUILD_TAGS=$build_tags
UNIVERSAL_TOOL=tools/makefat
EOF

printf 'Darwin builds completed: amd64 arm64 universal\n'
