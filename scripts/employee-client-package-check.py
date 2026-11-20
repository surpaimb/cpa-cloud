#!/usr/bin/env python3
"""Fail-closed package and release checks for the employee desktop preview.

Provenance: independently authored for CPA Cloud from the project's preview
contract and public Tauri/GitHub CLI documentation. This script never runs the
installed application.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import struct
import subprocess
import sys
import tempfile
import time
import tomllib
from urllib.error import HTTPError
from urllib.request import Request, urlopen


TAG = "employee-config-v0.1.0-preview.1"
VERSION = "0.1.0-preview.1"
APP_VERSION = "0.1.0"
APP_IDENTIFIER = "com.cpa-cloud.employee-config"
APP_PRODUCT = "CPA 云端配置"
APP_EXECUTABLE = "cpa-cloud-employee-config"
WORKFLOW_PATH = ".github/workflows/employee-config-preview-package.yml"
RELEASE_SOURCE_FILES = {
    WORKFLOW_PATH,
    "desktop/employee-config-switcher/src-tauri/tauri.release.conf.json",
    "docs/releases/employee-config-v0.1.0-preview.1.md",
    "docs/releases/employee-config-v0.1.0-preview.1-provenance.md",
    "scripts/employee-client-notices.py",
    "scripts/employee-client-package-check.py",
    "scripts/employee-client-pnpm-shims.py",
    "scripts/test-employee-client-pnpm-shims.py",
    "tests/test_employee_client_notices_junction.py",
    "desktop/employee-config-switcher/scripts/employee-bundle-modules.mjs",
    "desktop/employee-config-switcher/scripts/native-evidence/openclaw-probe.test.mjs",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/alloc-stdlib-0.3.0-LICENSE.txt",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/block2-0.6.2-LICENSE-MIT.txt",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/block2-0.6.2-UPSTREAM-LICENSE.md",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/defmt-parser-1.0.0-LICENSE-APACHE.txt",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/defmt-parser-1.0.0-LICENSE-MIT.txt",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/selectors-0.38.0-MPL-2.0.txt",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/selectors-0.38.0-STYLO-README.md",
    "desktop/employee-config-switcher/third_party/notice-fallbacks/webview2-rs-LICENSE.txt",
}
ASSET_NAMES = {
    "macos": f"cpa-cloud-employee-config_{VERSION}_macos_universal.dmg",
    "windows": f"cpa-cloud-employee-config_{VERSION}_windows_amd64_Setup.exe",
}
MAX_NOTICE_FILES = 4096
MAX_NOTICE_BYTES = 64 * 1024 * 1024
REPO_ROOT = Path(__file__).resolve().parents[1]
NOTICE_SCRIPT = REPO_ROOT / "scripts" / "employee-client-notices.py"
WORKSPACE = REPO_ROOT / "desktop" / "employee-config-switcher"
SOURCE_CONFIG = WORKSPACE / "src-tauri" / "tauri.conf.json"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def verify_source_identity() -> None:
    config = json.loads(SOURCE_CONFIG.read_text(encoding="utf-8"))
    cargo = tomllib.loads((WORKSPACE / "src-tauri" / "Cargo.toml").read_text(encoding="utf-8"))
    require(config.get("identifier") == APP_IDENTIFIER, "Unexpected employee app identifier")
    require(config.get("productName") == APP_PRODUCT, "Unexpected employee app product name")
    require(config.get("version") == APP_VERSION, "Unexpected employee app version")
    require(cargo.get("package", {}).get("name") == APP_EXECUTABLE, "Unexpected employee app executable name")
    require(cargo.get("package", {}).get("version") == APP_VERSION, "Unexpected employee Cargo version")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(args: list[str], timeout: int = 180) -> str:
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, check=False)
    require(result.returncode == 0, f"Command failed ({result.returncode}): {args[0]}: {result.stderr[-2000:]}")
    return result.stdout.strip()


def notice_tree(root: Path) -> dict[str, str]:
    require(root.is_dir() and not root.is_symlink(), f"Missing notice directory: {root}")
    require((root / "manifest.json").is_file(), "Missing notices manifest")
    entries: dict[str, str] = {}
    total_bytes = 0
    for path in sorted(root.rglob("*")):
        require(not path.is_symlink(), f"Symlink in notice directory: {path}")
        if path.is_dir():
            continue
        require(path.is_file(), f"Unexpected notice entry: {path}")
        relative = path.relative_to(root).as_posix()
        require(relative not in entries, f"Duplicate notice path: {relative}")
        entries[relative] = sha256(path)
        total_bytes += path.stat().st_size
        require(len(entries) <= MAX_NOTICE_FILES, "Too many notice files")
        require(total_bytes <= MAX_NOTICE_BYTES, "Notice payload exceeds 64 MiB")
    return entries


def verify_notices(root: Path, expected_manifest_sha256: str,
                   bundle_modules: Path) -> dict[str, str]:
    require(NOTICE_SCRIPT.is_file(), "Required notice collector/validator is missing")
    require(bundle_modules.is_file() and not bundle_modules.is_symlink(),
            "Vite module evidence is missing or is a symlink")
    entries = notice_tree(root)
    require(entries["manifest.json"] == expected_manifest_sha256, "Notices manifest differs from build-time source")
    run([sys.executable, str(NOTICE_SCRIPT), "verify", "--root", str(root),
         "--workspace", str(WORKSPACE), "--bundle-modules", str(bundle_modules),
         "--expected-manifest-sha256", expected_manifest_sha256])
    return entries


def exactly_one(directory: Path, suffix: str) -> Path:
    require(directory.is_dir(), f"Bundle directory is missing: {directory}")
    matches = [path for path in directory.iterdir() if path.is_file() and path.name.lower().endswith(suffix)]
    require(len(matches) == 1, f"Expected exactly one {suffix} bundle; found {len(matches)}")
    return matches[0]


def check_embedded(source: dict[str, str], embedded_root: Path,
                   bundle_modules: Path) -> None:
    embedded = verify_notices(embedded_root, source["manifest.json"], bundle_modules)
    require(source == embedded, "Packaged notices differ from the collected source")


def check_macos(asset: Path, source: dict[str, str], bundle_modules: Path) -> None:
    with tempfile.TemporaryDirectory(prefix="cpa-employee-dmg-") as temp:
        mount = Path(temp) / "mount"
        mount.mkdir()
        attached = False
        try:
            run(["hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", str(mount), str(asset)], 90)
            attached = True
            apps = [path for path in mount.iterdir() if path.is_dir() and not path.is_symlink() and path.suffix == ".app"]
            require(len(apps) == 1, "DMG must contain exactly one app bundle")
            require(not (apps[0] / "Contents").is_symlink() and not (apps[0] / "Contents" / "Resources").is_symlink(), "App resource ancestry contains a symlink")
            with (apps[0] / "Contents" / "Info.plist").open("rb") as stream:
                info = plistlib.load(stream)
            require(info.get("CFBundleIdentifier") == APP_IDENTIFIER, "Incorrect macOS bundle identifier")
            require(info.get("CFBundleShortVersionString") == APP_VERSION, "Incorrect macOS app version")
            require(APP_PRODUCT in {info.get("CFBundleName"), info.get("CFBundleDisplayName")}, "Incorrect macOS app name")
            require(info.get("CFBundleExecutable") == APP_EXECUTABLE, "Incorrect macOS executable identity")
            macos_dir = apps[0] / "Contents" / "MacOS"
            binaries = [path for path in macos_dir.iterdir() if path.is_file() and os.access(path, os.X_OK)]
            require(len(binaries) == 1 and binaries[0].name == APP_EXECUTABLE, "App executable does not match source identity")
            architectures = set(run(["lipo", "-archs", str(binaries[0])]).split())
            require(architectures == {"x86_64", "arm64"}, f"Not a universal app: {architectures}")
            run(["codesign", "--verify", "--deep", "--strict", "--verbose=2", str(apps[0])], 90)
            signature = subprocess.run(["codesign", "-dv", "--verbose=4", str(apps[0])],
                                       capture_output=True, text=True, timeout=90, check=False)
            require(signature.returncode == 0 and "Signature=adhoc" in
                    (signature.stdout + signature.stderr), "macOS app is not ad-hoc signed")
            check_embedded(source, apps[0] / "Contents" / "Resources" / "release-notices", bundle_modules)
        finally:
            if attached:
                run(["hdiutil", "detach", str(mount)], 90)


def windows_uninstall_records() -> dict[str, dict[str, str]]:
    import winreg

    root = r"Software\Microsoft\Windows\CurrentVersion\Uninstall"
    records: dict[str, dict[str, str]] = {}
    with winreg.OpenKey(winreg.HKEY_CURRENT_USER, root, 0, winreg.KEY_READ) as parent:
        for index in range(winreg.QueryInfoKey(parent)[0]):
            name = winreg.EnumKey(parent, index)
            with winreg.OpenKey(parent, name, 0, winreg.KEY_READ) as entry:
                values: dict[str, str] = {}
                for field in ("DisplayName", "DisplayVersion", "InstallLocation", "UninstallString"):
                    try:
                        value, _ = winreg.QueryValueEx(entry, field)
                    except FileNotFoundError:
                        continue
                    if isinstance(value, str):
                        values[field] = value
                records[name] = values
    return records


def require_nsis_install_location(value: str | None, expected: Path) -> None:
    # Tauri v2.10.1 writes InstallLocation as one quoted $INSTDIR value.
    require(isinstance(value, str) and len(value) >= 3 and value[0] == '"' and
            value[-1] == '"' and value.count('"') == 2,
            "NSIS InstallLocation must have exactly one outer quote pair")
    location = Path(value[1:-1])
    require(location.is_absolute() and location.resolve() == expected.resolve(),
            "NSIS registered a different install location")


def check_windows(asset: Path, source: dict[str, str], bundle_modules: Path) -> None:
    runner_temp = Path(os.environ.get("RUNNER_TEMP", tempfile.gettempdir()))
    temp_context = tempfile.TemporaryDirectory(prefix="cpa-employee-nsis-", dir=runner_temp)
    operation_error: Exception | None = None
    try:
        temp = temp_context.name
        require(Path(temp).resolve().is_relative_to(runner_temp.resolve()), "Installer test escaped runner temp")
        install_dir = Path(temp) / "installed"
        require(not any(char.isspace() for char in str(install_dir)), "NSIS test path must have no spaces")
        before = windows_uninstall_records()
        registration: str | None = None
        install_succeeded = False
        primary_error: Exception | None = None
        try:
            result = subprocess.run([str(asset), "/S", f"/D={install_dir}"], capture_output=True, timeout=300, check=False)
            require(result.returncode == 0, f"NSIS installation failed: {result.returncode}")
            install_succeeded = True
            check_embedded(source, install_dir / "release-notices", bundle_modules)
            binaries = [path for path in install_dir.iterdir()
                        if path.is_file() and path.suffix.lower() == ".exe" and path.name.lower() != "uninstall.exe"]
            require(len(binaries) == 1 and binaries[0].name == f"{APP_EXECUTABLE}.exe",
                    "Installed app executable does not match source identity")
            after = windows_uninstall_records()
            created = {name: values for name, values in after.items() if name not in before}
            matching = {name: values for name, values in created.items()
                        if values.get("DisplayName") == APP_PRODUCT and
                        values.get("DisplayVersion") == APP_VERSION}
            require(len(matching) == 1, "NSIS did not register exactly one expected product/version")
            registration = next(iter(matching))
            require_nsis_install_location(matching[registration].get("InstallLocation"), install_dir)
            uninstall_command = matching[registration].get("UninstallString", "")
            require("uninstall.exe" in uninstall_command.lower(), "NSIS registration lacks its uninstaller")
            with binaries[0].open("rb") as binary:
                header = binary.read(64)
                require(header[:2] == b"MZ" and len(header) == 64, "Installed app is not PE")
                pe_offset = struct.unpack_from("<I", header, 0x3C)[0]
                require(64 <= pe_offset <= 1024 * 1024, "Invalid PE header offset")
                binary.seek(pe_offset)
                coff = binary.read(6)
            require(len(coff) == 6 and coff[:4] == b"PE\0\0", "Invalid PE header")
            require(struct.unpack_from("<H", coff, 4)[0] == 0x8664, "Installed app is not Windows x64")
        except Exception as error:
            primary_error = error
        cleanup_error: Exception | None = None
        try:
            uninstaller = install_dir / "uninstall.exe"
            if uninstaller.is_file():
                result = subprocess.run([str(uninstaller), "/S"], capture_output=True, timeout=180, check=False)
                require(result.returncode == 0, f"NSIS uninstall failed: {result.returncode}")
            else:
                require(not install_succeeded, "NSIS uninstaller is missing")
            for _ in range(40):
                after_cleanup = windows_uninstall_records()
                remaining = {name: values for name, values in after_cleanup.items()
                             if name not in before and values.get("DisplayName") == APP_PRODUCT and
                             values.get("DisplayVersion") == APP_VERSION}
                known_registration_remains = registration is not None and registration in after_cleanup
                if (not remaining and not known_registration_remains and
                        not (install_dir / f"{APP_EXECUTABLE}.exe").exists() and
                        not (install_dir / "release-notices").exists()):
                    break
                time.sleep(0.5)
            require(not remaining, "NSIS uninstall left its product registration")
            require(not known_registration_remains, "NSIS uninstall left the original registration key")
            require(not (install_dir / f"{APP_EXECUTABLE}.exe").exists(), "NSIS uninstall left the app executable")
            require(not (install_dir / "release-notices").exists(), "NSIS uninstall left packaged notices")
        except Exception as error:
            cleanup_error = error
        if primary_error is not None:
            if cleanup_error is not None:
                raise RuntimeError(f"{primary_error}; cleanup also failed: {cleanup_error}") from primary_error
            raise primary_error
        if cleanup_error is not None:
            raise cleanup_error
    except Exception as error:
        operation_error = error
    try:
        temp_context.cleanup()
    except Exception as temp_error:
        if operation_error is not None:
            raise RuntimeError(f"{operation_error}; temporary directory cleanup also failed: {temp_error}") from operation_error
        raise
    if operation_error is not None:
        raise operation_error


def verify_package(args: argparse.Namespace) -> None:
    require(args.tag == TAG, "Unexpected employee preview tag")
    require(re.fullmatch(r"[0-9a-f]{40}", args.source_sha) is not None, "Invalid source SHA")
    verify_source_identity()
    source_root = Path(args.notices_root)
    bundle_modules = Path(args.bundle_modules)
    source = verify_notices(source_root, sha256(source_root / "manifest.json"),
                            bundle_modules)
    platform = args.platform
    asset = exactly_one(Path(args.bundle_dir), ".dmg" if platform == "macos" else "-setup.exe")
    if platform == "macos":
        check_macos(asset, source, bundle_modules)
    else:
        check_windows(asset, source, bundle_modules)
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=False)
    output_asset = output / ASSET_NAMES[platform]
    shutil.copyfile(asset, output_asset)
    bundle_output = output / f"{platform}.bundle-modules.json"
    shutil.copyfile(bundle_modules, bundle_output)
    digest = sha256(output_asset)
    (output / f"{output_asset.name}.sha256").write_text(f"{digest}  {output_asset.name}\n", encoding="ascii")
    evidence = {
        "asset": output_asset.name,
        "asset_sha256": digest,
        "notice_manifest_sha256": source["manifest.json"],
        "notice_files": len(source),
        "bundle_modules_sha256": sha256(bundle_output),
        "app_identifier": APP_IDENTIFIER,
        "app_product": APP_PRODUCT,
        "app_version": APP_VERSION,
        "platform": platform,
        "source_sha": args.source_sha,
        "tag": args.tag,
    }
    (output / f"{platform}.evidence.json").write_text(json.dumps(evidence, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")


def verify_release_set(args: argparse.Namespace) -> None:
    verify_source_identity()
    root = Path(args.assets_dir)
    require(root.is_dir(), "Release asset directory is missing")
    wanted = {name for name in ASSET_NAMES.values()}
    wanted.update(f"{name}.sha256" for name in ASSET_NAMES.values())
    if args.with_evidence:
        wanted.update(f"{platform}.evidence.json" for platform in ASSET_NAMES)
        wanted.update(f"{platform}.bundle-modules.json" for platform in ASSET_NAMES)
    found = {path.name for path in root.iterdir() if path.is_file()}
    require(found == wanted, f"Unexpected release asset set: missing={wanted-found}, extra={found-wanted}")
    notice_manifest: str | None = None
    notice_files: int | None = None
    for platform, name in ASSET_NAMES.items():
        digest = sha256(root / name)
        sidecar = (root / f"{name}.sha256").read_text(encoding="ascii")
        require(sidecar == f"{digest}  {name}\n", f"Incorrect checksum sidecar: {name}")
        if args.with_evidence:
            evidence = json.loads((root / f"{platform}.evidence.json").read_text(encoding="utf-8"))
            bundle_path = root / f"{platform}.bundle-modules.json"
            bundle_evidence = json.loads(bundle_path.read_text(encoding="utf-8"))
            require(evidence == {
                "asset": name,
                "asset_sha256": digest,
                "notice_manifest_sha256": evidence.get("notice_manifest_sha256"),
                "notice_files": evidence.get("notice_files"),
                "bundle_modules_sha256": sha256(bundle_path),
                "app_identifier": APP_IDENTIFIER,
                "app_product": APP_PRODUCT,
                "app_version": APP_VERSION,
                "platform": platform,
                "source_sha": args.source_sha,
                "tag": TAG,
            }, f"Incorrect build evidence: {platform}")
            require(re.fullmatch(r"[0-9a-f]{64}", evidence["notice_manifest_sha256"]) is not None, "Invalid manifest hash")
            require(type(evidence["notice_files"]) is int and evidence["notice_files"] > 1, "Empty notices")
            require(bundle_evidence.get("schema") == 2 and
                    bundle_evidence.get("sourceCommit") == args.source_sha and
                    isinstance(bundle_evidence.get("chunks"), list) and
                    bool(bundle_evidence["chunks"]) and
                    all(isinstance(chunk, dict) and type(chunk.get("codeBytes")) is int and
                        chunk["codeBytes"] > 0 and isinstance(chunk.get("codeSha256"), str) and
                        re.fullmatch(r"[0-9a-f]{64}", chunk["codeSha256"]) is not None
                        for chunk in bundle_evidence["chunks"]) and
                    isinstance(bundle_evidence.get("distFiles"), list) and
                    bool(bundle_evidence["distFiles"]),
                    "Vite bundle evidence does not match the build source")
            if notice_manifest is None:
                notice_manifest = evidence["notice_manifest_sha256"]
                notice_files = evidence["notice_files"]
            else:
                require(evidence["notice_manifest_sha256"] == notice_manifest and
                        evidence["notice_files"] == notice_files, "Mac and Windows packaged different notice sets")


def verify_notices_source(args: argparse.Namespace) -> None:
    root = Path(args.notices_root)
    verify_notices(root, sha256(root / "manifest.json"), Path(args.bundle_modules))


def api_get(path: str) -> dict | list | None:
    token = os.environ.get("GH_TOKEN", "")
    require(bool(token), "GH_TOKEN is required")
    request = Request(
        f"https://api.github.com/repos/{os.environ['GH_REPO']}/{path}",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json"},
    )
    try:
        with urlopen(request, timeout=25) as response:
            return json.load(response)
    except HTTPError as error:
        if error.code == 404:
            return None
        raise


def require_merged_release_source(source_sha: str, repository: str) -> None:
    associated = api_get(f"commits/{source_sha}/pulls?per_page=100")
    require(isinstance(associated, list) and len(associated) < 100, "Cannot enumerate source-associated PRs")
    merged = [pr for pr in associated if pr.get("merged_at") and
              pr.get("merge_commit_sha") == source_sha and
              pr.get("base", {}).get("ref") == "main" and
              pr.get("base", {}).get("repo", {}).get("full_name") == repository and
              pr.get("head", {}).get("repo", {}).get("full_name") == repository]
    require(len(merged) == 1, "Main source is not exactly one merged repository PR")
    number = merged[0]["number"]
    pr = api_get(f"pulls/{number}")
    require(isinstance(pr, dict) and pr.get("merged_at") and
            pr.get("merge_commit_sha") == source_sha and pr.get("state") == "closed",
            "Merged PR details changed")
    head_sha = pr["head"]["sha"]
    require(re.fullmatch(r"[0-9a-f]{40}", head_sha) is not None, "Invalid merged PR head")
    changed = api_get(f"pulls/{number}/files?per_page=100")
    require(isinstance(changed, list) and len(changed) < 100 and
            len(changed) == pr["changed_files"], "Cannot enumerate complete merged PR file set")
    require({item["filename"] for item in changed} == RELEASE_SOURCE_FILES,
            "Merged PR does not contain exactly the reviewed package source set")
    runs = api_get(f"actions/runs?event=pull_request&head_sha={head_sha}&per_page=100")
    require(isinstance(runs, dict) and type(runs.get("total_count")) is int and
            runs["total_count"] < 100, "Cannot enumerate complete source PR checks")
    passed = [run for run in runs.get("workflow_runs", []) if
              run.get("path") == WORKFLOW_PATH and run.get("head_sha") == head_sha and
              run.get("event") == "pull_request" and run.get("status") == "completed" and
              run.get("conclusion") == "success" and run.get("run_attempt") == 1 and
              any(item.get("number") == number for item in run.get("pull_requests", []))]
    require(len(passed) == 1, "Merged release PR lacks one exact-head successful first-attempt package workflow")


def release_assets_identity(release: dict) -> dict[str, dict[str, int | str | None]]:
    assets = release.get("assets")
    require(isinstance(assets, list), "Release assets are not a list")
    expected = {name for name in ASSET_NAMES.values()}
    expected.update(f"{name}.sha256" for name in ASSET_NAMES.values())
    require(len(assets) == len(expected) and {asset.get("name") for asset in assets} == expected,
            "Release asset set differs from the two packages and two sidecars")
    identity: dict[str, dict[str, int | str | None]] = {}
    for asset in assets:
        require(type(asset.get("id")) is int and type(asset.get("size")) is int and
                asset["size"] > 0 and asset.get("state") == "uploaded",
                "Release asset lacks a stable uploaded identity")
        digest = asset.get("digest")
        require(digest is None or (isinstance(digest, str) and
                re.fullmatch(r"sha256:[0-9a-f]{64}", digest) is not None),
                "Invalid GitHub asset digest")
        identity[asset["name"]] = {"id": asset["id"], "size": asset["size"], "digest": digest}
    return identity


def require_published_tag(tag: dict | None, source_sha: str) -> None:
    require(isinstance(tag, dict) and tag.get("ref") == f"refs/tags/{TAG}",
            "Published tag ref is missing or incorrect")
    target = tag.get("object")
    require(isinstance(target, dict) and isinstance(target.get("sha"), str) and
            re.fullmatch(r"[0-9a-f]{40}", target["sha"]) is not None,
            "Published tag object is missing or malformed")
    if target.get("type") == "commit":
        require(target["sha"] == source_sha, "Published tag points to a different commit")
        return
    require(target.get("type") == "tag", "Published tag has an unexpected object type")
    annotated = api_get(f"git/tags/{target['sha']}")
    require(isinstance(annotated, dict) and annotated.get("tag") == TAG,
            "Annotated published tag is missing or incorrectly named")
    annotated_target = annotated.get("object")
    require(isinstance(annotated_target, dict) and annotated_target.get("type") == "commit" and
            annotated_target.get("sha") == source_sha,
            "Annotated published tag points to a different commit")


def release_by_exact_tag() -> dict | None:
    """Enumerate authenticated releases, including drafts, then re-read the unique ID."""
    matches: list[dict] = []
    for page in range(1, 21):
        releases = api_get(f"releases?per_page=100&page={page}")
        require(isinstance(releases, list) and len(releases) <= 100,
                "Cannot enumerate authenticated Releases")
        for release in releases:
            require(isinstance(release, dict), "Malformed Release listing")
            if release.get("tag_name") == TAG:
                matches.append(release)
        require(len(matches) <= 1, "Multiple Releases have the preview tag")
        if len(releases) < 100:
            break
    else:
        raise RuntimeError("Release listing exceeds the bounded pagination window")
    if not matches:
        return None
    release_id = matches[0].get("id")
    require(type(release_id) is int and release_id > 0,
            "Matching Release has no stable ID")
    release = api_get(f"releases/{release_id}")
    require(isinstance(release, dict) and release.get("id") == release_id and
            release.get("tag_name") == TAG, "Release ID readback differs from listing")
    return release


def release_guard(args: argparse.Namespace) -> None:
    require(args.tag == TAG, "Unexpected employee preview tag")
    require(re.fullmatch(r"[0-9a-f]{40}", args.source_sha) is not None, "Invalid source SHA")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", os.environ.get("GH_REPO", "")) is not None, "Invalid repository")
    main = api_get("git/ref/heads/main")
    require(main is not None and main["object"]["sha"] == args.source_sha, "Main HEAD moved or differs from build source")
    require_merged_release_source(args.source_sha, os.environ["GH_REPO"])
    tag = api_get(f"git/ref/tags/{TAG}")
    release = release_by_exact_tag()
    if args.stage == "pre":
        require(tag is None and release is None, "Tag or Release already exists; refusing overwrite")
        return
    if args.stage == "draft":
        require(tag is None, "A Git tag exists before draft publication")
    else:
        require_published_tag(tag, args.source_sha)
        by_tag = api_get(f"releases/tags/{TAG}")
        require(isinstance(release, dict) and isinstance(by_tag, dict) and
                by_tag.get("id") == release.get("id"),
                "Published Release tag lookup differs from the unique Release ID")
    require(isinstance(release, dict) and release.get("tag_name") == TAG and
            release.get("prerelease") is True, "Release identity mismatch")
    require(release.get("draft") is (args.stage == "draft"), "Unexpected Release draft state")
    require(release.get("target_commitish") == args.source_sha, "Release target differs from source commit")
    identity = release_assets_identity(release)
    if args.freeze_assets:
        require(args.stage == "draft" and not args.expected_assets, "Asset freeze is only allowed on a draft")
        path = Path(args.freeze_assets)
        require(not path.exists(), "Asset identity freeze already exists")
        path.write_text(json.dumps(identity, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
    if args.expected_assets:
        expected = json.loads(Path(args.expected_assets).read_text(encoding="utf-8"))
        require(identity == expected, "Release assets changed after the draft readback")


def main() -> None:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    package = commands.add_parser("verify-package")
    package.add_argument("--platform", choices=sorted(ASSET_NAMES), required=True)
    package.add_argument("--bundle-dir", required=True)
    package.add_argument("--notices-root", required=True)
    package.add_argument("--bundle-modules", required=True)
    package.add_argument("--output", required=True)
    package.add_argument("--source-sha", required=True)
    package.add_argument("--tag", required=True)
    release_set = commands.add_parser("verify-release-set")
    release_set.add_argument("--assets-dir", required=True)
    release_set.add_argument("--source-sha")
    release_set.add_argument("--with-evidence", action="store_true")
    source_notices = commands.add_parser("verify-notices-source")
    source_notices.add_argument("--notices-root", required=True)
    source_notices.add_argument("--bundle-modules", required=True)
    guard = commands.add_parser("release-guard")
    guard.add_argument("--stage", choices=["pre", "draft", "published"], required=True)
    guard.add_argument("--source-sha", required=True)
    guard.add_argument("--tag", required=True)
    guard.add_argument("--freeze-assets")
    guard.add_argument("--expected-assets")
    args = parser.parse_args()
    if args.command == "verify-package":
        verify_package(args)
    elif args.command == "verify-release-set":
        verify_release_set(args)
    elif args.command == "verify-notices-source":
        verify_notices_source(args)
    else:
        release_guard(args)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
        print(f"employee package verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
