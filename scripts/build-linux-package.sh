#!/usr/bin/env sh
# Builds Linux binaries and matrixclass-linux-<arch> directory + tar.gz packages.
# Usage: scripts/build-linux-package.sh [output-dir] [arch...]   (SKIP_BUILD=1 to reuse binaries)
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

output_dir=$(abs_path "${1:-dist/linux}")
[ $# -gt 0 ] && shift
[ $# -eq 0 ] && set -- $supported_linux

if [ "${SKIP_BUILD:-0}" != 1 ]; then
  "$repo_root/scripts/build-linux.sh" "$output_dir" "$@"
fi

for arch in "$@"; do
  stage_name="matrixclass-linux-$arch"
  stage="$output_dir/$stage_name"
  rm -rf "$stage" "$output_dir/$stage_name.tar.gz"
  mkdir -p "$stage"
  cp "$output_dir/matrixclass-teacher-linux-$arch" "$output_dir/matrixclass-student-linux-$arch" "$stage/"
  for f in matrixclass-teacher.desktop matrixclass-student.desktop matrixclass.svg matrixclass-student.png matrixclass-teacher.png \
           matrixclass-teacher.service matrixclass-student.service install.sh uninstall.sh README.md; do
    cp "$repo_root/packaging/linux/$f" "$stage/"
  done
  chmod 0755 "$stage/matrixclass-teacher-linux-$arch" "$stage/matrixclass-student-linux-$arch" "$stage/install.sh" "$stage/uninstall.sh"
  make_archive "$output_dir/$stage_name.tar.gz" "$output_dir" "$stage_name"
done

printf 'Linux packages completed: %s\n' "$*"
command -v dpkg-deb >/dev/null 2>&1 || printf '%s\n' 'Note: dpkg-deb unavailable; .deb packages need scripts/build-linux-distro-packages.sh on a host with dpkg-deb.'
command -v rpmbuild >/dev/null 2>&1 || printf '%s\n' 'Note: rpmbuild unavailable; .rpm packages need scripts/build-linux-distro-packages.sh on a host with rpmbuild.'
