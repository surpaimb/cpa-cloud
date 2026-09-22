#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --source DIR --portable-archive FILE --portable-checksum FILE --output DIR --version TAG --arch amd64|arm64 --appimagetool FILE --appimage-runtime FILE" >&2
  exit 2
}

source_root=""
portable_archive=""
portable_checksum=""
output_dir=""
version=""
arch=""
appimagetool=""
appimage_runtime=""
while (($#)); do
  case "$1" in
    --source) source_root=${2-}; shift 2 ;;
    --portable-archive) portable_archive=${2-}; shift 2 ;;
    --portable-checksum) portable_checksum=${2-}; shift 2 ;;
    --output) output_dir=${2-}; shift 2 ;;
    --version) version=${2-}; shift 2 ;;
    --arch) arch=${2-}; shift 2 ;;
    --appimagetool) appimagetool=${2-}; shift 2 ;;
    --appimage-runtime) appimage_runtime=${2-}; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$source_root" && -n "$portable_archive" && -n "$portable_checksum" && -n "$output_dir" && -n "$version" && -n "$arch" && -n "$appimagetool" && -n "$appimage_runtime" ]] || usage
[[ "$version" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)-preview\.([0-9]+)$ ]] || { echo "invalid preview version: $version" >&2; exit 1; }
major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
patch=${BASH_REMATCH[3]}
preview=${BASH_REMATCH[4]}
package_version="$major.$minor.$patch"
deb_version="$package_version~preview.$preview"
rpm_release="0.preview.$preview"
case "$arch" in
  amd64) elf_pattern='x86-64'; appimage_arch=x86_64; rpm_arch=x86_64 ;;
  arm64) elf_pattern='ARM aarch64'; appimage_arch=aarch64; rpm_arch=aarch64 ;;
  *) usage ;;
esac

source_root=$(cd "$source_root" && pwd -P)
portable_archive=$(cd "$(dirname "$portable_archive")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$portable_archive")")
portable_checksum=$(cd "$(dirname "$portable_checksum")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$portable_checksum")")
appimagetool=$(cd "$(dirname "$appimagetool")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$appimagetool")")
appimage_runtime=$(cd "$(dirname "$appimage_runtime")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$appimage_runtime")")
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd -P)

for required in "$portable_archive" "$portable_checksum" "$appimagetool" "$appimage_runtime" \
  "$source_root/packaging/linux/cpa-cloud-launcher" \
  "$source_root/packaging/linux/cpa-cloud-cli" \
  "$source_root/packaging/linux/cpa-cloud.desktop" \
  "$source_root/packaging/linux/cpa-cloud.svg" \
  "$source_root/packaging/linux/APPIMAGE-RUNTIME-LICENSE.txt"; do
  [[ -f "$required" ]] || { echo "required packaging input is missing: $required" >&2; exit 1; }
done
for command_name in file dpkg-deb rpmbuild rpm tar sha256sum; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "required packaging command is missing: $command_name" >&2; exit 1; }
done

expected_checksum=$(awk 'NF == 2 { print $1; exit }' "$portable_checksum")
expected_name=$(awk 'NF == 2 { print $2; exit }' "$portable_checksum")
[[ "$expected_checksum" =~ ^[a-fA-F0-9]{64}$ && "$expected_name" == "$(basename "$portable_archive")" ]] || {
  echo "portable checksum has an unexpected format or filename" >&2
  exit 1
}
actual_checksum=$(sha256sum "$portable_archive" | awk '{print $1}')
[[ "$actual_checksum" == "$expected_checksum" ]] || { echo "portable archive checksum mismatch" >&2; exit 1; }

temp_base=${TMPDIR:-/tmp}
temp_base=${temp_base%/}
stage_root=$(mktemp -d "$temp_base/cpa-cloud-linux-installers.XXXXXXXX")
cleanup() {
  case "$stage_root" in
    "$temp_base"/cpa-cloud-linux-installers.*) rm -rf -- "$stage_root" ;;
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
file "$portable_root/cpa-cloud" | grep -Fq "$elf_pattern" || { echo "server architecture does not match linux/$arch" >&2; exit 1; }

package_root="$stage_root/package-root"
install -Dm755 "$portable_root/cpa-cloud" "$package_root/usr/lib/cpa-cloud/cpa-cloud"
cp -a "$portable_root/web" "$package_root/usr/lib/cpa-cloud/web"
install -Dm755 "$source_root/packaging/linux/cpa-cloud-launcher" "$package_root/usr/lib/cpa-cloud/cpa-cloud-launcher"
install -Dm755 "$source_root/packaging/linux/cpa-cloud-cli" "$package_root/usr/bin/cpa-cloud"
install -Dm644 "$source_root/packaging/linux/cpa-cloud.desktop" "$package_root/usr/share/applications/cpa-cloud.desktop"
install -Dm644 "$source_root/packaging/linux/cpa-cloud.svg" "$package_root/usr/share/icons/hicolor/scalable/apps/cpa-cloud.svg"
mkdir -p "$package_root/usr/share/doc/cpa-cloud"
cp -a "$portable_root/THIRD-PARTY-LICENSES" "$package_root/usr/share/doc/cpa-cloud/THIRD-PARTY-LICENSES"
cp "$portable_root/THIRD_PARTY_NOTICES.md" "$package_root/usr/share/doc/cpa-cloud/THIRD_PARTY_NOTICES.md"
for notice in GO-DEPENDENCIES.txt FRONTEND-DEPENDENCIES.txt BUILD-INFO.txt README.md README.en.md; do
  [[ ! -f "$portable_root/$notice" ]] || cp "$portable_root/$notice" "$package_root/usr/share/doc/cpa-cloud/$notice"
done
cat > "$package_root/usr/share/doc/cpa-cloud/DESKTOP-PACKAGE-INFO.txt" <<EOF
Version: $version
Target: linux/$arch
Formats: AppImage, deb, rpm
Desktop launcher: terminal-hosted Bash launcher; local loopback service; xdg-open browser handoff
Code signing: not performed
Automatic updates: not implemented; use the platform package manager or replace the AppImage manually
User data: \${XDG_CONFIG_HOME:-\$HOME/.config}/cpa-cloud; not owned or removed by these packages
EOF

appdir="$stage_root/CPA_Cloud.AppDir"
mkdir -p "$appdir/usr/bin" "$appdir/usr/lib" "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/scalable/apps"
cp -a "$package_root/usr/lib/cpa-cloud" "$appdir/usr/lib/cpa-cloud"
cp -a "$package_root/usr/share/doc" "$appdir/usr/share/doc"
install -Dm755 "$source_root/packaging/linux/cpa-cloud-launcher" "$appdir/AppRun"
cat > "$appdir/usr/bin/cpa-cloud" <<'EOF'
#!/bin/sh
set -eu
exec "$APPDIR/AppRun" "$@"
EOF
chmod 755 "$appdir/usr/bin/cpa-cloud"
install -Dm644 "$source_root/packaging/linux/cpa-cloud.desktop" "$appdir/cpa-cloud.desktop"
sed -i 's|^Exec=.*|Exec=cpa-cloud|' "$appdir/cpa-cloud.desktop"
install -Dm644 "$source_root/packaging/linux/cpa-cloud.svg" "$appdir/cpa-cloud.svg"
cp "$source_root/packaging/linux/cpa-cloud.desktop" "$appdir/usr/share/applications/cpa-cloud.desktop"
sed -i 's|^Exec=.*|Exec=cpa-cloud|' "$appdir/usr/share/applications/cpa-cloud.desktop"
cp "$source_root/packaging/linux/cpa-cloud.svg" "$appdir/usr/share/icons/hicolor/scalable/apps/cpa-cloud.svg"
install -Dm644 "$source_root/packaging/linux/APPIMAGE-RUNTIME-LICENSE.txt" "$appdir/usr/share/doc/cpa-cloud/THIRD-PARTY-LICENSES/appimage-runtime/LICENSE.txt"

appimage="$output_dir/cpa-cloud_${version}_linux_${arch}.AppImage"
deb="$output_dir/cpa-cloud_${version}_linux_${arch}.deb"
rpm_output="$output_dir/cpa-cloud_${version}_linux_${arch}.rpm"
for output in "$appimage" "$deb" "$rpm_output"; do
  [[ ! -e "$output" && ! -e "$output.sha256" ]] || { echo "refusing to overwrite output: $output" >&2; exit 1; }
done

chmod +x "$appimagetool"
ARCH="$appimage_arch" VERSION="$package_version-preview.$preview" APPIMAGE_EXTRACT_AND_RUN=1 \
  "$appimagetool" --runtime-file "$appimage_runtime" "$appdir" "$appimage"
chmod 755 "$appimage"
appimage_readback="$stage_root/appimage-readback"
mkdir -p "$appimage_readback"
(cd "$appimage_readback" && "$appimage" --appimage-extract >/dev/null)
[[ -x "$appimage_readback/squashfs-root/AppRun" ]] || { echo "AppImage readback is missing AppRun" >&2; exit 1; }
[[ -x "$appimage_readback/squashfs-root/usr/lib/cpa-cloud/cpa-cloud" ]] || { echo "AppImage readback is missing the server" >&2; exit 1; }
[[ -f "$appimage_readback/squashfs-root/usr/lib/cpa-cloud/web/index.html" ]] || { echo "AppImage readback is missing the web console" >&2; exit 1; }

deb_root="$stage_root/deb-root"
cp -a "$package_root" "$deb_root"
mkdir -p "$deb_root/DEBIAN"
cat > "$deb_root/DEBIAN/control" <<EOF
Package: cpa-cloud
Version: $deb_version
Section: web
Priority: optional
Architecture: $arch
Maintainer: CPA Cloud Contributors
Depends: bash, curl, util-linux, xdg-utils
Description: Local CPA Cloud administration service and desktop launcher
 CPA Cloud runs a loopback-only service and opens its administration console
 in the default browser. User data remains outside the package-owned tree.
EOF
dpkg-deb --build --root-owner-group "$deb_root" "$deb" >/dev/null
dpkg-deb --info "$deb" >/dev/null
dpkg-deb --contents "$deb" | grep -q './usr/lib/cpa-cloud/cpa-cloud$' || { echo "deb readback is missing the server" >&2; exit 1; }

rpm_top="$stage_root/rpmbuild"
mkdir -p "$rpm_top"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
tar -C "$package_root" -czf "$rpm_top/SOURCES/cpa-cloud-root.tar.gz" .
cat > "$rpm_top/SPECS/cpa-cloud.spec" <<EOF
Name: cpa-cloud
Version: $package_version
Release: $rpm_release
Summary: Local CPA Cloud administration service and desktop launcher
License: Proprietary
BuildArch: $rpm_arch
Requires: bash, curl, util-linux, xdg-utils
Source0: cpa-cloud-root.tar.gz

%description
CPA Cloud runs a loopback-only service and opens its administration console
in the default browser. User data remains outside the package-owned tree.

%prep

%build

%install
mkdir -p %{buildroot}
tar -xzf %{SOURCE0} -C %{buildroot}

%files
/usr/bin/cpa-cloud
/usr/lib/cpa-cloud
/usr/share/applications/cpa-cloud.desktop
/usr/share/icons/hicolor/scalable/apps/cpa-cloud.svg
/usr/share/doc/cpa-cloud
EOF
rpmbuild -bb --define "_topdir $rpm_top" --target "$rpm_arch" "$rpm_top/SPECS/cpa-cloud.spec" >/dev/null
built_rpms=("$rpm_top"/RPMS/"$rpm_arch"/*.rpm)
[[ ${#built_rpms[@]} -eq 1 && -f "${built_rpms[0]}" ]] || { echo "expected exactly one built rpm" >&2; exit 1; }
cp "${built_rpms[0]}" "$rpm_output"
rpm -qpl "$rpm_output" | grep -q '/usr/lib/cpa-cloud/cpa-cloud$' || { echo "rpm readback is missing the server" >&2; exit 1; }

for output in "$appimage" "$deb" "$rpm_output"; do
  digest=$(sha256sum "$output" | awk '{print $1}')
  printf '%s  %s\n' "$digest" "$(basename "$output")" > "$output.sha256"
done

echo "Linux AppImage: $appimage"
echo "Linux deb: $deb"
echo "Linux rpm: $rpm_output"
