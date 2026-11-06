#!/usr/bin/env python3
"""Public fixtures/mocks only; these are not real systemd CLI acceptance."""
import importlib.util
from pathlib import Path
import struct
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("offline_cli", Path(__file__).with_name("offline-v3-cli.py"))
offline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(offline)


class PublicGuards(unittest.TestCase):
    def test_every_negative_has_frozen_stage(self):
        groups = {
            "manager": {"foreign-activation"},
            "installation": {"missing-master", "wrong-master", "swapped-master", "swapped-both", "master-instance", "master-id", "master-version", "binding-canary", "m2-impersonation"},
            "identity": {"binding-instance", "binding-id", "binding-version", "backup-id", "backup-version", "expected-provider", "expected-instance", "expected-master", "expected-wrapping", "expected-master-version", "expected-wrapping-version"},
            "wrapping": {"missing-backup", "swapped-backup", "same-value", "backup-instance", "overlap-id"},
            "package": {"wrong-backup", "header", "ciphertext", "truncate-header", "truncate-body", "input-link"},
            "seed": {"seed-missing", "seed-unsafe", "seed-file-unsafe", "seed-link", "seed-hardlink", "seed-parent-link"},
            "target": {"target-exists", "target-link", "parent-link", "parent-replaced"},
        }
        self.assertEqual(set().union(*groups.values()), set(offline.CLI_NEGATIVES))
        for stage, cases in groups.items():
            for case in cases: self.assertEqual(offline.expected_stage(case), stage)

    def test_unique_bounded_cases(self):
        self.assertEqual(len(offline.CLI_CASES), len(set(offline.CLI_CASES)))
        self.assertTrue(set(offline.cross.NEGATIVES) - {"publish-binding", "publish-parent", "publish-credential"} <= set(offline.CLI_NEGATIVES))
        for case in offline.CLI_NEGATIVES:
            self.assertIn("cli-restore-" + case, offline.CLI_CASES)
            self.assertEqual("cli-verify-" + case in offline.CLI_CASES, case not in offline.RESTORE_ONLY)

    def test_arguments_use_only_public_identity(self):
        harness = offline.Harness.__new__(offline.Harness)
        harness.cli = Path("/public/cli")
        harness.sha = "a" * 40
        harness.materials = offline.cross.fixtures()
        for restore in (False, True):
            args = harness.cli_args(Path("/private/fixture"), restore)
            self.assertEqual(args[:2], [str(harness.cli), "systemd-v3-restore" if restore else "systemd-v3-verify"])
            names = set(args[2::2])
            self.assertEqual(names, {"--input", "--binding-seed", "--expect-provider", "--expect-instance", "--expect-master-id", "--expect-master-version", "--expect-wrapping-id", "--expect-wrapping-version", "--expect-source-version"} | ({"--data-dir"} if restore else set()))
            joined = " ".join(args)
            for value in offline.cross.same.KEYS: self.assertNotIn(value.hex(), joined)

    def test_workflow_native_and_closed(self):
        source = Path(__file__).parent.parent.joinpath(".github/workflows/offline-v3-cli.yml").read_text()
        self.assertEqual(source.count("ref: ${{ github.event.pull_request.head.sha || github.sha }}"), 2)
        self.assertEqual(source.count("cache: false"), 2)
        self.assertEqual(source.count("go build -ldflags"), 2)
        self.assertIn("runs-on: ubuntu-24.04-arm", source)
        self.assertNotIn("workflow_dispatch", source)
        self.assertIn("cancel-in-progress: false", source)
        self.assertIn("after.data.artifacts.some", source)
        self.assertNotIn("continue-on-error", source)

    def test_real_binary_has_no_acceptance_hook(self):
        root = Path(__file__).parent.parent
        for path in (root / "internal/offlinerecovery").glob("*.go"):
            if path.name.endswith("_test.go"): continue
            text = path.read_text()
            for word in ("CPA_CROSS", "cpacloud_crossvm_acceptance", "time.Sleep", "exec.Command", "service.Open(", "Initialize("):
                self.assertNotIn(word, text)

    def test_barrier_ignores_manager_then_requires_real_cli(self):
        barrier = offline.ParentBarrier.__new__(offline.ParentBarrier)
        barrier.fd, barrier.parent, barrier.package = 77, Path("/fixture/parent"), Path("/fixture/input")
        barrier.executable, barrier.identity = Path("/fixture/cli"), (500, 500)
        barrier.injected, barrier.failed = False, False
        barrier.libc = types.SimpleNamespace(fanotify_mark=mock.Mock(return_value=0))
        events = [struct.pack("=IBBHQii", 24, 3, 0, 24, 0x10000, 80, 2), struct.pack("=IBBHQii", 24, 3, 0, 24, 0x10000, 81, 123)]
        with mock.patch.object(offline.select, "select", return_value=([77], [], [])), mock.patch.object(offline.os, "read", side_effect=events), \
             mock.patch.object(offline.os, "readlink", side_effect=["/usr/lib/systemd/systemd", "/fixture/cli"]), \
             mock.patch.object(Path, "read_text", return_value="Uid:\t500\t500\t500\t500\n"), \
             mock.patch.object(offline.os, "rename") as rename, mock.patch.object(offline.cross, "private_dir"), \
             mock.patch.object(offline.cross, "write_private"), mock.patch.object(offline.os, "write") as write, mock.patch.object(offline.os, "close"):
            barrier.run()
        self.assertTrue(barrier.injected)
        self.assertFalse(barrier.failed)
        rename.assert_called_once()
        barrier.libc.fanotify_mark.assert_called_once()
        self.assertEqual([call.args[1] for call in write.call_args_list], [struct.pack("=iI",80,1), struct.pack("=iI",81,1)])

    def test_output_screening_and_cleanup_remain_independent(self):
        text = Path(__file__).with_name("offline-v3-cli.py").read_text()
        for required in ("super().receiver()", "super().cleanup()", "self.command(args, check=False", "--after-cursor=", "self.cli_started == self.cli_journals", "self.stop_unit(UNIT)", "not os.path.lexists(RUNTIME)", "barrier.finish()"):
            self.assertIn(required, text)

    def test_timeout_never_claims_complete_capture(self):
        harness = offline.Harness.__new__(offline.Harness)
        harness.capture_complete = True
        with mock.patch.object(offline.cross.Harness, "command", side_effect=offline.subprocess.TimeoutExpired("public-fixture", 1)):
            with self.assertRaises(offline.subprocess.TimeoutExpired): harness.command(["public-fixture"])
        self.assertFalse(harness.capture_complete)


if __name__ == "__main__":
    unittest.main()
