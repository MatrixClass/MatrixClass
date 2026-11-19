#!/usr/bin/env sh
set -eu

prefix=${PREFIX:-/opt/matrixclass}
bindir=${BINDIR:-/usr/local/bin}
desktopdir=${DESKTOPDIR:-/usr/share/applications}
icondir=${ICONDIR:-/usr/share/icons/hicolor/scalable/apps}

rm -f "$bindir/matrixclass-teacher" "$bindir/matrixclass-student"
rm -f "$desktopdir/matrixclass-teacher.desktop" "$desktopdir/matrixclass-student.desktop"
rm -f "$icondir/matrixclass.svg"
rm -rf "$prefix"

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$desktopdir" >/dev/null 2>&1 || true
fi
echo "MatrixClass removed"
