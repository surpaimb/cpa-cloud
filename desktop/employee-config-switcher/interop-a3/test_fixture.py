#!/usr/bin/env python3
"""Public dummy/source tests only. Never start a bus, daemon or CPA process."""
import contextlib
import ast
import hashlib
import importlib.util
import io
import os
import re
from pathlib import Path
import tempfile
import subprocess
import sys
import types
import unittest
from unittest import mock
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("a3_fixture_public", HERE / "fixture.py")
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
LIVE = HERE.parent / "crates/employee-config-core/src/live"


class FixtureTests(unittest.TestCase):
    def test_case_matrix_is_bounded_and_sequential(self):
        self.assertEqual(len(f.CASES), 27)
        self.assertEqual(len(set(f.CASES)), 27)
        self.assertEqual(len(f.BOUNDARIES), 9)
        for boundary in f.BOUNDARIES:
            for outcome in ("complete", "rollback"):
                self.assertIn(outcome + "-" + boundary, f.CASES)

    def test_every_plan_has_unique_preregistered_logs(self):
        for case in f.CASES:
            with self.subTest(case=case):
                names = f.log_names(case)
                self.assertEqual(len(set(names)), len(names))
                self.assertTrue(all("/" not in name and name.endswith(".log") for name in names))
                self.assertIn("bus.log", names)
                self.assertIn("keyring.log", names)

    def test_actual_presend_and_employee_samples_only(self):
        self.assertEqual(len(f.samples("exact")), 4)
        self.assertEqual(len(f.samples("duplicate")), 4)
        for case in f.CASES[2:]:
            self.assertEqual(len(f.samples(case)), 6)
        self.assertTrue(all("expected" not in name and "reference" not in name
                            for case in f.CASES for name, _ in f.samples(case)))

    def scan(self, missing=None, leaked=None):
        calls = []
        def read(path, uid, gid, length=None, *, trace=None):
            calls.append(str(path))
            if path.name == missing:
                raise FileNotFoundError()
            if trace:
                trace("reader", "ok")
            if path.name.endswith(".complete"):
                return b"complete\n"
            return b"q" * length if length else (leaked or b"")
        report = f.Report()
        with mock.patch.object(f, "private_read", side_effect=read):
            result = f.scan_all(Path("public-dummy"), "complete-terminal", 1, 1, (), report)
        return result, report, calls

    def test_missing_sample_keeps_clear_distinct_from_complete(self):
        ok, report, calls = self.scan(missing="old.sample")
        self.assertFalse(ok)
        self.assertEqual(report.states["scan-content"], "clear")
        self.assertEqual(report.states["scan-completeness"], "incomplete")
        self.assertEqual(len(calls), 6 + 2 * len(f.log_names("complete-terminal")))
        self.assertEqual(report.first_evidence, ("sample-target-old", "reader", "missing"))
        self.assertEqual(report.integrity["samples"], "incomplete")
        self.assertEqual(report.integrity["logs"], "complete")

    def test_missing_log_does_not_stop_other_scans(self):
        ok, report, calls = self.scan(missing="keyring.log")
        self.assertFalse(ok)
        self.assertEqual(report.states["scan-completeness"], "incomplete")
        self.assertEqual(len(calls), 6 + 2 * len(f.log_names("complete-terminal")))

    def test_missing_eof_certificate_is_incomplete_even_with_a_log(self):
        ok, report, _ = self.scan(missing="child-final-neighbor.log.complete")
        self.assertFalse(ok)
        self.assertEqual(report.states["scan-content"], "clear")
        self.assertEqual(report.states["scan-completeness"], "incomplete")

    def test_all_secret_encodings_are_rejected(self):
        for value in f.volatile.secret_forms(b"q" * 32):
            with self.subTest(form=value[:8]):
                ok, report, _ = self.scan(leaked=value)
                self.assertFalse(ok)
                self.assertEqual(report.states["scan-content"], "matched")

    def test_complete_empty_logs_are_not_missing(self):
        ok, report, _ = self.scan()
        self.assertTrue(ok)
        self.assertEqual(report.states["scan-completeness"], "complete")

    def test_first_operation_survives_later_cleanup_failure(self):
        report = f.Report()
        with self.assertRaises(ValueError):
            report.run("exact-delete", lambda: (_ for _ in ()).throw(ValueError("public dummy")))
        report.set("children", "failed")
        report.set("scan-content", "clear")
        self.assertEqual(report.first, "exact-delete")
        self.assertEqual(report.states["operation-exact-delete"], "failed")

    def test_diagnostics_are_source_owned(self):
        report = f.Report()
        with self.assertRaises(f.base.PreflightFailure):
            report.set("raw-secret-value", "ok")
        with self.assertRaises(f.base.PreflightFailure):
            report.set("children", "untrusted text")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            report.emit()
        for line in output.getvalue().splitlines():
            self.assertIn(line.removeprefix("FIXTURE_DIAGNOSTIC "), f.DIAGNOSTICS)

    def test_first_red_never_starts_second_case(self):
        with mock.patch.object(f, "Fixture") as fixture, mock.patch.object(f.base, "outer", side_effect=RuntimeError("public red")) as outer:
            with self.assertRaises(RuntimeError):
                f.outer("public-binary")
            self.assertEqual(fixture.call_count, 1)
            self.assertEqual(outer.call_count, 1)

    def test_unit_evidence_requires_pipe_and_nonempty_current_journal(self):
        fixture = object.__new__(f.Fixture)
        fixture.case, fixture.cursor = "exact", "public-cursor"
        metadata = dict(work="public-dummy", uid=1, gid=1)
        for complete, status, journal in ((False, 0, b"unit"), (True, 1, b"unit"), (True, 0, b""), (True, 0, b"unit")):
            with self.subTest(complete=complete, status=status, journal=journal):
                with mock.patch.object(fixture, "verify_unit_stopped"), mock.patch.object(f, "scan_all", return_value=True), mock.patch.object(
                        f.base, "run", return_value=types.SimpleNamespace(returncode=status, stdout=journal)), contextlib.redirect_stdout(io.StringIO()):
                    with self.assertRaises(f.base.PreflightFailure):
                        fixture.after_unit(metadata, b"captured", complete)

    def test_root_accepts_only_complete_inner_and_outer_capture(self):
        fixture = object.__new__(f.Fixture)
        fixture.case, fixture.cursor = "exact", "public-cursor"
        metadata = dict(work="public-dummy", uid=1, gid=1)
        with mock.patch.object(fixture, "verify_unit_stopped"), mock.patch.object(f, "scan_all", return_value=True), mock.patch.object(
                f.base, "run", return_value=types.SimpleNamespace(returncode=0, stdout=b"unit")), contextlib.redirect_stdout(io.StringIO()):
            fixture.after_unit(metadata, b"A3_SCAN_OK\n", True)

    def test_outer_evidence_is_separate_and_scans_obtainable_material_once(self):
        # Public bytes only; no journal command or service is actually started.
        cases = ((False, "public-cursor", 0, b"public-unit", b"A3_SCAN_OK\n", None,
                  ("pipe-unit", "pipe-capture", "incomplete"), "incomplete", "complete", "complete"),
                 (True, "public-cursor", 1, b"public-unit", b"A3_SCAN_OK\n", None,
                  ("journal-unit", "journal-command", "failed"), "complete", "incomplete", "complete"),
                 (True, "public-cursor", 0, b"", b"A3_SCAN_OK\n", None,
                  ("journal-unit", "journal-nonempty", "rejected"), "complete", "incomplete", "complete"),
                 (True, None, 0, b"", b"A3_SCAN_OK\n", None,
                  ("journal-unit", "journal-cursor", "missing"), "complete", "incomplete", "complete"),
                 (True, "public-cursor", 0, b"public-partial", b"A3_SCAN_OK\n",
                  subprocess.TimeoutExpired("DO-NOT-PRINT-COMMAND", 30, output=b"public-partial"),
                  ("journal-unit", "journal-capture", "incomplete"), "complete", "incomplete", "complete"),
                 (True, "public-cursor", 0, b"public-unit", b"public-no-marker", None,
                  ("pipe-unit", "inner-marker", "rejected"), "complete", "complete", "incomplete"))
        for complete, cursor, status, journal, captured, error, first, pipe, journal_state, inner in cases:
            with self.subTest(first=first):
                fixture = object.__new__(f.Fixture)
                fixture.case, fixture.cursor = "exact", cursor
                reports = []
                def scan(work, case, uid, gid, extra, report):
                    self.assertEqual(extra, (captured, journal))
                    reports.append(report)
                    report.set("scan-content", "clear")
                    report.set("scan-completeness", "complete")
                    return True
                output = io.StringIO()
                with mock.patch.object(fixture, "verify_unit_stopped"), mock.patch.object(f, "scan_all", side_effect=scan) as scanned, mock.patch.object(
                        f.base, "run", side_effect=error, return_value=types.SimpleNamespace(returncode=status, stdout=journal)) as command, contextlib.redirect_stdout(output):
                    with self.assertRaises(f.base.PreflightFailure):
                        fixture.after_unit(dict(work="DO-NOT-PRINT-PATH", uid=123456, gid=654321), captured, complete)
                self.assertEqual(scanned.call_count, 1)
                self.assertEqual(command.call_count, int(cursor is not None))
                self.assertEqual(reports[0].first_evidence, first)
                self.assertEqual([reports[0].integrity[k] for k in ("pipe", "journal", "inner")], [pipe, journal_state, inner])
                for line in output.getvalue().splitlines():
                    self.assertIn(line.removeprefix("FIXTURE_DIAGNOSTIC "), f.DIAGNOSTICS)
                for private in ("DO-NOT-PRINT", "123456", "654321", "public-partial", "public-unit"):
                    self.assertNotIn(private, output.getvalue())

    def test_policy_last_match_allows_only_scoped_typed_delete(self):
        old = ET.parse(HERE.parent / "interop/bus.conf.in").getroot().find("policy")
        new = ET.parse(HERE / "bus.conf.in").getroot().find("policy")
        self.assertEqual([(r.tag, r.attrib) for r in old], [(r.tag, r.attrib) for r in new][:-1])
        def allowed(interface, member, destination="org.freedesktop.secrets"):
            values = dict(send_type="method_call", send_path="/fixture", send_destination=destination,
                          send_interface=interface, send_member=member)
            result = False
            for rule in new:
                attrs = {k: v for k, v in rule.attrib.items() if k.startswith("send_")}
                if attrs and all(v == "*" or v == values.get(k) for k, v in attrs.items()):
                    result = rule.tag == "allow"
            return result
        self.assertTrue(allowed("org.freedesktop.Secret.Item", "Delete"))
        self.assertFalse(allowed(None, "Delete"))
        self.assertFalse(allowed("org.freedesktop.Secret.Collection", "Delete"))
        self.assertFalse(allowed("org.freedesktop.Secret.Item", "Delete", "org.freedesktop.DBus"))
        for member in ("Unlock", "Prompt", "Dismiss", "SetSecret", "Set", "CreateCollection", "Lock"):
            self.assertFalse(allowed(None, member))
            self.assertFalse(allowed("org.freedesktop.Secret.Item", member))

    def test_real_provider_and_six_attribute_query_are_unchanged(self):
        source = (LIVE / "linux_secret_service.rs").read_text()
        body = source[source.index("    fn delete(\n", source.index("struct ContractProvider")):source.index("fn create_from_existing")]
        self.assertIn("find_one(backend, transaction_id, self.uid, self.lock_namespace)", body)
        self.assertNotIn("backend.search(&full_attributes", body)
        self.assertIn("backend.delete_item(&item.handle)", body)
        self.assertIn("if prompt.to_string() != ROOT_PATH", source)
        a3 = (LIVE / "linux_secret_service_a3_fixture.rs").read_text()
        for forbidden in ("from_durable_phase", "DurableTerminalHandoff::for_test", "WorkspaceRemovalReceipt::", "RecoveryKeyCleanupSequence::for_test"):
            self.assertNotIn(forbidden, a3)
        for call in ("replace_existing_synthetic(", "receipt.into_terminal_handoff()", "cleanup.retire_workspace_contents_durable()",
                     "cleanup.remove_workspace_durable(&retired)", "cleanup.delete_key(p)", "remove_completion_tombstone(root, &authorization)"):
            self.assertIn(re.sub(r"\s+", "", call), re.sub(r"\s+", "", a3))

    def test_hooks_are_test_only_and_default_off(self):
        a3 = (LIVE / "linux_secret_service_a3_fixture.rs").read_text()
        self.assertIn("Cell::new(false)", a3)
        ss = (LIVE / "linux_secret_service.rs").read_text()
        self.assertIn('#[cfg(test)]\n#[path = "linux_secret_service_a3_fixture.rs"]', ss)
        journal = (LIVE / "cleanup_journal.rs").read_text()
        at = journal.index('checkpoint("deleted-before-confirm")')
        self.assertLess(journal.rindex("self.sequence.delete_key(provider)?", 0, at), at)
        self.assertLess(at, journal.index("self.persist_or_restore(previous)", at))

    def test_native_failure_labels_are_fixed_and_complete(self):
        a3 = (LIVE / "linux_secret_service_a3_fixture.rs").read_text()
        self.assertEqual(set(f.RUST_FAILURES), set(re.findall(r'"(a3-[a-z-]+)"', a3)))
        verifier = (HERE / "verifier.c").read_text()
        codes = {int(v) for v in re.findall(r"return (\d+);", verifier)} - {0}
        self.assertTrue(codes <= set(f.VERIFIER_FAILURES))
        self.assertTrue(all(re.fullmatch(r"a3-[a-z-]+", v) for v in f.RUST_FAILURES + f.VERIFIER_LABELS))

    def test_a3_approval_bridge_remains_inside_test_only_impl(self):
        source = (LIVE / "linux_replacement.rs").read_text()
        at = source.index("pub(in crate::live) fn for_a3_test")
        start = source.rindex("#[cfg(test)]", 0, at)
        self.assertIn("impl SyntheticRootApproval", source[start:at])
        self.assertIn("Self::for_test(root)", source[at:source.index("\n}", at)])

    def test_actual_kill_uses_owned_pid_after_fixed_handshake(self):
        source = (HERE / "fixture.py").read_text()
        body = source[source.index("    def command("):source.index("    def cpa(")]
        self.assertIn("base.owned_process(process.pid", body)
        self.assertIn("process.kill()", body)
        self.assertIn("status == -signal.SIGKILL", body)
        self.assertNotIn("time.sleep", body)
        self.assertIn('"A3_BOUNDARY " + crash', body)

    def test_real_verifier_is_not_a_deleter(self):
        source = (HERE / "verifier.c").read_text()
        self.assertNotIn("secret_item_delete", source)
        self.assertIn('"/org/freedesktop/secrets/a3_policy_probe"', source)
        self.assertIn("G_DBUS_MESSAGE_FLAGS_NO_AUTO_START", source)
        self.assertIn("path_absent(service", source)
        self.assertIn('g_hash_table_size(attrs) != 7', source)
        self.assertIn('memcmp(envelope, bytes, 148)', source)

    def test_outer_cgroup_gate_precedes_account_and_root_removal(self):
        source = (HERE.parent / "preflight/run.py").read_text()
        at = source.index('fixture.verify_unit_stopped(metadata)')
        self.assertLess(at, source.index("unit_stopped = True", at))
        self.assertLess(at, source.index('run(["userdel", USER])', at))
        self.assertLess(source.index('actions.append(("unit-evidence", unit_evidence))'), source.index('run(["userdel", USER])'))

    def test_new_batch_does_not_change_business_or_capture_writer_flow(self):
        tree = ast.parse((HERE / "fixture.py").read_text())
        expected = {"mark_complete": "edc4a3394a6454dd581193a7d10f8b5dcf27c765b12d99bd26857a56c8f0d30a",
            "Children": "8852f8dc187afea55d1f3750c857de2ff67d63e0212d8cdfe211fbc9f1f75a3c",
            "sequence": "f911d61405158a5c9713d194fce4a9eb0cb255d8e561cdf11caf39b5962f42ca",
            "inside": "f65d41fd5ef811699359ff4beb27b5d8f7190ab9e815bb938b9510b053f3555b",
            "outer": "900f9ecd41c60c6820fc1c475d75fe14bcb9dae954ab03ae9ca11eb846cab28e",
            "Fixture.prepare": "e6c3b8820cc35f194158dbfcddfd02ae095d475afd7399608e68bca7908799da",
            "Fixture.before_unit": "2bb14888b0a3e659e5d690f9c619db6279f0c4db8c10907d55da306e5b3171cd",
            "Fixture.verify_unit_stopped": "674189cc2e2cdbf0e4c88aba8dc93b388c209e036ac9d672be390e22995123d0"}
        nodes = {node.name: node for node in tree.body if isinstance(node, (ast.FunctionDef, ast.ClassDef))}
        for node in nodes["Fixture"].body:
            if isinstance(node, ast.FunctionDef):
                nodes["Fixture." + node.name] = node
        for name, digest in expected.items():
            self.assertEqual(hashlib.sha256(ast.dump(nodes[name], include_attributes=False).encode()).hexdigest(), digest, name)


class ReaderTests(unittest.TestCase):
    def read_mock(self, *, mode=None, read_error=None, close_error=None, leaf_open_error=None,
                  parent_changes=None, leaf_changes=None, post_changes=None, read_value=b"public"):
        directory = types.SimpleNamespace(st_mode=f.stat.S_IFDIR | 0o700, st_uid=1, st_gid=1)
        info = types.SimpleNamespace(st_mode=mode if mode is not None else f.stat.S_IFREG | 0o600,
            st_uid=1, st_gid=1, st_nlink=1, st_size=6, st_dev=2, st_ino=3)
        directory.__dict__.update(parent_changes or {})
        info.__dict__.update(leaf_changes or {})
        post = types.SimpleNamespace(**(vars(info) | (post_changes or {})))
        report = f.Report()
        stream = mock.Mock()
        stream.read.return_value = read_value
        stream.read.side_effect = read_error
        stream.close.side_effect = close_error
        trace = lambda stage, result: report.evidence("sample-original-key", stage, result)
        with contextlib.ExitStack() as stack:
            for name, flag in (("O_DIRECTORY", 0x10000), ("O_NOFOLLOW", 0x20000), ("O_CLOEXEC", 0x40000), ("O_NONBLOCK", 0x80000)):
                stack.enter_context(mock.patch.object(f.os, name, getattr(f.os, name, flag), create=True))
            opened = stack.enter_context(mock.patch.object(f.os, "open", side_effect=[10, leaf_open_error or 11]))
            stated = stack.enter_context(mock.patch.object(f.os, "fstat", side_effect=[directory, info, post]))
            wrapped = stack.enter_context(mock.patch.object(f.os, "fdopen", return_value=stream))
            closed = stack.enter_context(mock.patch.object(f.os, "close"))
            result, error = None, None
            try:
                result = f.private_read(Path("PUBLIC-PATH/value"), 1, 1, 6, trace=trace)
            except f.EvidenceReadFailure as failure:
                error = failure
            calls = (opened.call_args_list, stated.call_count, wrapped.call_args_list, closed.call_args_list,
                     stream.read.call_count, stream.close.call_count)
        return result, error, report, calls

    def test_reader_uses_one_nonblocking_nofollow_leaf_open_and_one_read(self):
        result, error, report, calls = self.read_mock()
        self.assertEqual(result, b"public")
        self.assertIsNone(error)
        self.assertIsNone(report.first_evidence)
        self.assertEqual(calls[1], 3)
        self.assertEqual(calls[3], [mock.call(10), mock.call(11)])
        self.assertEqual(calls[4:], (1, 1))
        self.assertEqual(calls[2], [mock.call(11, "rb", closefd=False)])
        flags = calls[0][1].args[1]
        self.assertTrue(flags & getattr(os, "O_NONBLOCK", 0x80000))
        self.assertTrue(flags & getattr(os, "O_NOFOLLOW", 0x20000))

    def test_fifo_kind_is_rejected_before_buffer_or_read(self):
        _, error, report, calls = self.read_mock(mode=f.stat.S_IFIFO | 0o600)
        self.assertEqual((error.stage, error.result), ("leaf-kind", "rejected"))
        self.assertEqual(report.first_evidence, ("sample-original-key", "leaf-kind", "rejected"))
        self.assertEqual(calls[2], [])
        self.assertEqual(calls[4:], (0, 0))
        self.assertEqual(calls[3], [mock.call(10), mock.call(11)])

    def test_missing_and_denied_are_fixed_without_exception_text(self):
        for error, category in ((FileNotFoundError("DO-NOT-PRINT"), "missing"), (PermissionError("DO-NOT-PRINT"), "denied")):
            with self.subTest(category=category):
                _, failure, report, calls = self.read_mock(leaf_open_error=error)
                self.assertEqual((failure.stage, failure.result), ("leaf-open", category))
                self.assertEqual(str(failure), "a3-evidence-read-failed")
                self.assertEqual(calls[4], 0)
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    report.emit_evidence()
                self.assertNotIn("DO-NOT-PRINT", output.getvalue())
                self.assertNotIn("PUBLIC-PATH", output.getvalue())

    def test_read_and_close_failure_preserve_first_and_close_once(self):
        _, failure, report, calls = self.read_mock(read_error=OSError("DO-NOT-PRINT"), close_error=OSError("ALSO-NOT-PRINT"))
        self.assertEqual((failure.stage, failure.result), ("read", "failed"))
        self.assertEqual(report.first_evidence, ("sample-original-key", "read", "failed"))
        self.assertEqual(report.evidence_events[("sample-original-key", "buffer-close")], "failed")
        self.assertEqual(calls[4:], (1, 1))
        self.assertEqual(calls[3], [mock.call(10), mock.call(11)])

    def test_successful_read_with_failed_close_is_not_returned(self):
        result, error, _, calls = self.read_mock(close_error=OSError("DO-NOT-PRINT"))
        self.assertIsNone(result)
        self.assertEqual((error.stage, error.result), ("buffer-close", "failed"))
        self.assertEqual(calls[4:], (1, 1))

    def test_evidence_first_is_not_erased_by_later_success(self):
        report = f.Report()
        report.evidence("log-bus", "leaf-open", "missing")
        report.evidence("log-bus", "leaf-open", "ok")
        report.evidence("log-keyring", "integrity", "complete")
        self.assertEqual(report.first_evidence, ("log-bus", "leaf-open", "missing"))
        self.assertEqual(report.evidence_events[("log-bus", "leaf-open")], "missing")
        with self.assertRaises(f.base.PreflightFailure):
            report.evidence("UNTRUSTED-PATH", "leaf-open", "missing")

    def test_each_existing_metadata_and_binding_check_has_a_fixed_stage(self):
        cases = (("parent-kind", dict(parent_changes=dict(st_mode=f.stat.S_IFREG | 0o700))),
                 ("parent-owner", dict(parent_changes=dict(st_uid=2))),
                 ("parent-group", dict(parent_changes=dict(st_gid=2))),
                 ("parent-mode", dict(parent_changes=dict(st_mode=f.stat.S_IFDIR | 0o755))),
                 ("leaf-owner", dict(leaf_changes=dict(st_uid=2))),
                 ("leaf-group", dict(leaf_changes=dict(st_gid=2))),
                 ("leaf-links", dict(leaf_changes=dict(st_nlink=2))),
                 ("leaf-mode", dict(leaf_changes=dict(st_mode=f.stat.S_IFREG | 0o644))),
                 ("leaf-size", dict(leaf_changes=dict(st_size=4 * 1024 * 1024 + 1))),
                 ("leaf-length", dict(leaf_changes=dict(st_size=5))),
                 ("read-length", dict(read_value=b"short")),
                 ("post-binding", dict(post_changes=dict(st_ino=4))))
        for stage, changes in cases:
            with self.subTest(stage=stage):
                result, error, report, calls = self.read_mock(**changes)
                self.assertIsNone(result)
                self.assertEqual((error.stage, error.result), (stage, "rejected"))
                self.assertEqual(report.first_evidence, ("sample-original-key", stage, "rejected"))
                self.assertLessEqual(calls[1], 3)
                self.assertLessEqual(calls[4], 1)


@unittest.skipUnless(sys.platform == "linux", "Linux public filesystem/FIFO evidence not executed here")
class LinuxReaderTests(unittest.TestCase):
    @staticmethod
    def write(path, value, mode=0o600):
        # Exact test-only creation mode, independent of the runner's umask.
        # No chmod/chown or repair of any runtime fixture is permitted.
        previous = os.umask(0)
        try:
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, mode)
        finally:
            os.umask(previous)
        try:
            if os.write(descriptor, value) != len(value):
                raise AssertionError("public-dummy-short-write")
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def test_actual_completion_writer_to_reader(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            f.mark_complete(root, "public.log")
            events = []
            self.assertEqual(f.private_read(root / "public.log.complete", os.geteuid(), os.getegid(), 9,
                                           trace=lambda stage, result: events.append((stage, result))), b"complete\n")
            self.assertIn(("reader", "ok"), events)

    def test_real_public_sample_and_empty_log_writer_to_reader(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value, length in (("sample", b"public-dummy", 12), ("log", b"", None)):
                self.write(root / name, value)
                self.assertEqual(f.private_read(root / name, os.geteuid(), os.getegid(), length), value)

    def test_real_missing_mode_and_length_classification(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.write(root / "unsafe", b"public", 0o644)
            self.write(root / "short", b"public")
            for name, length, stage, result in (("missing", 6, "leaf-open", "missing"),
                    ("unsafe", 6, "leaf-mode", "rejected"), ("short", 32, "leaf-length", "rejected")):
                with self.subTest(name=name):
                    with self.assertRaises(f.EvidenceReadFailure) as raised:
                        f.private_read(root / name, os.geteuid(), os.getegid(), length)
                    self.assertEqual((raised.exception.stage, raised.exception.result), (stage, result))

    def test_real_no_writer_fifo_is_quickly_rejected_in_child(self):
        # Independent static defect regression, not the old red's root cause.
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "public.fifo"
            os.mkfifo(path, 0o600)
            code = ("import runpy,os,sys; from pathlib import Path; m=runpy.run_path(sys.argv[1]); "
                    "r=m['Report'](); p=Path(sys.argv[2]); "
                    "\ntry: m['private_read'](p,os.geteuid(),os.getegid(),trace=lambda s,v:r.evidence('sample-original-key',s,v))"
                    "\nexcept m['EvidenceReadFailure'] as e:"
                    "\n assert (e.stage,e.result)==('leaf-kind','rejected'); print('PUBLIC_FIFO_REJECTED')"
                    "\nelse: raise AssertionError('fifo-accepted')")
            result = subprocess.run([sys.executable, "-B", "-c", code, str(HERE / "fixture.py"), str(path)],
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=3, check=False)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b"PUBLIC_FIFO_REJECTED\n")

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "actual permission denial needs unprivileged dummy test")
    def test_real_open_permission_denied(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "public.denied"
            self.write(path, b"public", 0o000)  # Creation only; never repair/chmod a fixture.
            with self.assertRaises(f.EvidenceReadFailure) as raised:
                f.private_read(path, os.geteuid(), os.getegid(), 6)
            self.assertEqual((raised.exception.stage, raised.exception.result), ("leaf-open", "denied"))

    def test_real_registered_materials_and_each_missing_or_bad_marker(self):
        for missing, bad, first in ((None, None, None), ("neighbor/sent.key", None, ("sample-neighbor-key", "leaf-open", "missing")),
                ("keyring.log", None, ("log-keyring", "leaf-open", "missing")),
                ("bus.log.complete", None, ("completion-bus", "leaf-open", "missing")),
                (None, "bus.log.complete", ("completion-bus", "marker-value", "rejected"))):
            with self.subTest(missing=missing, bad=bad), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for role in ("target", "neighbor"):
                    (root / role).mkdir(mode=0o700)
                for name, length in f.samples("exact"):
                    if name != missing:
                        self.write(root / name, b"q" * length)
                for name in f.log_names("exact"):
                    if name != missing:
                        self.write(root / name, b"")
                    if name + ".complete" != missing:
                        if name + ".complete" == bad:
                            self.write(root / (name + ".complete"), b"badmark!\n")
                        else:
                            f.mark_complete(root, name)
                report = f.Report()
                with mock.patch.object(f, "private_read", wraps=f.private_read) as read:
                    ok = f.scan_all(root, "exact", os.geteuid(), os.getegid(), (), report)
                self.assertEqual(ok, first is None)
                self.assertEqual(report.first_evidence, first)
                self.assertEqual(read.call_count, len(f.samples("exact")) + 2 * len(f.log_names("exact")))
                self.assertEqual(report.states["scan-content"], "clear")

    @unittest.skipUnless(sys.platform == "linux", "Linux no-follow private-file test not executed here")
    def test_linux_private_file_accepts_empty_log_but_not_wrong_sample(self):
        import os
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "public.log"
            descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            os.close(descriptor)
            self.assertEqual(f.private_read(path, os.geteuid(), os.getegid()), b"")
            with self.assertRaises(f.base.PreflightFailure):
                f.private_read(path, os.geteuid(), os.getegid(), 32)


if __name__ == "__main__":
    unittest.main(verbosity=2)
