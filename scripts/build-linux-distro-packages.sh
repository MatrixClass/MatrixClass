#!/usr/bin/env sh
# Builds separate teacher and student .deb (dpkg-deb) and .rpm (rpmbuild)
# packages from an existing matrixclass-linux-<arch> stage directory
# (see build-linux-package.sh). Missing tools are skipped with a message.
# Usage: scripts/build-linux-distro-packages.sh [output-dir] [arch]
#   env: MATRIXCLASS_VERSION / VERSION (default 0.1.0)
set -eu

output_dir=${1:-dist/linux}
arch=${2:-$(uname -m)}
case "$arch" in
  x86_64|amd64) arch=amd64; deb_arch=amd64; rpm_arch=x86_64 ;;
  aarch64|arm64) arch=arm64; deb_arch=arm64; rpm_arch=aarch64 ;;
  loongarch64|loong64) arch=loong64; deb_arch=loong64; rpm_arch=loongarch64 ;;
  riscv64) arch=riscv64; deb_arch=riscv64; rpm_arch=riscv64 ;;
  i386|i686|386) arch=386; deb_arch=i386; rpm_arch=i686 ;;
  armv7l|armhf|arm) arch=arm; deb_arch=armhf; rpm_arch=armv7hl ;;
  *) echo "unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac

version=${MATRIXCLASS_VERSION:-${VERSION:-0.1.0}}
version=${version#v}
stage="$output_dir/matrixclass-linux-$arch"
[ -d "$stage" ] || { echo "stage directory not found: $stage (run build-linux-package.sh first)" >&2; exit 1; }
mkdir -p "$output_dir"
output_abs=$(cd "$output_dir" && pwd)

helper=/opt/matrixclass/matrixclass-student-helper
stop_student="pkill -f '^/opt/matrixclass/matrixclass-student( |\$)' >/dev/null 2>&1 || true"

summary_teacher="MatrixClass teacher console"
summary_student="MatrixClass student client"
desc_teacher="Teacher console for the MatrixClass LAN electronic classroom."
desc_student="Student client for the MatrixClass LAN electronic classroom. Starts automatically after installation and at login; uninstalling requires the student exit password."

build_deb() {
  app=$1
  root="$output_dir/matrixclass-$app-deb-$arch"
  rm -rf "$root"
  mkdir -p "$root/DEBIAN" "$root/opt/matrixclass" "$root/usr/bin" "$root/usr/share/applications" "$root/usr/share/pixmaps"
  install -m 0755 "$stage/matrixclass-$app-linux-$arch" "$root/opt/matrixclass/matrixclass-$app"
  ln -s "/opt/matrixclass/matrixclass-$app" "$root/usr/bin/matrixclass-$app"
  install -m 0644 "$stage/matrixclass-$app.desktop" "$root/usr/share/applications/matrixclass-$app.desktop"
  install -m 0644 "$stage/matrixclass-$app.png" "$root/usr/share/pixmaps/matrixclass-$app.png"
  if [ "$app" = teacher ]; then
    summary=$summary_teacher; desc=$desc_teacher; recommends="libx11-6, libxtst6, libgl1"
  else
    summary=$summary_student; desc=$desc_student; recommends="libx11-6, libxtst6, libgl1, procps"
    mkdir -p "$root/etc/xdg/autostart"
    install -m 0755 "$stage/matrixclass-student-helper.sh" "$root$helper"
    install -m 0644 "$stage/matrixclass-student-autostart.desktop" "$root/etc/xdg/autostart/matrixclass-student.desktop"
    cat > "$root/DEBIAN/preinst" <<EOF
#!/bin/sh
set -e
$stop_student
exit 0
EOF
    cat > "$root/DEBIAN/postinst" <<EOF
#!/bin/sh
set -e
if [ "\$1" = configure ]; then
  $helper launch || true
fi
exit 0
EOF
    cat > "$root/DEBIAN/prerm" <<EOF
#!/bin/sh
set -e
case "\$1" in
  remove|deconfigure)
    $helper verify || exit 1
    $helper stop || true
    ;;
  upgrade)
    $stop_student
    ;;
esac
exit 0
EOF
    chmod 0755 "$root/DEBIAN/preinst" "$root/DEBIAN/postinst" "$root/DEBIAN/prerm"
  fi
  cat > "$root/DEBIAN/control" <<EOF
Package: matrixclass-$app
Version: $version
Section: education
Priority: optional
Architecture: $deb_arch
Maintainer: MatrixClass
Conflicts: matrixclass
Replaces: matrixclass
Recommends: $recommends
Description: $summary
 $desc
EOF
  out="$output_dir/matrixclass-${app}_${version}_${deb_arch}.deb"
  dpkg-deb --root-owner-group --build "$root" "$out" 2>/dev/null || dpkg-deb --build "$root" "$out"
}

build_rpm() {
  app=$1
  root="$output_abs/rpm-$app-$arch"
  rm -rf "$root"
  mkdir -p "$root/SPECS" "$root/SOURCES"
  tar -czf "$root/SOURCES/matrixclass-$version.tar.gz" -C "$output_dir" "matrixclass-linux-$arch"
  if [ "$app" = teacher ]; then
    summary=$summary_teacher; desc=$desc_teacher
    extra_install=""; extra_files=""; scripts=""
  else
    summary=$summary_student; desc=$desc_student
    extra_install="install -D -m 0755 matrixclass-student-helper.sh %{buildroot}$helper
install -D -m 0644 matrixclass-student-autostart.desktop %{buildroot}%{_sysconfdir}/xdg/autostart/matrixclass-student.desktop"
    extra_files="$helper
%config(noreplace) %{_sysconfdir}/xdg/autostart/matrixclass-student.desktop"
    scripts="%pre
$stop_student

%preun
if [ \"\$1\" = 0 ]; then
  $helper verify || exit 1
  $helper stop || :
fi

%posttrans
$helper launch || :
"
  fi
  cat > "$root/SPECS/matrixclass-$app.spec" <<EOF
%global debug_package %{nil}
%global _build_id_links none
%global __strip /bin/true
Name: matrixclass-$app
Version: $version
Release: 1
Summary: $summary
License: GPL-3.0-or-later
Source0: matrixclass-$version.tar.gz
Obsoletes: matrixclass < $version

%description
$desc

%prep
%setup -q -n matrixclass-linux-$arch

%install
install -D -m 0755 matrixclass-$app-linux-$arch %{buildroot}/opt/matrixclass/matrixclass-$app
mkdir -p %{buildroot}%{_bindir}
ln -s /opt/matrixclass/matrixclass-$app %{buildroot}%{_bindir}/matrixclass-$app
install -D -m 0644 matrixclass-$app.desktop %{buildroot}%{_datadir}/applications/matrixclass-$app.desktop
install -D -m 0644 matrixclass-$app.png %{buildroot}%{_datadir}/pixmaps/matrixclass-$app.png
$extra_install

$scripts
%files
%dir /opt/matrixclass
/opt/matrixclass/matrixclass-$app
%{_bindir}/matrixclass-$app
%{_datadir}/applications/matrixclass-$app.desktop
%{_datadir}/pixmaps/matrixclass-$app.png
$extra_files
EOF
  rpmbuild --define "_topdir $root" --target "$rpm_arch" -bb "$root/SPECS/matrixclass-$app.spec"
  find "$root/RPMS" -name '*.rpm' -exec cp {} "$output_dir/" \;
}

if command -v dpkg-deb >/dev/null 2>&1; then
  build_deb teacher
  build_deb student
else
  echo "Skipping .deb generation: dpkg-deb is unavailable."
fi

if command -v rpmbuild >/dev/null 2>&1; then
  build_rpm teacher
  build_rpm student
else
  echo "Skipping .rpm generation: rpmbuild is unavailable."
fi