#!/usr/bin/env sh
set -eu

output_dir=${1:-dist/darwin}
version=${MATRIXCLASS_VERSION:-0.1.0}
skip_build=${SKIP_BUILD:-0}
skip_dmg=${SKIP_DMG:-0}

if [ "$skip_build" != "1" ]; then
  "$(dirname "$0")/build-darwin.sh" "$output_dir"
fi

mkdir -p "$output_dir/apps/amd64" "$output_dir/apps/arm64"

create_app() {
  arch=$1
  role=$2
  display_name=$3
  bundle_id=$4
  executable="matrixclass-$role"
  app_path="$output_dir/apps/$arch/$display_name.app"
  contents_path="$app_path/Contents"
  mkdir -p "$contents_path/MacOS" "$contents_path/Resources"
  cp "$output_dir/matrixclass-$role-darwin-$arch" "$contents_path/MacOS/$executable"
  cp packaging/darwin/README.md "$contents_path/Resources/README.md"
  cp packaging/darwin/permissions.md "$contents_path/Resources/permissions.md"
  cat > "$contents_path/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDisplayName</key><string>$display_name</string>
    <key>CFBundleExecutable</key><string>$executable</string>
    <key>CFBundleIdentifier</key><string>$bundle_id</string>
    <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
    <key>CFBundleName</key><string>$display_name</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>$version</string>
    <key>CFBundleVersion</key><string>$version</string>
    <key>LSMinimumSystemVersion</key><string>12.3</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSMicrophoneUsageDescription</key><string>MatrixClass 使用麦克风进行课堂音频通信。</string>
    <key>NSScreenCaptureUsageDescription</key><string>MatrixClass 使用屏幕录制权限进行课堂屏幕广播和监控。</string>
</dict>
</plist>
EOF
}

create_app amd64 teacher "MatrixClass Teacher" com.matrixclass.teacher
create_app amd64 student "MatrixClass Student" com.matrixclass.student
create_app arm64 teacher "MatrixClass Teacher" com.matrixclass.teacher
create_app arm64 student "MatrixClass Student" com.matrixclass.student

universal_created=0
if command -v lipo >/dev/null 2>&1; then
  mkdir -p "$output_dir/apps/universal"
  for role in teacher student; do
    if [ "$role" = teacher ]; then display_name="MatrixClass Teacher"; else display_name="MatrixClass Student"; fi
    executable="matrixclass-$role"
    app_path="$output_dir/apps/universal/$display_name.app"
    contents_path="$app_path/Contents"
    mkdir -p "$contents_path/MacOS" "$contents_path/Resources"
    lipo -create "$output_dir/matrixclass-$role-darwin-amd64" "$output_dir/matrixclass-$role-darwin-arm64" -output "$contents_path/MacOS/$executable"
    cp "$output_dir/apps/amd64/$display_name.app/Contents/Info.plist" "$contents_path/Info.plist"
    cp packaging/darwin/README.md "$contents_path/Resources/README.md"
    cp packaging/darwin/permissions.md "$contents_path/Resources/permissions.md"
  done
  universal_created=1
else
  printf 'lipo not found; skipping Universal 2 apps. Run this step on macOS.\n'
fi

cp packaging/darwin/README.md "$output_dir/README-macOS.md"
cp packaging/darwin/permissions.md "$output_dir/permissions-macOS.md"

if [ "$skip_dmg" != "1" ] && [ "$universal_created" = "1" ] && command -v hdiutil >/dev/null 2>&1; then
  dmg_root="$output_dir/dmg"
  rm -rf "$dmg_root"
  mkdir -p "$dmg_root"
  cp -R "$output_dir/apps/universal/"*.app "$dmg_root/"
  cp "$output_dir/README-macOS.md" "$dmg_root/"
  cp "$output_dir/permissions-macOS.md" "$dmg_root/"
  hdiutil create -volname MatrixClass -srcfolder "$dmg_root" -ov -format UDZO "$output_dir/MatrixClass-macOS-unsigned.dmg"
else
  printf 'hdiutil or Universal 2 apps unavailable; skipping DMG.\n'
fi

printf 'Darwin packaging completed in %s\n' "$output_dir"
