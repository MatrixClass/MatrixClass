#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/linux}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

"$script_dir/build-linux.sh" "$output_dir"

for arch in amd64 arm64 loong64; do
  stage="$output_dir/matrixclass-linux-$arch"
  archive="$output_dir/matrixclass-linux-$arch.tar.gz"
  rm -rf "$stage" "$archive"
  mkdir -p "$stage"
  cp "$output_dir/matrixclass-teacher-linux-$arch" "$stage/"
  cp "$output_dir/matrixclass-student-linux-$arch" "$stage/"
  cp "$repo_root/packaging/linux/matrixclass-teacher.desktop" "$stage/"
  cp "$repo_root/packaging/linux/matrixclass-student.desktop" "$stage/"
  cp "$repo_root/packaging/linux/matrixclass.svg" "$stage/"
  cp "$repo_root/packaging/linux/matrixclass-teacher.service" "$stage/"
  cp "$repo_root/packaging/linux/matrixclass-student.service" "$stage/"
  cp "$repo_root/packaging/linux/install.sh" "$stage/"
  cp "$repo_root/packaging/linux/uninstall.sh" "$stage/"
  cp "$repo_root/packaging/linux/README.md" "$stage/"
  tar -czf "$archive" -C "$output_dir" "matrixclass-linux-$arch"
done

printf 'Linux packages completed: amd64 arm64 loong64\n'

if ! command -v dpkg-deb >/dev/null 2>&1; then
  printf '%s\n' 'Skipping .deb generation: dpkg-deb is unavailable on this host.'
fi
if ! command -v rpmbuild >/dev/null 2>&1; then
  printf '%s\n' 'Skipping .rpm generation: rpmbuild is unavailable on this host.'
fi
