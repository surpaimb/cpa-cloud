#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --source DIR --portable-amd64-archive FILE --portable-amd64-checksum FILE --portable-arm64-archive FILE --portable-arm64-checksum FILE --output DIR --version TAG" >&2
  exit 2
}

source_root=""
amd64_archive=""
amd64_checksum=""
arm64_archive=""
arm64_checksum=""
output_dir=""
version=""
while (($#)); do
  case "$1" in
    --source) source_root=${2-}; shift 2 ;;
    --portable-amd64-archive) amd64_archive=${2-}; shift 2 ;;
    --portable-amd64-checksum) amd64_checksum=${2-}; shift 2 ;;
    --portable-arm64-archive) arm64_archive=${2-}; shift 2 ;;
    --portable-arm64-checksum) arm64_checksum=${2-}; shift 2 ;;
    --output) output_dir=${2-}; shift 2 ;;
    --version) version=${2-}; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$source_root" && -n "$amd64_archive" && -n "$amd64_checksum" && -n "$arm64_archive" && -n "$arm64_checksum" && -n "$output_dir" && -n "$version" ]] || usage
[[ "$version" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)-preview\.([0-9]+)$ ]] || { echo "invalid preview version: $version" >&2; exit 1; }
short_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
bundle_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] * 1000 + BASH_REMATCH[4]))"

absolute_file() {
  local input=$1
  (cd "$(dirname "$input")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$input")")
}

source_root=$(cd "$source_root" && pwd -P)
amd64_archive=$(absolute_file "$amd64_archive")
amd64_checksum=$(absolute_file "$amd64_checksum")
arm64_archive=$(absolute_file "$arm64_archive")
arm64_checksum=$(absolute_file "$arm64_checksum")
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd -P)

[[ -f "$source_root/desktop/macos/Package.swift" ]] || { echo "desktop/macos/Package.swift is missing" >&2; exit 1; }
[[ -f "$source_root/desktop/macos/Info.plist" ]] || { echo "desktop/macos/Info.plist is missing" >&2; exit 1; }

temp_base=${TMPDIR:-/tmp}
temp_base=${temp_base%/}
stage_root=$(mktemp -d "$temp_base/cpa-cloud-macos-universal.XXXXXXXX")
mount_dir=""
cleanup() {
  if [[ -n "$mount_dir" && -d "$mount_dir" ]]; then
    hdiutil detach "$mount_dir" -quiet >/dev/null 2>&1 || true
  fi
  case "$stage_root" in
    "$temp_base"/cpa-cloud-macos-universal.*) rm -rf -- "$stage_root" ;;
    *) echo "refusing to remove unsafe temporary path: $stage_root" >&2 ;;
  esac
}
trap cleanup EXIT

portable_root=""
extract_portable() {
  local archive=$1 checksum=$2 destination=$3 swift_arch=$4
  [[ -f "$archive" && -f "$checksum" ]] || { echo "portable input is missing for $swift_arch" >&2; exit 1; }
  local expected_checksum expected_name actual_checksum root_count root
  expected_checksum=$(awk 'NF == 2 { print $1; exit }' "$checksum")
  expected_name=$(awk 'NF == 2 { print $2; exit }' "$checksum")
  [[ "$expected_checksum" =~ ^[a-fA-F0-9]{64}$ && "$expected_name" == "$(basename "$archive")" ]] || {
    echo "portable checksum has an unexpected format or filename for $swift_arch" >&2
    exit 1
  }
  actual_checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
  [[ "$actual_checksum" == "$expected_checksum" ]] || { echo "portable archive checksum mismatch for $swift_arch" >&2; exit 1; }
  mkdir -p "$destination"
  tar -xzf "$archive" -C "$destination"
  root_count=$(find "$destination" -mindepth 1 -maxdepth 1 -type d -print | wc -l | tr -d ' ')
  [[ "$root_count" == "1" ]] || { echo "portable archive must contain exactly one root directory for $swift_arch" >&2; exit 1; }
  root=$(find "$destination" -mindepth 1 -maxdepth 1 -type d -print | head -n 1)
  for required in cpa-cloud web/index.html THIRD_PARTY_NOTICES.md THIRD-PARTY-LICENSES; do
    [[ -e "$root/$required" ]] || { echo "portable archive is incomplete for $swift_arch: missing $required" >&2; exit 1; }
  done
  local server_arches
  server_arches=$(lipo -archs "$root/cpa-cloud")
  [[ " $server_arches " == *" $swift_arch "* ]] || { echo "server architecture mismatch: expected $swift_arch, found $server_arches" >&2; exit 1; }
  portable_root=$root
}

extract_portable "$amd64_archive" "$amd64_checksum" "$stage_root/portable-amd64" x86_64
amd64_root=$portable_root
extract_portable "$arm64_archive" "$arm64_checksum" "$stage_root/portable-arm64" arm64
arm64_root=$portable_root

diff -qr "$amd64_root/web" "$arm64_root/web" >/dev/null || { echo "web payload differs between macOS architectures" >&2; exit 1; }
diff -qr "$amd64_root/THIRD-PARTY-LICENSES" "$arm64_root/THIRD-PARTY-LICENSES" >/dev/null || { echo "license payload differs between macOS architectures" >&2; exit 1; }
cmp -s "$amd64_root/THIRD_PARTY_NOTICES.md" "$arm64_root/THIRD_PARTY_NOTICES.md" || { echo "third-party notice differs between macOS architectures" >&2; exit 1; }

package_dir="$source_root/desktop/macos"
swift build --package-path "$package_dir" -c release --arch x86_64 --product CPACloudLauncher
x64_bin_dir=$(swift build --package-path "$package_dir" -c release --arch x86_64 --show-bin-path)
cp "$x64_bin_dir/CPACloudLauncher" "$stage_root/CPACloudLauncher-x86_64"
swift build --package-path "$package_dir" -c release --arch arm64 --product CPACloudLauncher
arm64_bin_dir=$(swift build --package-path "$package_dir" -c release --arch arm64 --show-bin-path)
cp "$arm64_bin_dir/CPACloudLauncher" "$stage_root/CPACloudLauncher-arm64"
lipo -create "$stage_root/CPACloudLauncher-x86_64" "$stage_root/CPACloudLauncher-arm64" -output "$stage_root/CPACloudLauncher-universal"
lipo -create "$amd64_root/cpa-cloud" "$arm64_root/cpa-cloud" -output "$stage_root/cpa-cloud-universal"

for binary in "$stage_root/CPACloudLauncher-universal" "$stage_root/cpa-cloud-universal"; do
  arches=$(lipo -archs "$binary")
  [[ " $arches " == *' x86_64 '* && " $arches " == *' arm64 '* ]] || { echo "universal binary is missing an architecture: $binary ($arches)" >&2; exit 1; }
done

app="$stage_root/CPA Cloud.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/server" "$app/Contents/Resources/notices"
cp "$source_root/desktop/macos/Info.plist" "$app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $short_version" "$app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $bundle_version" "$app/Contents/Info.plist"
cp "$stage_root/CPACloudLauncher-universal" "$app/Contents/MacOS/CPACloudLauncher"
cp "$stage_root/cpa-cloud-universal" "$app/Contents/Resources/server/cpa-cloud"
ditto "$amd64_root/web" "$app/Contents/Resources/web"
ditto "$amd64_root/THIRD-PARTY-LICENSES" "$app/Contents/Resources/notices/THIRD-PARTY-LICENSES"
cp "$amd64_root/THIRD_PARTY_NOTICES.md" "$app/Contents/Resources/notices/THIRD_PARTY_NOTICES.md"
for notice in GO-DEPENDENCIES.txt FRONTEND-DEPENDENCIES.txt; do
  [[ ! -f "$amd64_root/$notice" ]] || cp "$amd64_root/$notice" "$app/Contents/Resources/notices/$notice"
done
[[ ! -f "$amd64_root/BUILD-INFO.txt" ]] || cp "$amd64_root/BUILD-INFO.txt" "$app/Contents/Resources/notices/PORTABLE-BUILD-INFO-amd64.txt"
[[ ! -f "$arm64_root/BUILD-INFO.txt" ]] || cp "$arm64_root/BUILD-INFO.txt" "$app/Contents/Resources/notices/PORTABLE-BUILD-INFO-arm64.txt"
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
Target: macos/universal (x86_64, arm64)
Launcher runtime boundary: Swift runtime libraries are not copied into the app; dependencies are recorded in SWIFT-RUNTIME-DEPENDENCIES.txt.
Code signing: not performed
Notarization: not performed
Validation: bundle structure, plist, universal architectures, dynamic library paths, ZIP and DMG readback
EOF

dmg="$output_dir/cpa-cloud_${version}_macos_universal.dmg"
zip="$output_dir/cpa-cloud_${version}_macos_universal.zip"
for output in "$dmg" "$zip"; do
  [[ ! -e "$output" && ! -e "$output.sha256" ]] || { echo "refusing to overwrite output: $output" >&2; exit 1; }
done

ditto -c -k --sequesterRsrc --keepParent "$app" "$zip"
zip_readback="$stage_root/zip-readback"
mkdir -p "$zip_readback"
ditto -x -k "$zip" "$zip_readback"
[[ -x "$zip_readback/CPA Cloud.app/Contents/MacOS/CPACloudLauncher" ]] || { echo "ZIP readback is missing the launcher" >&2; exit 1; }

dmg_root="$stage_root/dmg-root"
mkdir -p "$dmg_root"
ditto "$app" "$dmg_root/CPA Cloud.app"
ln -s /Applications "$dmg_root/Applications"
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

for output in "$dmg" "$zip"; do
  checksum=$(shasum -a 256 "$output" | awk '{print $1}')
  printf '%s  %s\n' "$checksum" "$(basename "$output")" > "$output.sha256"
done

echo "macOS Universal DMG: $dmg"
echo "macOS Universal ZIP: $zip"
