#!/usr/bin/env sh
# Builds and packages every supported MatrixClass target into dist/ and writes
# dist/release/SHA256SUMS. See scripts/build-all.ps1 for the target list.
# Usage: scripts/build-all.sh [output-dir]
#   env: MATRIXCLASS_VERSION (default 0.1.0)
#        DISTRO_PACKAGES=1     also build .deb/.rpm (needs dpkg-deb / rpmbuild)
#        SIGN_ADHOC=1          ad-hoc codesign the .app bundles (macOS only)
#        INCLUDE_COMPILE_ONLY=1 also cross-compile windows/386 and
#                              linux/{loong64,riscv64,386,arm} into dist/compile-only (never released)
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

dist=$(abs_path "${1:-dist}")
version=$(mc_version)
export MATRIXCLASS_VERSION="$version"
release="$dist/release"
printf 'MatrixClass build-all: version %s -> %s\n' "$version" "$dist"

"$repo_root/scripts/build-windows.sh" "$dist/windows" $supported_windows
"$repo_root/scripts/build-linux-package.sh" "$dist/linux" $supported_linux
if [ "${DISTRO_PACKAGES:-0}" = 1 ]; then
  for arch in $supported_linux; do
    sh "$repo_root/scripts/build-linux-distro-packages.sh" "$dist/linux" "$arch"
  done
fi
"$repo_root/scripts/build-darwin-package.sh" "$dist/darwin"

if [ "${INCLUDE_COMPILE_ONLY:-0}" = 1 ]; then
  co="$dist/compile-only"
  mkdir -p "$co"
  for arch in $compile_only_windows; do
    for app in teacher student; do
      go_build windows "$arch" "$app" "$co/matrixclass-$app-windows-$arch.exe" "$version" -H windowsgui
    done
  done
  for arch in $compile_only_linux; do
    for app in teacher student; do
      go_build linux "$arch" "$app" "$co/matrixclass-$app-linux-$arch" "$version"
    done
  done
  printf 'Compile-only targets built in %s (not released).\n' "$co"
fi

rm -rf "$release"
mkdir -p "$release"
for arch in $supported_windows; do cp "$dist/windows/matrixclass-windows-$arch.zip" "$release/"; done
for arch in $supported_linux; do cp "$dist/linux/matrixclass-linux-$arch.tar.gz" "$release/"; done
for arch in amd64 arm64 universal; do cp "$dist/darwin/matrixclass-macos-$arch.zip" "$release/"; done
for f in "$dist/linux/matrixclass-$version-"*.deb "$dist/linux/matrixclass-$version-1."*.rpm "$dist/darwin/matrixclass-macos-universal.dmg"; do
  if [ -f "$f" ]; then cp "$f" "$release/"; fi
done

write_sha256sums "$release"
printf '\nRelease artifacts:\n'
ls -l "$release"
