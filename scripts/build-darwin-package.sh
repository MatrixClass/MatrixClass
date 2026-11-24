#!/usr/bin/env sh
# Creates macOS .app bundles (amd64, arm64, Universal 2) and zip packages.
# On macOS it can additionally ad-hoc sign (SIGN_ADHOC=1) and build a DMG.
# Usage: scripts/build-darwin-package.sh [output-dir]
#   env: MATRIXCLASS_VERSION (default 0.1.0)
#        SKIP_BUILD=1   reuse binaries already in output-dir
#        SIGN_ADHOC=1   run `codesign --force --deep -s -` (requires macOS codesign)
#        SKIP_DMG=1     do not build a DMG even if hdiutil is available
set -eu
. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

output_dir=$(abs_path "${1:-dist/darwin}")
version=$(mc_version)
packaging="$repo_root/packaging/darwin"

if [ "${SKIP_BUILD:-0}" != 1 ]; then
  "$repo_root/scripts/build-darwin.sh" "$output_dir"
fi

if [ "${SIGN_ADHOC:-0}" = 1 ] && ! command -v codesign >/dev/null 2>&1; then
  echo "SIGN_ADHOC=1 requires codesign (macOS)." >&2
  exit 1
fi

# create_app <arch> <role> <display name> <bundle id>
create_app() {
  arch=$1 role=$2 display_name=$3 bundle_id=$4
  executable="matrixclass-$role"
  app="$output_dir/apps/$arch/$display_name.app"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$output_dir/matrixclass-$role-darwin-$arch" "$app/Contents/MacOS/$executable"
  chmod 0755 "$app/Contents/MacOS/$executable"
  cp "$packaging/README.md" "$packaging/permissions.md" "$app/Contents/Resources/"
  icon_file="MatrixClass Student.png"
  [ "$role" = teacher ] && icon_file="MatrixClass Teacher.png"
  cp "$packaging/$icon_file" "$app/Contents/Resources/$icon_file"
  if [ -f "$packaging/MatrixClass.icns" ]; then cp "$packaging/MatrixClass.icns" "$app/Contents/Resources/"; fi
  icon_entry="    <key>CFBundleIconFile</key><string>$icon_file</string>\n"
  sed -e "s|@DISPLAY_NAME@|$display_name|g" -e "s|@EXECUTABLE@|$executable|g" \
      -e "s|@BUNDLE_ID@|$bundle_id|g" -e "s|@VERSION@|$version|g" \
      "$packaging/Info.plist.in" | awk -v icon="$icon_entry" '{ if (index($0, "@ICON_ENTRY@")) { sub(/@ICON_ENTRY@/, ""); printf "%s", icon } print }' \
      > "$app/Contents/Info.plist"
  printf 'APPL????' > "$app/Contents/PkgInfo"
  if [ "${SIGN_ADHOC:-0}" = 1 ]; then
    codesign --force --deep -s - "$app"
    codesign --verify --deep --strict "$app"
  fi
  printf '  app %s\n' "$app"
}

cp "$packaging/README.md" "$output_dir/README-macOS.md"
cp "$packaging/permissions.md" "$output_dir/permissions-macOS.md"

for arch in amd64 arm64 universal; do
  create_app "$arch" teacher "MatrixClass Teacher" com.matrixclass.teacher
  create_app "$arch" student "MatrixClass Student" com.matrixclass.student
  stage_name="matrixclass-macos-$arch"
  stage="$output_dir/$stage_name"
  rm -rf "$stage"
  mkdir -p "$stage"
  cp -R "$output_dir/apps/$arch/MatrixClass Teacher.app" "$output_dir/apps/$arch/MatrixClass Student.app" "$stage/"
  cp "$output_dir/README-macOS.md" "$output_dir/permissions-macOS.md" "$packaging/uninstall-student.sh" "$stage/"
  chmod 0755 "$stage/uninstall-student.sh"
  if [ "$(uname -s)" = Darwin ] && command -v ditto >/dev/null 2>&1; then
    # ditto keeps extended attributes and code signatures exactly as Finder expects.
    rm -f "$output_dir/$stage_name.zip"
    (cd "$output_dir" && ditto -c -k --sequesterRsrc --keepParent "$stage_name" "$stage_name.zip")
    printf '  archive %s\n' "$output_dir/$stage_name.zip"
  else
    make_archive "$output_dir/$stage_name.zip" "$output_dir" "$stage_name"
  fi
done

if [ "${SKIP_DMG:-0}" != 1 ] && command -v hdiutil >/dev/null 2>&1; then
  dmg_root="$output_dir/dmg"
  dmg="$output_dir/matrixclass-macos-universal.dmg"
  rm -rf "$dmg_root" "$dmg"
  mkdir -p "$dmg_root"
  cp -R "$output_dir/apps/universal/"*.app "$dmg_root/"
  cp "$output_dir/README-macOS.md" "$output_dir/permissions-macOS.md" "$dmg_root/"
  ln -s /Applications "$dmg_root/Applications"
  hdiutil create -volname "MatrixClass $version" -srcfolder "$dmg_root" -ov -format UDZO "$dmg"
  if [ "${SIGN_ADHOC:-0}" = 1 ]; then codesign --force -s - "$dmg"; fi
  printf '  dmg %s\n' "$dmg"
else
  printf 'hdiutil unavailable or SKIP_DMG=1; skipping DMG (built only on macOS).\n'
fi

printf 'Darwin packaging completed in %s\n' "$output_dir"
