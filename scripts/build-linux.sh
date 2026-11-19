#!/usr/bin/env sh
set -eu

output_dir="${1:-dist/linux}"
mkdir -p "$output_dir"

for arch in amd64 arm64 loong64; do
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
    go build -tags mygo_nowebview -o "$output_dir/matrixclass-teacher-linux-$arch" ./cmd/teacher
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
    go build -tags mygo_nowebview -o "$output_dir/matrixclass-student-linux-$arch" ./cmd/student
done

printf 'Linux builds completed: amd64 arm64 loong64\n'
