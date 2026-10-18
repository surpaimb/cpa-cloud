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

        def call(args, environment, expected, label, outputs, report, phase):
            calls.append((args[0], args[-1] if args[0] != "cpa" else environment["CPA_SS_FIXTURE_MODE"]))
            if len(calls) == fail_at:
                raise module.CaseFailure("child-failed")

        with patch.object(module, "bounded_call", side_effect=call), contextlib.redirect_stdout(output):
            try:
                module.cases(dict(cpa="cpa", verifier="verifier", a2_helper="helper"), env, [], lambda: None)
            except module.CaseFailure:
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
            with self.assertRaises(module.CaseFailure):
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
                with self.assertRaises(module.CaseFailure):
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
                    report = module.Report()
                    self.assertTrue(module.scan(HERE, [b"prefix" + form + b"suffix"], report))
                    self.assertEqual(report.states["scan-content"], "matched")

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
                report = module.Report()
                self.assertFalse(module.scan(HERE, [second.hex().encode()], report))
                self.assertEqual(report.states["scan-content"], "matched")

    def test_cleanup_attempts_every_owned_child_and_scan_even_if_stop_fails(self):
        children, handles, order = [Mock(), Mock()], [Mock()], []
        report = module.Report()
        def stop(child):
            order.append(child)
            if child is children[1]:
                raise OSError()
        with tempfile.TemporaryDirectory() as directory, patch.object(module, "stop", side_effect=stop), \
                patch.object(module, "scan", return_value=True) as scan, contextlib.redirect_stdout(io.StringIO()):
            module.cleanup(Path(directory), dict(zip(("bus", "keyring"), children)),
                           {"bus": handles[0]}, [], report, set())
            self.assertEqual(order, list(reversed(children)))
            scan.assert_called_once()
            handles[0].close.assert_called_once()
            self.assertEqual(report.states["cleanup"], "failed")

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
        self.assertLess(source.index("base.verify_isolation(metadata)"), source.index('report.run("bus-start"'))
        self.assertIn("finish_inside(report, operation,", source)

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

    def assert_fixed_report(self, text):
        for line in text.splitlines():
            if line.startswith("FIXTURE_DIAGNOSTIC "):
                self.assertIn(line.removeprefix("FIXTURE_DIAGNOSTIC "), module.DIAGNOSTICS)

    def test_simultaneous_operation_sample_content_log_child_handle_and_outer_failures(self):
        report, relay = module.Report(), io.StringIO()
        dummy = b"public-dummy-only-scan-value-12345"
        report.attempted.update(("create", "inject"))
        children = {"bus": Mock(), "keyring": Mock()}
        handles = {"bus": Mock(), "keyring": Mock()}
        handles["bus"].close.side_effect = OSError("untrusted-error-must-not-appear")
        order = []

        def stop(child):
            order.append(child)
            raise OSError("untrusted-error-must-not-appear")

        def read(path, size):
            if path.name == "sent.envelope":
                raise module.base.PreflightFailure("fixture-oracle-invalid")
            if path.name == "duplicate.sent.envelope":
                return None
            return dummy

        def operation():
            report.run("inject", lambda: (_ for _ in ()).throw(module.CaseFailure("child-failed")))

        def finalizer():
            module.cleanup(HERE, children, handles, [dummy.hex().encode()], report, {"bus", "keyring"})
            def failure():
                raise OSError("untrusted-error-must-not-appear")
            with self.assertRaises(module.base.PreflightFailure):
                module.base.diagnostic_cleanup([("unit", failure), ("work-root", lambda: None)],
                                               lambda key, value: report.set("outer-" + key, value))

        with patch.object(module, "stop", side_effect=stop), patch.object(Path, "read_bytes", side_effect=FileNotFoundError()), \
                patch.object(module.volatile, "read_sample", side_effect=read), contextlib.redirect_stdout(relay):
            with self.assertRaises(module.base.PreflightFailure):
                module.finish_inside(report, operation, finalizer)
            report.emit(outer=True)
        self.assertEqual(order, list(reversed(tuple(children.values()))))
        for handle in handles.values():
            handle.close.assert_called_once()
        self.assertEqual(report.first, ("inject", "child-failed"))
        for key, value in {"sample-original-envelope": "invalid", "sample-duplicate-envelope": "missing",
                           "scan-content": "matched", "scan-completeness": "incomplete", "cleanup": "failed",
                           "log-bus": "missing", "log-keyring": "missing", "child-bus": "failed",
                           "handle-bus": "failed", "outer-unit": "failed", "outer-work-root": "ok",
                           "outer-cleanup": "failed"}.items():
            self.assertEqual(report.states[key], value)
        self.assert_fixed_report(relay.getvalue())
        self.assertNotIn(dummy.decode(), relay.getvalue())
        self.assertNotIn(dummy.hex(), relay.getvalue())
        self.assertNotIn("untrusted-error", relay.getvalue())

    def test_expected_log_missing_or_unreadable_cannot_pass_complete_scan(self):
        for error, state in ((FileNotFoundError(), "missing"), (PermissionError(), "read-failed")):
            report = module.Report()
            with patch.object(Path, "read_bytes", side_effect=error) as read, \
                    patch.object(module.volatile, "read_sample", side_effect=lambda path, n: b"x" * n), \
                    contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(module.base.PreflightFailure):
                    module.finish_inside(report, lambda: None,
                                         lambda: module.cleanup(HERE, {}, {}, [], report, {"bus"}))
            self.assertEqual(read.call_count, 1)
            self.assertEqual(report.states["log-bus"], state)
            self.assertEqual(report.states["log-keyring"], "not-created")
            self.assertEqual(report.states["scan-content"], "clear")
            self.assertEqual(report.states["scan-completeness"], "incomplete")
            self.assertEqual(report.states["cleanup"], "ok")

    def test_each_sample_category_and_remaining_samples_scanned_once(self):
        report = module.Report()
        report.attempted.add("create")
        samples = [None, b"public", module.base.PreflightFailure("fixture-oracle-invalid"), PermissionError()]
        def read(path, size):
            value = samples.pop(0)
            if isinstance(value, Exception):
                raise value
            return value
        with patch.object(module.volatile, "read_sample", side_effect=read) as read:
            self.assertFalse(module.scan(HERE, [], report))
            self.assertEqual(read.call_count, 4)
        self.assertEqual([report.states["sample-" + key] for key in module.SAMPLE_ROLES],
                         ["missing", "valid", "invalid", "read-failed"])
        report = module.Report()
        with patch.object(module.volatile, "read_sample", return_value=None):
            self.assertFalse(module.scan(HERE, [], report))
        self.assertTrue(all(report.states["sample-" + key] == "not-reached" for key in module.SAMPLE_ROLES))

    def test_timeout_first_category_survives_scan_and_cleanup_exception(self):
        report, relay, outputs = module.Report(), io.StringIO(), []
        def operation():
            report.run("inject", lambda: module.bounded_call([], {}, b"ok", "a2-inject-failed", outputs, report, "inject"))
        def finalizer():
            report.set("scan-content", "failed")
            report.set("scan-completeness", "incomplete")
            raise OSError("untrusted-error-must-not-appear")
        with patch.object(module.base, "run", side_effect=subprocess.TimeoutExpired("untrusted-command", 12, output=b"public-dummy")), \
                contextlib.redirect_stdout(relay):
            with self.assertRaises(module.base.PreflightFailure):
                module.finish_inside(report, operation, finalizer)
        self.assertEqual(report.first, ("inject", "timeout"))
        self.assertEqual(report.states["operation-two-items"], "not-reached")
        self.assertEqual(report.states["scan-content"], "failed")
        self.assertEqual(report.states["cleanup"], "failed")
        self.assertEqual(outputs, [b"public-dummy"])
        self.assert_fixed_report(relay.getvalue())
        self.assertNotIn("untrusted-", relay.getvalue())

    def test_outer_cleanup_attempts_existing_actions_once_and_keeps_all_statuses(self):
        states, calls = {}, []
        def action(name, error=None):
            def invoke():
                calls.append(name)
                if error:
                    raise error
            return invoke
        with self.assertRaises(module.base.PreflightFailure):
            module.base.diagnostic_cleanup([
                ("unit", action("unit", subprocess.TimeoutExpired("public-dummy", 1))),
                ("user", action("user", OSError())), ("work-root", action("work-root"))], states.__setitem__)
        self.assertEqual(calls, ["unit", "user", "work-root"])
        self.assertEqual(states, {"unit": "timeout", "user": "failed", "work-root": "ok", "cleanup": "failed"})

    def test_helper_only_reports_fixed_existing_checkpoints_without_new_calls(self):
        report = module.Report()
        report.helper("inject", b"A2_HELPER_STAGE reference failed\nA2_HELPER_STAGE session not-reached\n"
                      b"A2_HELPER_STAGE secret-raw-value failed\nA2_HELPER_STAGE reference private-value\n")
        self.assertEqual(report.states["helper-inject-reference"], "failed")
        self.assertEqual(report.states["helper-inject-session"], "not-reached")
        with self.assertRaises(ValueError):
            report.set("untrusted-key", "failed")
        stages = C[C.index("static const char *stage_names[]"):C.index("static unsigned char stage_states")]
        self.assertEqual(re.findall(r'"([a-z-]+)"', stages), list(module.HELPER_STAGES))
        self.assertEqual(C.count('"CreateItem"'), 1)
        self.assertEqual(C.count("getrandom("), 1)
        self.assertEqual(C.count("secret_service_open_sync("), 1)
        self.assertEqual(C.count("secret_service_get_secret_for_dbus_path_sync("), 1)
        self.assertIn("atexit(emit_stages);", C)

    def test_outer_wrapper_preserves_operation_and_cleanup_categories_without_exception_text(self):
        relay = io.StringIO()
        def fixture(binary, report):
            value = Mock()
            value.outer_diagnostic.side_effect = lambda key, status: report.set("outer-" + key, status)
            return value
        def fail(value):
            value.outer_diagnostic("operation", "failed")
            value.outer_diagnostic("unit", "timeout")
            value.outer_diagnostic("cleanup", "failed")
            raise OSError("untrusted-outer-error")
        with patch.object(module, "Fixture", side_effect=fixture), patch.object(module.base, "outer", side_effect=fail), \
                contextlib.redirect_stdout(relay):
            with self.assertRaises(OSError):
                module.outer("public-dummy")
        self.assertIn("a2-outer-operation-failed", relay.getvalue())
        self.assertIn("a2-outer-unit-timeout", relay.getvalue())
        self.assertIn("a2-outer-cleanup-failed", relay.getvalue())
        self.assertNotIn("A2_OUTER_CLEANUP_OK=true", relay.getvalue())
        self.assertNotIn("untrusted-", relay.getvalue())
        self.assert_fixed_report(relay.getvalue())

    def test_missing_helper_report_is_unobserved_and_not_proof_of_no_send(self):
        report = module.Report()
        report.attempted.add("inject")
        report.helper("inject", b"public-dummy-unstructured-output")
        self.assertEqual(report.states["helper-inject-capture-key"], "unobserved")
        with patch.object(module.volatile, "read_sample", return_value=None):
            module.scan(HERE, [], report)
        self.assertEqual(report.states["sample-duplicate-key"], "missing")
        report.helper("inject", b"A2_HELPER_STAGE capture-key not-reached\n")
        with patch.object(module.volatile, "read_sample", return_value=None):
            module.scan(HERE, [], report)
        self.assertEqual(report.states["sample-duplicate-key"], "not-reached")

    def test_successful_helper_without_complete_diagnostic_frame_fails_closed(self):
        report = module.Report()
        with patch.object(module.base, "run", return_value=Mock(returncode=0, stdout=b"A2_HELPER_OK\n")):
            with self.assertRaises(module.CaseFailure) as failure:
                module.bounded_call([], {}, b"A2_HELPER_OK\n", "a2-inject-failed", [], report, "inject")
        self.assertEqual(failure.exception.category, "marker-missing")
        frame = b"A2_HELPER_OK\nA2_ORIGINAL_ENVELOPE_READ ok\nA2_ORIGINAL_PATH_READ ok\n" + b"".join(("A2_HELPER_STAGE " + stage + " not-reached\n").encode()
                                                for stage in module.HELPER_STAGES)
        with patch.object(module.base, "run", return_value=Mock(returncode=0, stdout=frame)):
            module.bounded_call([], {}, b"A2_HELPER_OK\n", "a2-inject-failed", [], report, "inject")

    def test_expected_sample_writer_uses_explicit_private_creation_and_real_libsecret_bytes(self):
        writer = (HERE.parent / "interop/private-envelope.h").read_text()
        verifier = (HERE.parent / "interop/verifier.c").read_text()
        self.assertIn('openat(dir, "expected.envelope", O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600)', writer)
        self.assertLess(writer.index("expected_envelope_metadata(&before, 0)"), writer.index("write(fd, bytes, length)"))
        for predicate in ("s->st_uid == geteuid()", "s->st_gid == getegid()", "s->st_nlink == 1", "0600",
                          "fsync(fd)", "fsync(dir)", "AT_SYMLINK_NOFOLLOW", "before.st_ino == entry.st_ino"):
            self.assertIn(predicate, writer)
        self.assertNotIn("chmod(", writer)
        self.assertNotIn("read(", writer)
        self.assertIn("write_expected_envelope(bytes, size)", verifier)
        self.assertNotIn('fopen("expected.envelope", "wx")', verifier)
        self.assertLess(verifier.index("secret_service_get_secret_for_dbus_path_sync"), verifier.index("write_expected_envelope(bytes, size)"))
        self.assertIn("umask(0)", C)
        self.assertIn("read_original_envelope(dir, loaded)", C)

    def test_reader_categories_preserve_existing_syscall_counts_and_predicates(self):
        reader = C[C.index("static gboolean read_sample("):C.index("static gboolean read_original_envelope(")]
        for call, count in (("openat(", 1), ("fstat(", 2), ("fstatat(", 1), ("read(", 2), ("close(", 1)):
            self.assertEqual(reader.count(call), count, call)
        for category in ("READ_OPEN", "READ_METADATA", "READ_LENGTH", "READ_CHANGED", "READ_CLOSE"):
            self.assertIn("last_read_category = " + category, reader)
        self.assertIn("if (ok) last_read_category = READ_CLOSE;", reader)
        self.assertIn("private_file(&before, length)", reader)
        self.assertIn("same_file(&before, &entry)", reader)
        wrapper = C[C.index("static gboolean read_original_envelope("):C.index("static gboolean read_path(")]
        self.assertEqual(wrapper.count("read_sample("), 1)

    def test_read_failure_categories_only_accept_source_owned_enum(self):
        for category in module.READ_CATEGORIES[1:]:
            report = module.Report()
            report.helper("inject", ("A2_ORIGINAL_ENVELOPE_READ " + category + "\n").encode())
            self.assertEqual(report.states["helper-inject-original-read"], category)
        report.helper("inject", b"A2_ORIGINAL_ENVELOPE_READ private-untrusted-value\n")
        self.assertEqual(report.states["helper-inject-original-read"], "close-failed")

    def test_item_path_writer_is_private_exclusive_and_uses_existing_libsecret_path(self):
        writer = (HERE.parent / "interop/private-path.h").read_text()
        verifier = (HERE.parent / "interop/verifier.c").read_text()
        self.assertIn('openat(dir, "item.path", O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0600)', writer)
        self.assertIn("if (!length || length >= 256) return false;", writer)
        self.assertLess(writer.index("item_path_metadata(&before, 0)"), writer.index("write(fd, bytes, length)"))
        for predicate in ("s->st_uid == geteuid()", "s->st_gid == getegid()", "s->st_nlink == 1", "0600",
                          "fsync(fd)", "fsync(dir)", "AT_SYMLINK_NOFOLLOW", "before.st_ino == entry.st_ino"):
            self.assertIn(predicate, writer)
        self.assertNotIn("chmod(", writer)
        self.assertNotIn("read(", writer)
        self.assertIn("write_item_path(unlocked[0], strlen(unlocked[0]))", verifier)
        self.assertNotIn('fopen("item.path", "wx")', verifier)
        self.assertLess(verifier.index("secret_service_search_for_dbus_paths_sync"), verifier.index("write_item_path("))
        self.assertIn('static const char public_path[] = SESSION "/public_item";', C)
        self.assertIn("read_original_path(dir, loaded_path)", C)
        self.assertIn('!write_item_path("must-not-overwrite", 18)', C)

    def test_path_categories_preserve_existing_read_and_format_predicates(self):
        reader = C[C.index("static gboolean read_path("):C.index("static gboolean read_original_path(")]
        for call in ("fstatat(", "read_sample(", "strlen(", "g_variant_is_object_path(", "g_str_has_prefix("):
            self.assertEqual(reader.count(call), 1, call)
        for predicate in ("s.st_size <= 0", "s.st_size >= 256", "AT_SYMLINK_NOFOLLOW",
                          "private_file(&s, (size_t)s.st_size)", "strlen(out) == n", 'SESSION "/"'):
            self.assertIn(predicate, reader)
        self.assertNotIn("openat(", reader)
        self.assertNotIn("read(", reader)
        for category in ("PATH_METADATA", "PATH_READ", "PATH_FORMAT"):
            self.assertIn(category, reader)
        wrapper = C[C.index("static gboolean read_original_path("):C.index("static void checksum(")]
        self.assertEqual(wrapper.count("read_path("), 1)
        self.assertIn("original_path_category = last_path_category;", wrapper)

    def test_path_enum_rejects_raw_values_and_missing_success_frame_fails_closed(self):
        for category in module.PATH_CATEGORIES[1:]:
            report = module.Report()
            report.helper("inject", ("A2_ORIGINAL_PATH_READ " + category + "\n").encode())
            self.assertEqual(report.states["helper-inject-original-path-read"], category)
        report.helper("inject", b"A2_ORIGINAL_PATH_READ private-untrusted-value\n")
        self.assertEqual(report.states["helper-inject-original-path-read"], "format-failed")
        report = module.Report()
        frame = b"A2_HELPER_OK\nA2_ORIGINAL_ENVELOPE_READ ok\n" + b"".join(
            ("A2_HELPER_STAGE " + stage + " not-reached\n").encode() for stage in module.HELPER_STAGES)
        with patch.object(module.base, "run", return_value=Mock(returncode=0, stdout=frame)):
            with self.assertRaises(module.CaseFailure) as failure:
                module.bounded_call([], {}, b"A2_HELPER_OK\n", "a2-inject-failed", [], report, "inject")
        self.assertEqual(failure.exception.category, "marker-missing")
        self.assertEqual(report.states["helper-inject-original-path-read"], "unobserved")


if __name__ == "__main__":
    unittest.main()
