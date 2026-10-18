"""Public dummy/mock checks only. Never run systemd or real credential readers."""
import ast
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("crossvm", HERE / "cross-vm-backup.py")
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)


class CrossVMTests(unittest.TestCase):
    def harness(self, root, role="b"):
        with patch.dict(os.environ, {"RUNNER_TEMP": str(root)}):
            return h.Harness(role, __file__, str(root / "cpa-crossvm-relay"), "a" * 40)

    def test_provisioning_is_independent_and_roles_and_values_are_distinct(self):
        a, b = h.fixtures(), h.fixtures()
        self.assertEqual(a, b)
        master, backup = a["master-good"][1], a["backup-good"][1]
        self.assertNotEqual(master["master_key"], backup["wrapping_key"])
        self.assertNotEqual(master["key_id"], backup["key_id"])
        self.assertEqual(master["instance_id"], backup["instance_id"])
        self.assertEqual(a["master-swapped"][1], backup)
        self.assertEqual(a["backup-swapped"][1], master)
        self.assertEqual(a["backup-same"][1]["wrapping_key"], master["master_key"])
        self.assertEqual(a["backup-overlap"][1]["key_id"], master["key_id"])

    def test_all_negative_credentials_are_known_or_deliberately_missing(self):
        known = h.fixtures()
        for case in h.NEGATIVES:
            master, backup = h.credentials_for(case)
            self.assertTrue(master is None or master == "foreign-master" or master in known)
            self.assertTrue(backup is None or backup in known)
            if case != "foreign-activation":
                self.assertIn(h.rejection_stage(case), {"binding", "provider", "package", "parent", "publish", "target"})

    def test_capture_scans_actual_provisioned_documents_and_all_key_encodings(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            self.assertTrue(all(payload in fixture.oracles for payload in fixture.payloads.values()))
            for value in fixture.oracles:
                for encoded in h.forms(value):
                    fixture.content_match = False
                    fixture.capture(b"prefix" + encoded + b"suffix")
                    self.assertTrue(fixture.content_match)

    def test_timeout_preserves_original_error_and_independent_match_state(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            relay = io.StringIO()
            error = subprocess.TimeoutExpired("public-dummy", 1, output=h.SENTINELS[0])
            with patch.object(subprocess, "run", side_effect=error), contextlib.redirect_stdout(relay):
                with self.assertRaises(subprocess.TimeoutExpired):
                    fixture.action("restore", lambda: fixture.command(["public-dummy"]))
            self.assertEqual(fixture.first, ("restore", "timeout"))
            self.assertTrue(fixture.content_match)
            self.assertNotIn(h.SENTINELS[0].decode(), relay.getvalue())

    def test_first_operation_failure_survives_scan_and_multiple_cleanup_failures(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.preflight = Mock(side_effect=h.Failure("identity"))
            fixture.units.add("public-owned-unit")
            fixture.stop_unit = Mock(side_effect=h.Failure("cleanup"))
            fixture.content_match = True
            relay = io.StringIO()
            with contextlib.redirect_stdout(relay), self.assertRaises(h.Failure):
                fixture.main()
            self.assertEqual(fixture.first, ("preflight", "identity"))
            for marker in ("unit-cleanup failed", "scan-content matched", "scan-completeness incomplete",
                           "CROSSVM_FIRST b preflight identity"):
                self.assertIn(marker, relay.getvalue())
            self.assertNotIn("untrusted", relay.getvalue())

    def test_positive_restore_and_two_reopens_precede_every_negative_and_fail_stops(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.root = Path(directory)
            fixture.case_work = Mock(return_value=Path(directory))
            calls = []
            def run(case, *args):
                calls.append(case)
                if case == "wrong-master":
                    raise h.Failure("evidence")
            fixture.run_case = run
            with patch.object(h, "fingerprint", return_value={"public": "dummy"}), contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(h.Failure):
                    fixture.receiver()
            self.assertEqual(calls[:3], ["restore", "reopen-one", "reopen-two"])
            self.assertEqual(calls[3:], list(h.NEGATIVES[:h.NEGATIVES.index("wrong-master") + 1]))
            self.assertEqual(fixture.first, ("wrong-master", "evidence"))

    def test_missing_expected_journal_fails_completeness_even_if_process_is_stopped(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.units.add("public-owned-unit")
            fixture.stopped.add("public-owned-unit")
            fixture.stop_unit = Mock()
            relay = io.StringIO()
            with contextlib.redirect_stdout(relay), self.assertRaises(h.Failure):
                fixture.cleanup()
            self.assertIn("scan-completeness incomplete", relay.getvalue())
            self.assertIn("unit-cleanup ok", relay.getvalue())

    def test_unknown_unit_cleanup_does_not_remove_identity_or_data(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.root = Path(directory)
            fixture.user_created = True
            fixture.units.add("public-owned-unit")
            fixture.stop_unit = Mock(side_effect=OSError("untrusted"))
            fixture.command = Mock()
            with patch.object(h.shutil, "rmtree") as remove, contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(h.Failure):
                    fixture.cleanup()
            remove.assert_not_called()
            fixture.command.assert_not_called()

    def test_stop_failure_is_not_retried(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.stop_attempted.add("public-owned-unit")
            fixture.command = Mock()
            with self.assertRaises(h.Failure):
                fixture.stop_unit("public-owned-unit")
            fixture.command.assert_not_called()

    def test_relay_exact_allowlist_and_manifest_binding_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = {name: b"public-dummy" for name in h.ALLOWLIST}
            binding = dict(format_version=1, provider_kind="linux-systemd-credential-v1", instance_id=h.same.INSTANCE,
                           key_id="installation-root", key_version=1, canary_nonce="public", canary_ciphertext="public")
            values["binding.json"] = json.dumps(binding).encode()
            manifest = dict(sha="a" * 40, backup_format=3, credential_format=1, machine="a" * 32, boot="a" * 36)
            values["vm.json"] = json.dumps(manifest).encode()
            with patch.object(Path, "iterdir", return_value=[root / name for name in h.ALLOWLIST]), \
                    patch.object(h, "read_regular", side_effect=lambda path, maximum: values[path.name]):
                self.assertEqual(h.validate_relay(root, "a" * 40)[1], manifest)
                with self.assertRaises(h.Failure):
                    h.validate_relay(root, "b" * 40)
                values["vm.json"] = json.dumps({**manifest, "credential": "forbidden"}).encode()
                with self.assertRaises(h.Failure):
                    h.validate_relay(root, "a" * 40)
            with patch.object(Path, "iterdir", return_value=[root / name for name in h.ALLOWLIST | {"master.key"}]):
                with self.assertRaises(h.Failure):
                    h.validate_relay(root, "a" * 40)

    def test_relay_local_cleanup_rejects_unrelated_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.dict(os.environ, {"RUNNER_TEMP": str(root)}):
                with self.assertRaises(h.Failure):
                    h.remove_local_relay(root)
                with self.assertRaises(h.Failure):
                    h.remove_local_relay(root / "unrelated")
                h.remove_local_relay(root / "cpa-crossvm-relay")

    def test_stage_output_rejects_values_outside_fixed_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            with self.assertRaises(h.Failure):
                fixture.emit("private-path", "ok")
            with self.assertRaises(h.Failure):
                fixture.emit("restore", "private-secret")

    def test_go_exercises_actual_readers_record_policy_session_and_publication(self):
        source = (HERE.parent / "internal/service/cross_vm_backup_linux_test.go").read_text()
        for forbidden in ("linuxReadBackupCredential =", "readLinuxSystemdCredential =", "net.Listen(", "httptest.NewServer("):
            self.assertNotIn(forbidden, source)
        for required in ("validator(encoded)", 'calls == 2', 'os.Rename(path, filepath.Join(root, "staged-binding-before"))', 'os.Setenv("CREDENTIALS_DIRECTORY", path)',
                         "bcrypt.CompareHashAndPassword", "authorizeKeyPolicy", "crossSessionStatus(app) == 401", "unix.AF_INET6"):
            self.assertIn(required, source)
        seed = source.split("func crossSeedPolicy(", 1)[1].split("func crossPermissions(", 1)[0]
        for required in ('SourceMode: "all", SourceCIDRs: []string{}',
                         'AccountGroupMode: "all", AccountGroupIDs: []string{}'):
            self.assertIn(required, seed)

    def test_workflow_uses_two_fresh_jobs_no_cache_exact_checkout_and_final_deletion(self):
        source = (HERE.parent / ".github/workflows/cross-vm-backup.yml").read_text()
        self.assertEqual(source.count("ref: ${{ github.event.pull_request.head.sha || github.sha }}"), 2)
        self.assertEqual(source.count("cache: false"), 2)
        self.assertIn("needs: source-a", source)
        self.assertIn("needs: [source-a, restore-b]", source)
        self.assertIn("cancel-in-progress: false", source)
        self.assertIn("retention-days: 1", source)
        self.assertIn("github.rest.actions.deleteArtifact", source)
        self.assertIn("after.data.artifacts.some", source)
        self.assertNotIn("workflow_dispatch", source)
        self.assertNotIn("continue-on-error", source)
        self.assertNotIn("upload-artifact@", source.split("  restore-b:", 1)[1])

    def test_contract_links_and_all_shell_invocations_are_argument_lists(self):
        doc = HERE.parent / "docs/linux-cross-vm-backup-contract.md"
        for target in re.findall(r"\]\(([^)]+)\)", doc.read_text()):
            if not target.startswith("https://"):
                self.assertTrue((doc.parent / target).is_file(), target)
        tree = ast.parse((HERE / "cross-vm-backup.py").read_text())
        self.assertFalse(any(isinstance(node, ast.keyword) and node.arg == "shell" for node in ast.walk(tree)))

    def test_operation_error_is_not_masked_by_failed_journal_and_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.harness(Path(directory))
            fixture.identity = (1001, 1001)
            fixture.encrypted = Path(directory) / "public-credentials"
            fixture.executable = Path(directory) / "public-test"
            def command(args, **kwargs):
                if args[0] == "journalctl":
                    raise h.Failure("command")
                return Mock(returncode=1, stdout=b"not-found\n")
            fixture.command = command
            fixture.stop_unit = Mock(side_effect=h.Failure("cleanup"))
            relay = io.StringIO()
            with patch.object(h, "private_dir"), patch.object(os, "readlink", return_value="public-netns"), \
                    contextlib.redirect_stdout(relay):
                with self.assertRaises(h.Failure):
                    fixture.action("restore", lambda: fixture.run_case("restore", Path(directory)))
            self.assertEqual(fixture.first, ("restore", "evidence"))
            self.assertFalse(fixture.capture_complete)
            self.assertIn("journal-restore failed", relay.getvalue())
            self.assertIn("unit-restore failed", relay.getvalue())

    def test_journal_requires_actual_owned_unit_metadata_not_generic_output(self):
        self.assertTrue(h.journal_matches(b'{"UNIT":"public-owned.service","MESSAGE":"public dummy"}\n', "public-owned.service"))
        self.assertFalse(h.journal_matches(b'{"UNIT":"other.service","MESSAGE":"public-owned.service"}\n', "public-owned.service"))
        self.assertFalse(h.journal_matches(b"", "public-owned.service"))
        with self.assertRaises(json.JSONDecodeError):
            h.journal_matches(b"No journal files were found.\n-- No entries --\n", "public-owned.service")


if __name__ == "__main__":
    unittest.main()
