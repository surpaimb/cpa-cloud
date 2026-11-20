"""Synthetic path-boundary checks for the employee notice collector.

Provenance: independently authored CPA Cloud fixtures using Python's standard
library. No package script, registry access, or product binary is involved.
"""

from __future__ import annotations

import importlib.util
import io
import json
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
