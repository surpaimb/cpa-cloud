"""Independent static/predicate tests; --linux-fs also uses only public files."""
import ast
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import sys
import tempfile
from types import SimpleNamespace
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
spec = importlib.util.spec_from_file_location("b0", HERE / "fixture.py")
b0 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b0)
SOURCE = (HERE / "fixture.py").read_text()
OWNER = (HERE / "owner.c").read_text()
LINUX_FS = "--linux-fs" in sys.argv
if LINUX_FS:
    sys.argv.remove("--linux-fs")
    if sys.platform != "linux":
        raise SystemExit("FAIL: linux-fs-required")


def info(**changes):
    fields = dict(st_mode=stat.S_IFREG | 0o600, st_uid=123, st_gid=456,
                  st_nlink=1, st_size=len(b0.PUBLIC), st_dev=7, st_ino=8)
    fields.update(changes)
    return SimpleNamespace(**fields)


class StaticTests(unittest.TestCase):
    def test_private_file_predicates(self):
        self.assertTrue(b0.file_ok(info(), 123, 456, len(b0.PUBLIC)))
        for changes in (dict(st_mode=stat.S_IFLNK | 0o600), dict(st_mode=stat.S_IFIFO | 0o600),
                        dict(st_mode=stat.S_IFREG | 0o644), dict(st_mode=stat.S_IFREG | 0o4600),
                        dict(st_uid=124), dict(st_gid=457), dict(st_nlink=2), dict(st_size=0)):
            self.assertFalse(b0.file_ok(info(**changes), 123, 456, len(b0.PUBLIC)))

    def test_private_directory_predicates(self):
        self.assertTrue(b0.directory_ok(info(st_mode=stat.S_IFDIR | 0o700), 123, 456))
        for mode in (stat.S_IFDIR | 0o755, stat.S_IFDIR | 0o2700, stat.S_IFREG | 0o700,
                     stat.S_IFLNK | 0o700):
            self.assertFalse(b0.directory_ok(info(st_mode=mode), 123, 456))
        self.assertFalse(b0.directory_ok(info(st_mode=stat.S_IFDIR | 0o700, st_uid=1), 123, 456))

    def test_no_provisioning_or_secret_calls(self):
        for forbidden in ('"--login"', '"--unlock"', '"--start"', '"--replace"',
                          '"CreateCollection"', '"CreateItem"', '"SetAlias"', '"OpenSession"',
                          '"Unlock"', '"GetSecret"', '"Delete"', '"Prompt"',
                          "secret_password_", "secret_item_", "socket.connect", "input="):
            self.assertNotIn(forbidden, SOURCE + OWNER)
        self.assertIn("stdin=subprocess.DEVNULL", SOURCE)
        self.assertIn("preflight.verify_isolation(metadata)", SOURCE)
        self.assertNotIn("bus_config] =", SOURCE)
        self.assertNotIn("interop/fixture", SOURCE)

    def test_owner_probe_has_only_bus_metadata(self):
        methods = re.findall(r'query\(bus, "([A-Za-z]+)"', OWNER)
        self.assertEqual(set(methods), {"NameHasOwner", "GetNameOwner"})
        self.assertIn('"GetConnectionUnixProcessID", "GetConnectionUnixUser"', OWNER)
        self.assertIn("G_DBUS_CALL_FLAGS_NO_AUTO_START", OWNER)
        self.assertIn('!absent(bus, "org.freedesktop.secrets") || !absent(bus, argv[3])', OWNER)

    def test_restart_order_and_fresh_identity(self):
        statements = ['first, first_owner = launch("first")', "stop(first, strict=True)",
                      "gone(first_owner)", 'second, second_owner = launch("second")',
                      "second.pid != first.pid and second_owner != first_owner",
                      "stop(second, strict=True)", "gone(second_owner)", "directory.remove()"]
        offsets = [SOURCE.index(value) for value in statements]
        self.assertEqual(offsets, sorted(offsets))
        self.assertIn("process.wait(timeout=5)", SOURCE)
        self.assertIn('not Path("/proc", str(process.pid)).exists()', SOURCE)

    def test_fixed_failure_labels(self):
        for node in ast.walk(ast.parse(SOURCE)):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "require":
                self.assertIsInstance(node.args[1], ast.Constant)
                label = node.args[1].value
                if label.startswith("b0-"):
                    self.assertIn(label, b0.FAILURES)
        self.assertEqual(len(b0.MARKERS), len(set(b0.MARKERS)))

    def test_exact_workflow_and_no_secret_fixture_trigger(self):
        workflow = (ROOT / ".github/workflows/secret-service-persistent-b0.yml").read_text()
        self.assertIn("ref: ${{ github.event.pull_request.head.sha || github.sha }}", workflow)
        self.assertIn('test "$(git rev-parse HEAD)" = "$EXPECTED_HEAD"', workflow)
        self.assertIn("test_fixture.py --linux-fs", workflow)
        self.assertIn("preflight/components.json", workflow)
        self.assertNotIn("workflow_dispatch", workflow)
        self.assertNotIn("continue-on-error", workflow)
        self.assertNotIn("upload-artifact", workflow)
        self.assertNotIn("interop/fixture.py", workflow)
        self.assertNotIn("cargo ", workflow)
        self.assertEqual(len(list(HERE.glob("*.py"))), 2)

    def test_exact_dependencies(self):
        data = json.loads((ROOT / "desktop/employee-config-switcher/preflight/components.json").read_text())
        self.assertEqual({k: v["version"] for k, v in data.items()}, {
            "dbus-daemon": "1.14.10-4ubuntu4.1", "gnome-keyring": "46.1-2ubuntu0.2",
            "libsecret-1-0": "0.21.4-1build3", "libsecret-1-dev": "0.21.4-1build3"})

    def test_markdown_relative_links(self):
        for document in HERE.glob("*.md"):
            for link in re.findall(r"\]\(([^)]+)\)", document.read_text()):
                if not link.startswith("https://"):
                    self.assertTrue((HERE / link.split("#")[0]).exists(), link)


if LINUX_FS:
    class FilesystemTests(unittest.TestCase):
        def setUp(self):
            self.temp = tempfile.TemporaryDirectory(prefix="cpa-b0-public-test-")
            self.work = Path(self.temp.name)
            self.work.chmod(0o700)
            (self.work / "data").mkdir(mode=0o700)
            self.fixture = None

        def tearDown(self):
            if self.fixture is not None:
                self.fixture.close()
            self.temp.cleanup()

        def create(self):
            self.fixture = b0.PublicDirectory(self.work, os.geteuid(), os.getegid())
            return self.fixture

        def test_public_preservation_and_removal(self):
            fixture = self.create()
            fixture.check()
            fixture.remove()
            self.assertEqual(list((self.work / "data").iterdir()), [])

        def test_symlink_parent_refused(self):
            (self.work / "data").rmdir()
            (self.work / "target").mkdir(mode=0o700)
            (self.work / "data").symlink_to(self.work / "target", target_is_directory=True)
            with self.assertRaises(OSError):
                self.create()
            self.assertEqual(list((self.work / "target").iterdir()), [])

        def test_existing_directory_refused(self):
            (self.work / "data/keyrings").mkdir(mode=0o700)
            with self.assertRaises(b0.preflight.PreflightFailure):
                self.create()

        def test_permissions_not_repaired(self):
            fixture = self.create()
            path = self.work / "data/keyrings" / b0.LEAF
            path.chmod(0o644)
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.remove()
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o644)
            self.assertEqual(path.read_bytes(), b0.PUBLIC)

        def test_symlink_sentinel_refused(self):
            fixture = self.create()
            path = self.work / "data/keyrings" / b0.LEAF
            outside = self.work / "public-target"
            outside.write_bytes(b0.PUBLIC)
            path.unlink()
            path.symlink_to(outside)
            with self.assertRaises(OSError):
                fixture.remove()
            self.assertTrue(path.is_symlink())
            self.assertEqual(outside.read_bytes(), b0.PUBLIC)

        def test_hardlink_refused(self):
            fixture = self.create()
            path = self.work / "data/keyrings" / b0.LEAF
            os.link(path, self.work / "public-hardlink")
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.check()

        def test_fifo_does_not_block(self):
            fixture = self.create()
            path = self.work / "data/keyrings" / b0.LEAF
            path.unlink()
            os.mkfifo(path, 0o600)
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.check()

        def test_unexpected_entry_blocks_cleanup(self):
            fixture = self.create()
            path = self.work / "data/keyrings/public-extra"
            path.write_bytes(b"public-extra")
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.remove()
            self.assertTrue(path.exists())

        def test_directory_replacement_refused(self):
            fixture = self.create()
            (self.work / "data/keyrings").rename(self.work / "retained")
            (self.work / "data/keyrings").mkdir(mode=0o700)
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.check()

        def test_content_corruption_refused(self):
            fixture = self.create()
            path = self.work / "data/keyrings" / b0.LEAF
            path.write_bytes(b"x" * len(b0.PUBLIC))
            with self.assertRaises(b0.preflight.PreflightFailure):
                fixture.check()


if __name__ == "__main__":
    unittest.main()
