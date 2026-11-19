#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/linux}
arch=${2:-$(uname -m)}
case "$arch" in
  x86_64|amd64) arch=amd64; deb_arch=amd64; rpm_arch=x86_64 ;;
  aarch64|arm64) arch=arm64; deb_arch=arm64; rpm_arch=aarch64 ;;
  loongarch64|loong64) arch=loong64; deb_arch=loong64; rpm_arch=loongarch64 ;;
  *) echo "unsupported Linux architecture: $arch" >&2; exit 1 ;;
esac

version=${VERSION:-0.1.0}
stage="$output_dir/matrixclass-linux-$arch"
mkdir -p "$output_dir"

if command -v dpkg-deb >/dev/null 2>&1; then
  deb_root="$output_dir/matrixclass-deb-$arch"
  rm -rf "$deb_root"
  mkdir -p "$deb_root/DEBIAN" "$deb_root/opt/matrixclass" "$deb_root/usr/local/bin" "$deb_root/usr/share/applications" "$deb_root/usr/share/icons/hicolor/scalable/apps"
  install -m 0755 "$stage/matrixclass-teacher-linux-$arch" "$deb_root/opt/matrixclass/matrixclass-teacher"
  install -m 0755 "$stage/matrixclass-student-linux-$arch" "$deb_root/opt/matrixclass/matrixclass-student"
  ln -s /opt/matrixclass/matrixclass-teacher "$deb_root/usr/local/bin/matrixclass-teacher"
  ln -s /opt/matrixclass/matrixclass-student "$deb_root/usr/local/bin/matrixclass-student"
  install -m 0644 "$stage/matrixclass-teacher.desktop" "$stage/matrixclass-student.desktop" "$deb_root/usr/share/applications/"
  install -m 0644 "$stage/matrixclass.svg" "$deb_root/usr/share/icons/hicolor/scalable/apps/matrixclass.svg"
  cat > "$deb_root/DEBIAN/control" <<EOF
Package: matrixclass
Version: $version
Section: education
Priority: optional
Architecture: $deb_arch
Maintainer: MatrixClass
Description: MatrixClass classroom management software
EOF
  dpkg-deb --build "$deb_root" "$output_dir/matrixclass-$version-$arch.deb"
else
  echo "Skipping .deb generation: dpkg-deb is unavailable."
fi

if command -v rpmbuild >/dev/null 2>&1; then
  rpm_root="$output_dir/rpm-$arch"
  rm -rf "$rpm_root"
  mkdir -p "$rpm_root/SPECS" "$rpm_root/SOURCES"
  tar -czf "$rpm_root/SOURCES/matrixclass-$version.tar.gz" -C "$output_dir" "matrixclass-linux-$arch"
  cat > "$rpm_root/SPECS/matrixclass.spec" <<EOF
Name: matrixclass
Version: $version
Release: 1
Summary: MatrixClass classroom management software
License: GPL-3.0-or-later
BuildArch: $rpm_arch
Source0: matrixclass-$version.tar.gz

%description
MatrixClass classroom management software.

%install
mkdir -p %{buildroot}/opt/matrixclass
cp -a %{_sourcedir}/matrixclass-linux-$arch/* %{buildroot}/opt/matrixclass/

%files
/opt/matrixclass
EOF
  rpmbuild --define "_topdir $rpm_root" -bb "$rpm_root/SPECS/matrixclass.spec"
  find "$rpm_root/RPMS" -name '*.rpm' -exec cp {} "$output_dir/" \;
else
  echo "Skipping .rpm generation: rpmbuild is unavailable."
fi
