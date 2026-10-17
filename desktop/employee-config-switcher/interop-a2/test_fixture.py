"""Independent A2 orchestration/scanner guards, no real service operations."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("a2_fixture", HERE / "fixture.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
C = (HERE / "duplicate.c").read_text()
RUST = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service_fixture.rs").read_text()


class A2Tests(unittest.TestCase):
    def run_cases(self, fail_at=None):
        calls, env, output = [], {}, io.StringIO()

        def call(args, environment, expected, label, outputs):
            calls.append((args[0], args[-1] if args[0] != "cpa" else environment["CPA_SS_FIXTURE_MODE"]))
            if len(calls) == fail_at:
                raise module.base.PreflightFailure(label)

        with patch.object(module, "bounded_call", side_effect=call), contextlib.redirect_stdout(output):
            try:
                module.cases(dict(cpa="cpa", verifier="verifier", a2_helper="helper"), env, [], lambda: None)
            except module.base.PreflightFailure:
                pass
        return calls, output.getvalue()

    def test_exact_positive_injection_proof_rejection_postcheck_order(self):
        calls, output = self.run_cases()
        self.assertEqual(calls, [("verifier", "setup"), ("cpa", "create"), ("verifier", "verify"),
                                ("cpa", "reopen"), ("verifier", "verify"), ("helper", "inject"),
                                ("helper", "verify-two"), ("cpa", "duplicate-open"), ("helper", "verify-two"),
                                ("cpa", "duplicate-create"), ("helper", "verify-two")])
        self.assertIn("A2_CREATE_UNAVAILABLE_UNCHANGED_OK", output)

    def test_injection_or_existence_failure_never_runs_rejection(self):
        for index in (6, 7):
            calls, output = self.run_cases(index)
            self.assertEqual(len(calls), index)
            self.assertNotIn("A2_TWO_REAL_ITEMS_OK", output)
            self.assertNotIn("A2_OPEN_CORRUPT_UNCHANGED_OK", output)

    def test_case_time_budget_reserves_cleanup_before_outer_deadline(self):
        with patch.object(module.time, "monotonic", side_effect=[0, 19]), \
                patch.object(module, "bounded_call") as call:
            with self.assertRaises(module.base.PreflightFailure):
                module.cases(dict(cpa="cpa", verifier="verifier", a2_helper="helper"), {}, [], lambda: None)
            call.assert_not_called()

    def test_each_failed_rejection_or_postcheck_stops_without_next_marker(self):
        for index in (8, 9, 10, 11):
            calls, output = self.run_cases(index)
            self.assertEqual(len(calls), index)
            self.assertNotIn("A2_CREATE_UNAVAILABLE_UNCHANGED_OK", output)
            if index <= 9:
                self.assertNotIn("A2_OPEN_CORRUPT_UNCHANGED_OK", output)

    def test_bounded_failure_and_timeout_preserve_output_without_relay(self):
        for result in (Mock(returncode=1, stdout=b"private-untrusted-output"),
                       subprocess.TimeoutExpired("private-path", 12, output=b"private-untrusted-output")):
            outputs, relay = [], io.StringIO()
            config = {"side_effect": result} if isinstance(result, Exception) else {"return_value": result}
            with patch.object(module.base, "run", **config), contextlib.redirect_stdout(relay):
                with self.assertRaises(module.base.PreflightFailure):
                    module.bounded_call([], {}, b"expected", "a2-inject-failed", outputs)
            self.assertEqual(outputs, [b"private-untrusted-output"])
            self.assertNotIn("private-untrusted-output", relay.getvalue())

    def test_both_keys_and_envelopes_all_scanner_encodings(self):
        samples = {name: bytes((index + i + 37) % 256 for i in range(size))
                   for index, (name, size) in enumerate(module.SAMPLES)}
        with patch.object(module.volatile, "read_sample", side_effect=lambda path, size: samples[path.name]):
            self.assertTrue(module.scan(HERE, [b"fixed-safe-label"]))
            for value in samples.values():
                for form in module.volatile.secret_forms(value):
                    with self.assertRaises(module.base.PreflightFailure):
                        module.scan(HERE, [b"prefix" + form + b"suffix"])

    def test_incomplete_or_invalid_sample_still_scans_other_actual_samples(self):
        second = b"public-dummy-second-key-32-bytes!!"
        for failure in (None, OSError()):
            def read(path, size):
                if path.name == "sent.key":
                    if isinstance(failure, Exception):
                        raise failure
                    return failure
                return second if path.name == "duplicate.sent.key" else b"x" * size
            with patch.object(module.volatile, "read_sample", side_effect=read):
                self.assertFalse(module.scan(HERE, []))
                with self.assertRaises(module.base.PreflightFailure):
                    module.scan(HERE, [second.hex().encode()])

    def test_cleanup_attempts_every_owned_child_and_scan_even_if_stop_fails(self):
        children, handles, order = [Mock(), Mock()], [Mock()], []
        def stop(child):
            order.append(child)
            if child is children[1]:
                raise OSError()
        with tempfile.TemporaryDirectory() as directory, patch.object(module, "stop", side_effect=stop), \
                patch.object(module, "scan", return_value=True) as scan, contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(module.base.PreflightFailure):
                module.cleanup(Path(directory), children, handles, [])
            self.assertEqual(order, list(reversed(children)))
            scan.assert_called_once()
            handles[0].close.assert_called_once()

    def test_second_samples_precede_encoding_and_only_false_create(self):
        start = C.index("static gboolean inject(")
        body = C[start:C.index("static gboolean verify_pair(")]
        self.assertLess(body.index('save_sample(dir, "duplicate.sent.key"'), body.index("secret_service_encode_dbus_secret"))
        self.assertLess(body.index('save_sample(dir, "duplicate.sent.envelope"'), body.index("secret_service_encode_dbus_secret"))
        self.assertEqual(body.count('"CreateItem"'), 1)
        self.assertIn('&properties, encoded, FALSE)', body)
        self.assertIn('strcmp(prompt, "/") == 0', body)
        self.assertIn("G_DBUS_CALL_FLAGS_NO_AUTO_START, 5000", body)
        self.assertNotIn("get_secret", body)
        self.assertNotIn("search_for", body)

    def test_private_oracles_exclusive_prewrite_fd_and_postwrite_entry_checks(self):
        body = C[C.index("static gboolean save_sample("):C.index("static gboolean read_sample(")]
        self.assertIn("O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600", body)
        self.assertLess(body.index("private_file(&before, 0)"), body.index("write(fd, bytes, length)"))
        for fragment in ("fsync(fd)", "fsync(dir)", "AT_SYMLINK_NOFOLLOW", "same_file(&before, &entry)"):
            self.assertIn(fragment, body)
        self.assertNotIn("chmod", C)

    def test_independent_two_item_count_paths_attributes_bytes_and_checksum(self):
        body = C[C.index("static gboolean verify_pair("):C.index("static gboolean self_test(")]
        for fragment in ("g_strv_length(unlocked) == 2", "g_strv_length(locked) == 0",
                         "strcmp(unlocked[0], unlocked[1]) != 0", "strcmp(unlocked[i], original_path)",
                         "strcmp(unlocked[i], second_path)", "!secret_item_get_locked(item)",
                         "same_attributes(actual, wanted)", "memcmp(bytes, expected, ENVELOPE_SIZE) == 0",
                         "valid_envelope(bytes, size, reference, expected_key)"):
            self.assertIn(fragment, body)
        self.assertEqual(body.count("secret_service_get_secret_for_dbus_path_sync("), 1)
        self.assertIn("g_hash_table_size(actual) != 7", C)
        self.assertIn("memcmp(sum, bytes + 116, 32)", C)

    def test_negative_outcomes_and_counter_before_oracle_write(self):
        hook = RUST[RUST.index("pub(super) fn capture_outgoing"):RUST.index("fn sent_envelope")]
        self.assertLess(hook.index("count.set(count.get() + 1)"), hook.index("write_sample("))
        for name, outcome, label in (("duplicate-open", "RecoveryKeyOpenOutcome::Corrupt", "A2_OPEN_CORRUPT_OK"),
                                     ("duplicate-create", "RecoveryKeyCreateOutcome::Unavailable", "A2_CREATE_UNAVAILABLE_NO_SEND_OK")):
            body = RUST[RUST.index('"' + name + '" => {'):]
            body = body[:body.index('println!')]
            self.assertIn(outcome, body)
            self.assertEqual(body.count("OUTGOING_ATTEMPTS.with(Cell::get) == 0"), 2)
            self.assertIn(label, RUST)

    def test_no_mutating_methods_or_policy_widening(self):
        for forbidden in ('"Delete"', '"SetSecret"', '"Unlock"', '"Prompt"', "secret_item_delete", "secret_service_store",
                          "secret_service_unlock", "secret_password_", "SECRET_SEARCH_UNLOCK", "SECRET_ITEM_CREATE_REPLACE"):
            self.assertNotIn(forbidden, C)
        source = (HERE / "fixture.py").read_text()
        self.assertIn("super().prepare(root, metadata)", source)
        self.assertNotIn("bus.conf.in", source)
        self.assertLess(source.index("base.verify_isolation(metadata)"), source.index("bus = start("))
        self.assertIn("finally:\n        cleanup(work, processes, handles, outputs)", source)

    def test_docs_links_and_dedicated_exact_head_workflow(self):
        for name in ("CONTRACT.md", "PROVENANCE.md"):
            for target in re.findall(r"\]\(([^)]+)\)", (HERE / name).read_text()):
                if not target.startswith("https://"):
                    self.assertTrue((HERE / target).is_file(), target)
        workflow = (HERE.parents[2] / ".github/workflows/employee-secret-service-duplicate-a2.yml").read_text()
        self.assertIn("ref: ${{ github.event.pull_request.head.sha || github.sha }}", workflow)
        self.assertIn('test "$(git rev-parse HEAD)" = "$EXPECTED_HEAD"', workflow)
        self.assertNotIn("workflow_dispatch", workflow)
        self.assertNotIn("continue-on-error", workflow)
        self.assertIn("interop-a2/fixture.py", workflow)

    def test_linux_helper_predicates_with_public_bytes_when_explicitly_supplied(self):
        executable = os.environ.get("CPA_A2_HELPER_SELF_TEST")
        if not executable:
            self.skipTest("Linux compiled helper not supplied; no runtime claim")
        with tempfile.TemporaryDirectory(prefix="cpa-a2-public-test-") as directory:
            os.chmod(directory, 0o700)
            result = subprocess.run([executable, "--self-test"], cwd=directory, timeout=10, capture_output=True)
            self.assertTrue(result.returncode == 0, "fixed-helper-self-test-failed")
            self.assertEqual(result.stdout, b"A2_HELPER_SELF_TEST_OK\n")
            self.assertEqual(result.stderr, b"")


if __name__ == "__main__":
    unittest.main()
