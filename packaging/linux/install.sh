#!/usr/bin/env sh
set -eu

prefix=${PREFIX:-/opt/matrixclass}
bindir=${BINDIR:-/usr/local/bin}
desktopdir=${DESKTOPDIR:-/usr/share/applications}
icondir=${ICONDIR:-/usr/share/icons/hicolor/scalable/apps}
datadir=${DATADIR:-${XDG_DATA_HOME:-$HOME/.local/share}/matrixclass}
configdir=${CONFIGDIR:-${XDG_CONFIG_HOME:-$HOME/.config}/matrixclass}
logdir=${LOGDIR:-${XDG_STATE_HOME:-$HOME/.local/state}/matrixclass}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  loongarch64|loong64) arch=loong64 ;;
  riscv64) arch=riscv64 ;;
  i386|i686) arch=386 ;;
  armv7l|armv7*) arch=arm ;;
  *) echo "unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac

if [ ! -f "$script_dir/matrixclass-teacher-linux-$arch" ]; then
  echo "This package does not contain binaries for $arch; download matrixclass-linux-$arch.tar.gz instead." >&2
  exit 1
fi

mkdir -p "$prefix" "$bindir" "$desktopdir" "$icondir"
mkdir -p "$datadir" "$configdir" "$logdir"
install -m 0755 "$script_dir/matrixclass-teacher-linux-$arch" "$prefix/matrixclass-teacher"
install -m 0755 "$script_dir/matrixclass-student-linux-$arch" "$prefix/matrixclass-student"
install -m 0755 "$prefix/matrixclass-teacher" "$bindir/matrixclass-teacher"
install -m 0755 "$prefix/matrixclass-student" "$bindir/matrixclass-student"
install -m 0644 "$script_dir/matrixclass-teacher.desktop" "$desktopdir/matrixclass-teacher.desktop"
install -m 0644 "$script_dir/matrixclass-student.desktop" "$desktopdir/matrixclass-student.desktop"
install -m 0644 "$script_dir/matrixclass.svg" "$icondir/matrixclass.svg"

if [ "${INSTALL_SYSTEMD:-0}" = "1" ] && command -v systemctl >/dev/null 2>&1; then
  systemd_user_dir=${SYSTEMD_USER_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user}
  mkdir -p "$systemd_user_dir"
  install -m 0644 "$script_dir/matrixclass-teacher.service" "$systemd_user_dir/matrixclass-teacher.service"
  install -m 0644 "$script_dir/matrixclass-student.service" "$systemd_user_dir/matrixclass-student.service"
  systemctl --user daemon-reload || true
  echo "User systemd templates installed; services are not enabled automatically."
fi

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$desktopdir" >/dev/null 2>&1 || true
fi
echo "MatrixClass installed for $arch in $prefix"
echo "Data: $datadir"
echo "Config: $configdir"
echo "Logs: $logdir"
