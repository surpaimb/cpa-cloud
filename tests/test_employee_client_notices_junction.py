"""Synthetic path-boundary checks for the employee notice collector.

Provenance: independently authored CPA Cloud fixtures using Python's standard
library. No package script, registry access, or product binary is involved.
"""

from __future__ import annotations

import importlib.util
import io
import json
import os
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "employee-client-notices.py"
SPEC = importlib.util.spec_from_file_location("employee_client_notices", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
notices = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(notices)

MANIFEST = json.dumps({"name": "synthetic-notice", "version": "1.0.0",
                       "license": "MIT"}, separators=(",", ":")).encode()
LICENSE = b"Synthetic full license text for path-boundary tests.\n"
FALLBACKS = SCRIPT.parents[1] / "desktop" / "employee-config-switcher" / "third_party" / "notice-fallbacks"
BLOCK2_DECLARATION_BYTES = (FALLBACKS / "block2-0.6.2-UPSTREAM-LICENSE.md").read_bytes()
MIT_TEMPLATE_BYTES = (FALLBACKS / "block2-0.6.2-LICENSE-MIT.txt").read_bytes()


def block2_archive(vcs: str, path: str) -> bytes:
    content = json.dumps({"git": {"sha1": vcs}, "path_in_vcs": path}).encode()
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        item = tarfile.TarInfo("block2-0.6.2/.cargo_vcs_info.json")
        item.size = len(content)
        tar.addfile(item, io.BytesIO(content))
    return buffer.getvalue()


def archive(files: dict[str, bytes], directories: set[str] | None = None) -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        for name in ["package", *(f"package/{d}" for d in sorted(directories or set()))]:
            entry = tarfile.TarInfo(name)
            entry.type = tarfile.DIRTYPE
            tar.addfile(entry)
        for name, data in sorted(files.items()):
            entry = tarfile.TarInfo(f"package/{name}")
            entry.size = len(data)
            tar.addfile(entry, io.BytesIO(data))
    return buffer.getvalue()


def junction(alias: Path, target: Path) -> None:
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(alias), str(target)],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0 or not alias.is_junction():
        raise AssertionError("native Windows junction fixture unavailable")
    info = alias.lstat()
    if not (getattr(info, "st_file_attributes", 0)
            & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)):
        raise AssertionError("junction fixture lacks reparse attribute")


class Block2FallbackTests(unittest.TestCase):
    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(prefix="cpa-block2-notice-")).resolve(strict=True)
        self.case_number = 0

    def cargo_case(self, *, label: str = "MIT",
                   repository: str = "https://github.com/madsmtm/objc2",
                   vcs: str = "b4167b582b2f75f9a1be75495c41b765344fd03c",
                   vcs_path: str = "crates/block2", checksum_valid: bool = True,
                   declaration: bytes = BLOCK2_DECLARATION_BYTES,
                   license_text: bytes = MIT_TEMPLATE_BYTES,
                   archive_texts: dict[str, bytes] | None = None) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / "block2-0.6.2"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        (source / "Cargo.toml").write_text("[package]\nname='block2'\nversion='0.6.2'\n")
        local = []
        for name, version in sorted(notices.FIRST_PARTY_CARGO):
            local_manifest = workspace / name / "Cargo.toml"
            local_manifest.parent.mkdir()
            local_manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": name, "version": version, "source": None,
                          "manifest_path": str(local_manifest)})
        data = block2_archive(vcs, vcs_path)
        (cache / "block2-0.6.2.crate").write_bytes(data)
        metadata = {"packages": local + [{
            "name": "block2", "version": "0.6.2", "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"),
            "license": label, "repository": repository,
        }]}
        locked = notices.sha(data) if checksum_valid else "0" * 64

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata), stderr="")

        committed = {
            notices.BLOCK2_DECLARATION: declaration,
            notices.VENDORED_CARGO[("block2", "0.6.2", "LICENSE-MIT.txt")]:
                license_text,
        }
        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "archive_files",
                          return_value=(archive_texts or {}, None)), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed.get(name, b"")):
            return notices.cargo_packages(workspace, {("block2", "0.6.2"): locked})[0]

    def test_block2_fallback_requires_two_distinct_sources(self) -> None:
        self.assertFalse(notices.has_authorization_text({
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
        }))
        package = self.cargo_case()
        self.assertEqual(package["notice_status"], "vendored_external")
        self.assertEqual(package["license"], "MIT")
        self.assertEqual(package["texts"], {
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
            "EXTERNAL_ORIGIN/LICENSE-MIT.txt": MIT_TEMPLATE_BYTES,
        })
        self.assertEqual([entry["role"] for entry in package["external_sources"]],
                         ["upstream-license-declaration", "full-license-text"])
        self.assertEqual([entry["path"] for entry in package["external_sources"]],
                         ["EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md",
                          "EXTERNAL_ORIGIN/LICENSE-MIT.txt"])
        self.assertIn(b"<year> <copyright holders>", MIT_TEMPLATE_BYTES)

    def test_block2_missing_or_changed_source_is_refused(self) -> None:
        for field in ("declaration", "license_text"):
            for bad in (b"", b"changed"):
                with self.subTest(field=field, bad=bad):
                    with self.assertRaisesRegex(notices.NoticeError,
                                                "vendored committed notice differs"):
                        self.cargo_case(**{field: bad})

    def test_block2_identity_and_archive_checksum_are_bound(self) -> None:
        cases = (
            ({"label": "Apache-2.0"}, "external license identity differs"),
            ({"repository": "https://example.invalid/objc2"},
             "external license identity differs"),
            ({"vcs": "0" * 40}, "archive VCS origin differs"),
            ({"vcs_path": "crates/other"}, "archive VCS origin differs"),
            ({"checksum_valid": False}, "archive checksum mismatch"),
        )
        for change, reason in cases:
            with self.subTest(change=change):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    self.cargo_case(**change)

    def test_existing_full_archive_text_does_not_need_fallback(self) -> None:
        package = self.cargo_case(archive_texts={"LICENSE": MIT_TEMPLATE_BYTES},
                                  declaration=b"", license_text=b"")
        self.assertEqual(package["notice_status"], "archive_text")
        self.assertEqual(package["texts"], {"LICENSE": MIT_TEMPLATE_BYTES})

    def test_full_synthetic_collect_verify_and_source_readback(self) -> None:
        package = self.cargo_case()
        workspace = self.scratch / "distribution-workspace"
        workspace.mkdir()
        output = self.scratch / "notice-output"
        source_names = [
            "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
        ]
        committed = {name: (name + "\n").encode() for name in source_names}
        committed[notices.BLOCK2_DECLARATION] = BLOCK2_DECLARATION_BYTES
        committed[notices.VENDORED_CARGO[("block2", "0.6.2", "LICENSE-MIT.txt")]] = (
            MIT_TEMPLATE_BYTES
        )
        cargo = {("block2", "0.6.2"): package["integrity"].removeprefix("sha256-")}

        def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[0], "git")
            if argv[-1] == "--show-toplevel":
                value = str(workspace)
            elif argv[-1] == "HEAD":
                value = "a" * 40
            elif "status" in argv:
                value = ""
            else:
                self.fail("unexpected synthetic Git operation")
            return SimpleNamespace(returncode=0, stdout=value, stderr="")

        with patch.object(notices.subprocess, "run", side_effect=fake_git), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed[name]), \
             patch.object(notices, "cargo_lock", return_value=cargo), \
             patch.object(notices, "pnpm_lock", return_value={}), \
             patch.object(notices, "pnpm_graph", return_value=([], {})), \
             patch.object(notices, "bundle_packages", return_value=set()), \
             patch.object(notices, "pnpm_packages", return_value=([], [])), \
             patch.object(notices, "cargo_packages", return_value=[package]):
            notices.collect(workspace, output, workspace / "synthetic-bundle")
            manifest_data = (output / "manifest.json").read_bytes()
            manifest = json.loads(manifest_data)
            prefix = "cargo/block2@0.6.2/EXTERNAL_ORIGIN/"
            self.assertEqual((output / (prefix + "UPSTREAM_DECLARATION.md")).read_bytes(),
                             BLOCK2_DECLARATION_BYTES)
            self.assertEqual((output / (prefix + "LICENSE-MIT.txt")).read_bytes(),
                             MIT_TEMPLATE_BYTES)
            self.assertEqual(manifest["source_files"][notices.BLOCK2_DECLARATION],
                             notices.sha(BLOCK2_DECLARATION_BYTES))
            notices.verify(output, workspace, notices.sha(manifest_data),
                           workspace / "synthetic-bundle")
            declaration_output = output / (prefix + "UPSTREAM_DECLARATION.md")
            declaration_output.write_bytes(b"tampered")
            with self.assertRaisesRegex(notices.NoticeError, "notice hash/size mismatch"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")
            declaration_output.write_bytes(BLOCK2_DECLARATION_BYTES)
            committed[notices.BLOCK2_DECLARATION] = b"changed"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "committed source file changed"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")


class NoticeJunctionTests(unittest.TestCase):
    def setUp(self) -> None:
        # Keep each isolated fixture for inspection; never remove a computed tree.
        self.scratch = Path(tempfile.mkdtemp(
            prefix="cpa-notice-junction-")).resolve(strict=True)
        self.root = self.scratch / "package"
        self.root.mkdir()

    def populate(self, files: dict[str, bytes]) -> None:
        for name, data in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)

    def basic(self) -> bytes:
        files = {"package.json": MANIFEST, "LICENSE": LICENSE}
        self.populate(files)
        return archive(files)

    def test_ordinary_package_and_readback(self) -> None:
        data = self.basic()
        texts, manifest = notices.archive_files(data, "package",
                                                installed_root=self.root)
        self.assertEqual(texts, {"LICENSE": LICENSE})
        self.assertEqual(manifest, MANIFEST)
        self.assertEqual(notices.source_file(self.root / "LICENSE", self.root),
                         LICENSE)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_canonical_package_root_alias_is_allowed(self) -> None:
        data = self.basic()
        alias = self.scratch / "root-alias"
        junction(alias, self.root)
        texts, manifest = notices.archive_files(data, "package",
                                                installed_root=alias)
        self.assertEqual(texts["LICENSE"], LICENSE)
        self.assertEqual(manifest, MANIFEST)
        self.assertEqual(notices.source_file(alias / "LICENSE", alias), LICENSE)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_archived_in_root_alias_license_is_refused(self) -> None:
        self.populate({"package.json": MANIFEST, "real/LICENSE": LICENSE})
        junction(self.root / "alias", self.root / "real")
        data = archive({"package.json": MANIFEST, "real/LICENSE": LICENSE,
                        "alias/LICENSE": LICENSE}, {"real", "alias"})
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.archive_files(data, "package", installed_root=self.root)
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.source_file(self.root / "alias" / "LICENSE", self.root)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_unarchived_junction_is_refused_by_inventory(self) -> None:
        self.populate({"package.json": MANIFEST, "real/LICENSE": LICENSE})
        junction(self.root / "extra", self.root / "real")
        data = archive({"package.json": MANIFEST, "real/LICENSE": LICENSE},
                       {"real"})
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.archive_files(data, "package", installed_root=self.root)

    def test_synthetic_symlink_to_same_license_is_refused(self) -> None:
        data = self.basic()
        target = self.scratch / "unvisited-symlink-target"
        hits = 0
        original = Path.is_symlink

        def is_symlink(path: Path) -> bool:
            nonlocal hits
            if path == target:
                hits += 1
                return True
            return original(path)

        with patch.object(Path, "is_symlink", is_symlink):
            texts, manifest = notices.archive_files(
                data, "package", installed_root=self.root)
            self.assertEqual(texts, {"LICENSE": LICENSE})
            self.assertEqual(manifest, MANIFEST)
            self.assertEqual(hits, 0)
            target = self.root / "LICENSE"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "reparse path refused"):
                notices.archive_files(data, "package", installed_root=self.root)
            self.assertGreater(hits, 0, "synthetic symlink target was not visited")

    def test_synthetic_windows_reparse_attribute_is_refused(self) -> None:
        data = self.basic()
        target = self.scratch / "unvisited-reparse-target"
        hits = 0
        original = Path.lstat

        def lstat(path: Path, *args: object, **kwargs: object) -> object:
            nonlocal hits
            info = original(path, *args, **kwargs)
            if path != target:
                return info
            hits += 1
            return SimpleNamespace(st_mode=info.st_mode, st_size=info.st_size,
                                   st_file_attributes=0x400)

        with patch.object(Path, "lstat", lstat):
            texts, manifest = notices.archive_files(
                data, "package", installed_root=self.root)
            self.assertEqual(texts, {"LICENSE": LICENSE})
            self.assertEqual(manifest, MANIFEST)
            self.assertEqual(hits, 0)
            target = self.root / "LICENSE"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "reparse path refused"):
                notices.archive_files(data, "package", installed_root=self.root)
            self.assertGreater(hits, 0, "synthetic reparse target was not visited")

    def test_walk_error_is_not_silently_ignored(self) -> None:
        data = self.basic()

        def failed_walk(*_args: object, **kwargs: object) -> object:
            kwargs["onerror"](OSError("synthetic scandir failure"))
            yield from ()

        with patch.object(notices.os, "walk", failed_walk):
            with self.assertRaisesRegex(notices.NoticeError,
                                        "directory inventory traversal failed"):
                notices.archive_files(data, "package", installed_root=self.root)

    def test_nested_node_modules_remains_rejected(self) -> None:
        data = self.basic()
        nested = self.root / "node_modules" / ".bin"
        nested.mkdir(parents=True)
        (nested / "generated").write_text("synthetic", encoding="utf-8")
        with self.assertRaisesRegex(notices.NoticeError,
                                    "nested node_modules in installed package"):
            notices.archive_files(data, "package", installed_root=self.root)


if __name__ == "__main__":
    unittest.main()
