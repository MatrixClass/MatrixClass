#!/usr/bin/env sh
# Builds .deb (dpkg-deb) and .rpm (rpmbuild) packages from an existing
# matrixclass-linux-<arch> stage directory (see build-linux-package.sh).
# Missing tools are skipped with a message.
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

if command -v dpkg-deb >/dev/null 2>&1; then
  deb_root="$output_dir/matrixclass-deb-$arch"
  rm -rf "$deb_root"
  mkdir -p "$deb_root/DEBIAN" "$deb_root/opt/matrixclass" "$deb_root/usr/bin" "$deb_root/usr/share/applications" "$deb_root/usr/share/icons/hicolor/scalable/apps" "$deb_root/usr/share/pixmaps"
  install -m 0755 "$stage/matrixclass-teacher-linux-$arch" "$deb_root/opt/matrixclass/matrixclass-teacher"
  install -m 0755 "$stage/matrixclass-student-linux-$arch" "$deb_root/opt/matrixclass/matrixclass-student"
  ln -s /opt/matrixclass/matrixclass-teacher "$deb_root/usr/bin/matrixclass-teacher"
  ln -s /opt/matrixclass/matrixclass-student "$deb_root/usr/bin/matrixclass-student"
  install -m 0644 "$stage/matrixclass-teacher.desktop" "$stage/matrixclass-student.desktop" "$deb_root/usr/share/applications/"
  install -m 0644 "$stage/matrixclass.svg" "$deb_root/usr/share/icons/hicolor/scalable/apps/matrixclass.svg"
  install -m 0644 "$stage/matrixclass-student.png" "$deb_root/usr/share/pixmaps/matrixclass-student.png"
  install -m 0644 "$stage/matrixclass-teacher.png" "$deb_root/usr/share/pixmaps/matrixclass-teacher.png"
  cat > "$deb_root/DEBIAN/control" <<EOF
Package: matrixclass
Version: $version
Section: education
Priority: optional
Architecture: $deb_arch
Maintainer: MatrixClass
Recommends: libx11-6, libxtst6, libgl1
Description: MatrixClass LAN classroom management software
 Teacher console and student client for LAN electronic classrooms.
EOF
  dpkg-deb --root-owner-group --build "$deb_root" "$output_dir/matrixclass-$version-$arch.deb" 2>/dev/null \
    || dpkg-deb --build "$deb_root" "$output_dir/matrixclass-$version-$arch.deb"
else
  echo "Skipping .deb generation: dpkg-deb is unavailable."
fi

if command -v rpmbuild >/dev/null 2>&1; then
  rpm_root=$(cd "$output_dir" && pwd)/rpm-$arch
  rm -rf "$rpm_root"
  mkdir -p "$rpm_root/SPECS" "$rpm_root/SOURCES"
  tar -czf "$rpm_root/SOURCES/matrixclass-$version.tar.gz" -C "$output_dir" "matrixclass-linux-$arch"
  cat > "$rpm_root/SPECS/matrixclass.spec" <<EOF
%global debug_package %{nil}
%global _build_id_links none
%global __strip /bin/true
Name: matrixclass
Version: $version
Release: 1
Summary: MatrixClass LAN classroom management software
License: GPL-3.0-or-later
Source0: matrixclass-$version.tar.gz

%description
Teacher console and student client for LAN electronic classrooms.

%prep
%setup -q -n matrixclass-linux-$arch

%install
install -D -m 0755 matrixclass-teacher-linux-$arch %{buildroot}/opt/matrixclass/matrixclass-teacher
install -D -m 0755 matrixclass-student-linux-$arch %{buildroot}/opt/matrixclass/matrixclass-student
mkdir -p %{buildroot}%{_bindir}
ln -s /opt/matrixclass/matrixclass-teacher %{buildroot}%{_bindir}/matrixclass-teacher
ln -s /opt/matrixclass/matrixclass-student %{buildroot}%{_bindir}/matrixclass-student
install -D -m 0644 matrixclass-teacher.desktop %{buildroot}%{_datadir}/applications/matrixclass-teacher.desktop
install -D -m 0644 matrixclass-student.desktop %{buildroot}%{_datadir}/applications/matrixclass-student.desktop
install -D -m 0644 matrixclass.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/matrixclass.svg
install -D -m 0644 matrixclass-student.png %{buildroot}%{_datadir}/pixmaps/matrixclass-student.png
install -D -m 0644 matrixclass-teacher.png %{buildroot}%{_datadir}/pixmaps/matrixclass-teacher.png

%files
/opt/matrixclass
%{_bindir}/matrixclass-teacher
%{_bindir}/matrixclass-student
%{_datadir}/applications/matrixclass-teacher.desktop
%{_datadir}/applications/matrixclass-student.desktop
%{_datadir}/icons/hicolor/scalable/apps/matrixclass.svg
%{_datadir}/pixmaps/matrixclass-student.png
%{_datadir}/pixmaps/matrixclass-teacher.png
EOF
  rpmbuild --define "_topdir $rpm_root" --target "$rpm_arch" -bb "$rpm_root/SPECS/matrixclass.spec"
  find "$rpm_root/RPMS" -name '*.rpm' -exec cp {} "$output_dir/" \;
else
  echo "Skipping .rpm generation: rpmbuild is unavailable."
fi
