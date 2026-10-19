#!/usr/bin/env python3
"""Public dummy/source tests only. Never start a bus, daemon or CPA process."""
import contextlib
import importlib.util
import io
import re
from pathlib import Path
import tempfile
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
        def read(path, uid, gid, length=None):
            calls.append(str(path))
            if path.name == missing:
                raise FileNotFoundError()
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

    @unittest.skipUnless(__import__("sys").platform == "linux", "Linux no-follow private-file test not executed here")
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
