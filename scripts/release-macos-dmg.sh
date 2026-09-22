#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --source DIR --portable-archive FILE --portable-checksum FILE --output DIR --version TAG --arch amd64|arm64" >&2
  exit 2
}

source_root=""
portable_archive=""
portable_checksum=""
output_dir=""
version=""
arch=""
while (($#)); do
  case "$1" in
    --source) source_root=${2-}; shift 2 ;;
    --portable-archive) portable_archive=${2-}; shift 2 ;;
    --portable-checksum) portable_checksum=${2-}; shift 2 ;;
    --output) output_dir=${2-}; shift 2 ;;
    --version) version=${2-}; shift 2 ;;
    --arch) arch=${2-}; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$source_root" && -n "$portable_archive" && -n "$portable_checksum" && -n "$output_dir" && -n "$version" && -n "$arch" ]] || usage
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-preview\.[0-9]+$ ]] || { echo "invalid preview version: $version" >&2; exit 1; }
case "$arch" in
  amd64) swift_arch=x86_64 ;;
  arm64) swift_arch=arm64 ;;
  *) usage ;;
esac

source_root=$(cd "$source_root" && pwd -P)
portable_archive=$(cd "$(dirname "$portable_archive")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$portable_archive")")
portable_checksum=$(cd "$(dirname "$portable_checksum")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$portable_checksum")")
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd -P)

[[ -f "$portable_archive" ]] || { echo "portable archive not found: $portable_archive" >&2; exit 1; }
[[ -f "$portable_checksum" ]] || { echo "portable checksum not found: $portable_checksum" >&2; exit 1; }
[[ -f "$source_root/desktop/macos/Package.swift" ]] || { echo "desktop/macos/Package.swift is missing" >&2; exit 1; }
[[ -f "$source_root/desktop/macos/Info.plist" ]] || { echo "desktop/macos/Info.plist is missing" >&2; exit 1; }

expected_checksum=$(awk 'NF == 2 { print $1; exit }' "$portable_checksum")
expected_name=$(awk 'NF == 2 { print $2; exit }' "$portable_checksum")
[[ "$expected_checksum" =~ ^[a-fA-F0-9]{64}$ && "$expected_name" == "$(basename "$portable_archive")" ]] || {
  echo "portable checksum has an unexpected format or filename" >&2
  exit 1
}
actual_checksum=$(shasum -a 256 "$portable_archive" | awk '{print $1}')
[[ "$actual_checksum" == "$expected_checksum" ]] || { echo "portable archive checksum mismatch" >&2; exit 1; }

temp_base=${TMPDIR:-/tmp}
temp_base=${temp_base%/}
stage_root=$(mktemp -d "$temp_base/cpa-cloud-macos-installer.XXXXXXXX")
mount_dir=""
cleanup() {
  if [[ -n "$mount_dir" && -d "$mount_dir" ]]; then
    hdiutil detach "$mount_dir" -quiet >/dev/null 2>&1 || true
  fi
  case "$stage_root" in
    "$temp_base"/cpa-cloud-macos-installer.*) rm -rf -- "$stage_root" ;;
    *) echo "refusing to remove unsafe temporary path: $stage_root" >&2 ;;
  esac
}
trap cleanup EXIT

portable_extract="$stage_root/portable"
mkdir -p "$portable_extract"
tar -xzf "$portable_archive" -C "$portable_extract"
portable_root_count=$(find "$portable_extract" -mindepth 1 -maxdepth 1 -type d -print | wc -l | tr -d ' ')
[[ "$portable_root_count" == "1" ]] || { echo "portable archive must contain exactly one root directory" >&2; exit 1; }
portable_root=$(find "$portable_extract" -mindepth 1 -maxdepth 1 -type d -print | head -n 1)
for required in cpa-cloud web/index.html THIRD_PARTY_NOTICES.md THIRD-PARTY-LICENSES; do
  [[ -e "$portable_root/$required" ]] || { echo "portable archive is incomplete: missing $required" >&2; exit 1; }
done

package_dir="$source_root/desktop/macos"
swift build --package-path "$package_dir" -c release --arch "$swift_arch" --product CPACloudLauncher
swift_bin_dir=$(swift build --package-path "$package_dir" -c release --arch "$swift_arch" --show-bin-path)
launcher_binary="$swift_bin_dir/CPACloudLauncher"
[[ -x "$launcher_binary" ]] || { echo "launcher output is missing: $launcher_binary" >&2; exit 1; }

launcher_arches=$(lipo -archs "$launcher_binary")
case " $launcher_arches " in
  *" $swift_arch "*) ;;
  *) echo "launcher architecture mismatch: expected $swift_arch, found $launcher_arches" >&2; exit 1 ;;
esac
server_arches=$(lipo -archs "$portable_root/cpa-cloud")
case " $server_arches " in
  *" $swift_arch "*) ;;
  *) echo "server architecture mismatch: expected $swift_arch, found $server_arches" >&2; exit 1 ;;
esac

app="$stage_root/CPA Cloud.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/server" "$app/Contents/Resources/notices"
cp "$source_root/desktop/macos/Info.plist" "$app/Contents/Info.plist"
cp "$launcher_binary" "$app/Contents/MacOS/CPACloudLauncher"
cp "$portable_root/cpa-cloud" "$app/Contents/Resources/server/cpa-cloud"
ditto "$portable_root/web" "$app/Contents/Resources/web"
ditto "$portable_root/THIRD-PARTY-LICENSES" "$app/Contents/Resources/notices/THIRD-PARTY-LICENSES"
cp "$portable_root/THIRD_PARTY_NOTICES.md" "$app/Contents/Resources/notices/THIRD_PARTY_NOTICES.md"
for notice in GO-DEPENDENCIES.txt FRONTEND-DEPENDENCIES.txt BUILD-INFO.txt; do
  [[ ! -f "$portable_root/$notice" ]] || cp "$portable_root/$notice" "$app/Contents/Resources/notices/$notice"
done
chmod 755 "$app/Contents/MacOS/CPACloudLauncher" "$app/Contents/Resources/server/cpa-cloud"

plutil -lint "$app/Contents/Info.plist"
bundle_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$app/Contents/Info.plist")
[[ "$bundle_id" == "com.surpaimb.cpa-cloud.launcher" ]] || { echo "unexpected bundle identifier: $bundle_id" >&2; exit 1; }

otool_output=$(otool -L "$app/Contents/MacOS/CPACloudLauncher")
if grep -Eq '/Applications/Xcode|/Toolchains/|/\.build/' <<<"$otool_output"; then
  echo "launcher contains a dependency on a build-machine toolchain path" >&2
  exit 1
fi
printf '%s\n' "$otool_output" > "$app/Contents/Resources/notices/SWIFT-RUNTIME-DEPENDENCIES.txt"
cat > "$app/Contents/Resources/notices/DESKTOP-BUILD-INFO.txt" <<EOF
Version: $version
Target: macos/$arch ($swift_arch)
Launcher runtime boundary: Swift runtime libraries are not copied into the app; dependencies are recorded in SWIFT-RUNTIME-DEPENDENCIES.txt.
Code signing: not performed
Notarization: not performed
Validation: bundle structure, plist, architectures, dynamic library paths, DMG attach/readback
EOF

dmg_root="$stage_root/dmg-root"
mkdir -p "$dmg_root"
ditto "$app" "$dmg_root/CPA Cloud.app"
ln -s /Applications "$dmg_root/Applications"

dmg="$output_dir/cpa-cloud_${version}_macos_${arch}.dmg"
[[ ! -e "$dmg" && ! -e "$dmg.sha256" ]] || { echo "refusing to overwrite existing DMG or checksum: $dmg" >&2; exit 1; }
hdiutil create -quiet -volname "CPA Cloud" -srcfolder "$dmg_root" -format UDZO -ov "$dmg"
hdiutil verify "$dmg" >/dev/null

mount_dir="$stage_root/mount"
mkdir -p "$mount_dir"
hdiutil attach "$dmg" -readonly -nobrowse -mountpoint "$mount_dir" -quiet
[[ -x "$mount_dir/CPA Cloud.app/Contents/MacOS/CPACloudLauncher" ]] || { echo "DMG readback is missing the launcher" >&2; exit 1; }
[[ -x "$mount_dir/CPA Cloud.app/Contents/Resources/server/cpa-cloud" ]] || { echo "DMG readback is missing the server" >&2; exit 1; }
[[ -f "$mount_dir/CPA Cloud.app/Contents/Resources/web/index.html" ]] || { echo "DMG readback is missing the web console" >&2; exit 1; }
[[ -L "$mount_dir/Applications" && "$(readlink "$mount_dir/Applications")" == "/Applications" ]] || { echo "DMG readback is missing the Applications link" >&2; exit 1; }
hdiutil detach "$mount_dir" -quiet
mount_dir=""

dmg_checksum=$(shasum -a 256 "$dmg" | awk '{print $1}')
printf '%s  %s\n' "$dmg_checksum" "$(basename "$dmg")" > "$dmg.sha256"
echo "macOS DMG: $dmg"
