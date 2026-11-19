#!/usr/bin/env sh
set -eu

prefix=${PREFIX:-/opt/matrixclass}
bindir=${BINDIR:-/usr/local/bin}
desktopdir=${DESKTOPDIR:-/usr/share/applications}
icondir=${ICONDIR:-/usr/share/icons/hicolor/scalable/apps}
systemd_user_dir=${SYSTEMD_USER_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user}

rm -f "$bindir/matrixclass-teacher" "$bindir/matrixclass-student"
rm -f "$desktopdir/matrixclass-teacher.desktop" "$desktopdir/matrixclass-student.desktop"
rm -f "$icondir/matrixclass.svg"
rm -rf "$prefix"
rm -f "$systemd_user_dir/matrixclass-teacher.service" "$systemd_user_dir/matrixclass-student.service"

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload || true
fi

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$desktopdir" >/dev/null 2>&1 || true
fi
echo "MatrixClass binaries and desktop entries removed"
echo "User data, configuration, logs and received files were preserved"
