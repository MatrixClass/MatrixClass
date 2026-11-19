#!/usr/bin/env sh
set -eu

prefix=${PREFIX:-/opt/matrixclass}
bindir=${BINDIR:-/usr/local/bin}
desktopdir=${DESKTOPDIR:-/usr/share/applications}
icondir=${ICONDIR:-/usr/share/icons/hicolor/scalable/apps}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  loongarch64|loong64) arch=loong64 ;;
  *) echo "unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac

mkdir -p "$prefix" "$bindir" "$desktopdir" "$icondir"
install -m 0755 "$script_dir/matrixclass-teacher-linux-$arch" "$prefix/matrixclass-teacher"
install -m 0755 "$script_dir/matrixclass-student-linux-$arch" "$prefix/matrixclass-student"
install -m 0755 "$prefix/matrixclass-teacher" "$bindir/matrixclass-teacher"
install -m 0755 "$prefix/matrixclass-student" "$bindir/matrixclass-student"
install -m 0644 "$script_dir/matrixclass-teacher.desktop" "$desktopdir/matrixclass-teacher.desktop"
install -m 0644 "$script_dir/matrixclass-student.desktop" "$desktopdir/matrixclass-student.desktop"
install -m 0644 "$script_dir/matrixclass.svg" "$icondir/matrixclass.svg"

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$desktopdir" >/dev/null 2>&1 || true
fi
echo "MatrixClass installed for $arch in $prefix"
