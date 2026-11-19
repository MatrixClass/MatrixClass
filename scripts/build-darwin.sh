#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/darwin}
build_tags=${BUILD_TAGS:-mygo_nowebview}
mkdir -p "$output_dir"

for arch in amd64 arm64; do
  printf 'Building darwin/%s (CGO_ENABLED=0, tags=%s)\n' "$arch" "$build_tags"
  GOOS=darwin GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -tags "$build_tags" -o "$output_dir/matrixclass-teacher-darwin-$arch" ./cmd/teacher
  GOOS=darwin GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -tags "$build_tags" -o "$output_dir/matrixclass-student-darwin-$arch" ./cmd/student
done

cat > "$output_dir/build-info.txt" <<EOF
GOOS=darwin
ARCHITECTURES=amd64,arm64
CGO_ENABLED=0
BUILD_TAGS=$build_tags
NATIVE_APPLE_FRAMEWORKS=not included in cross-build
EOF

printf 'Darwin builds completed: amd64 arm64\n'
