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


def writer_normalized_tree():
    """Only the three authorized opener substitutions normalize to old source."""
    tree = ast.parse((HERE / "fixture.py").read_text())
    pairs = (("private_log(path, self.m['uid'], self.m['gid'])", "path.open('xb')"),
             ("private_log(path, m['uid'], m['gid'])", "path.open('xb')"),
             ("private_log(work / (name + '.log'), m['uid'], m['gid'])", "(work / (name + '.log')).open('xb')"))
    allowed = {ast.dump(ast.parse(new, mode="eval").body): ast.parse(old, mode="eval").body for new, old in pairs}
    seen = []
    class Normalize(ast.NodeTransformer):
        def visit_Call(self, node):
            key = ast.dump(node)
            if key in allowed:
                seen.append(key)
                return allowed[key]
            return self.generic_visit(node)
    tree = Normalize().visit(tree)
    if len(seen) != 3 or len(set(seen)) != 3:
        raise AssertionError("writer-only-three-exact-call-sites")
    return tree


def without_crash_command(tree):
    """Freeze all parent source outside the expressly authorized diagnostics."""
    additions = {"CRASH_SCHEMA", "CRASH_DIAGNOSTICS", "crash_result", "fixed_child_failures", "JOURNAL_FAILURES", "JOURNAL_POINT_FAILURES"}
    tree.body = [node for node in tree.body if not (
        isinstance(node, ast.FunctionDef) and node.name in additions or
        isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id in additions for target in node.targets) or
        isinstance(node, ast.AugAssign) and isinstance(node.target, ast.Name) and isinstance(node.value, ast.Name)
        and (node.target.id, node.value.id) in (("DIAGNOSTICS", "CRASH_DIAGNOSTICS"), ("RUST_FAILURES", "JOURNAL_FAILURES"),
                                              ("RUST_FAILURES", "JOURNAL_POINT_FAILURES")))]
    owner = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Children")
    owner.body = [node for node in owner.body if not (isinstance(node, ast.FunctionDef) and node.name == "command")]
    return tree


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

    def test_historical_business_and_capture_flow_except_three_writer_calls(self):
        tree = without_crash_command(writer_normalized_tree())
        expected = {"mark_complete": "edc4a3394a6454dd581193a7d10f8b5dcf27c765b12d99bd26857a56c8f0d30a",
            "Children": "efffdb22637d5c65d444747e1a8936c650bb435eb39b5cfb3a5e2ea8d1a6af00",
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

    def test_writer_batch_freezes_entire_parent_module_except_helper_and_three_calls(self):
        tree = without_crash_command(writer_normalized_tree())
        tree.body = [node for node in tree.body if not (isinstance(node, (ast.ClassDef, ast.FunctionDef))
                     and node.name in ("PrivateLogWriter", "private_log"))]
        self.assertEqual(hashlib.sha256(ast.dump(tree, include_attributes=False).encode()).hexdigest(),
                         "f399279f56fbf53a4b795a65bfd258135d032f4b05440472dec9ddb1731347e4")

    def test_crash_batch_freezes_writer_reader_business_and_cleanup(self):
        source = ast.parse((HERE / "fixture.py").read_text())
        tree = without_crash_command(ast.parse((HERE / "fixture.py").read_text()))
        self.assertEqual(hashlib.sha256(ast.dump(tree, include_attributes=False).encode()).hexdigest(),
                         "d5b5602a7f2720372c31280150783b6e89122a2c51fac09247e6c15fdc862629")
        owner = next(node for node in source.body if isinstance(node, ast.ClassDef) and node.name == "Children")
        command = next(node for node in owner.body if isinstance(node, ast.FunctionDef) and node.name == "command")
        action = next(node for node in command.body if isinstance(node, ast.FunctionDef) and node.name == "action")
        main_try = next(node for node in action.body if isinstance(node, ast.Try) and node.finalbody)
        self.assertEqual(hashlib.sha256(ast.dump(ast.Module(body=main_try.finalbody, type_ignores=[]), include_attributes=False).encode()).hexdigest(),
                         "6be3556bcd8260619ee1692893132c1cd4b2609f63372c3858a774dc6543700c")
        gates = [node for node in ast.walk(command) if isinstance(node, ast.Call) and len(node.args) == 2
                 and isinstance(node.args[1], ast.Constant) and node.args[1].value == "a3-crash-not-observed"]
        expected = ast.parse('require(killed and ready_seen and status == -signal.SIGKILL, "a3-crash-not-observed")', mode="eval").body
        self.assertEqual([ast.dump(node) for node in gates], [ast.dump(expected)])

    def test_journal_batch_freezes_parent_except_fixed_labels_and_noncrash_filter(self):
        tree = ast.parse((HERE / "fixture.py").read_text())
        tree.body = [node for node in tree.body if not (
            isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id in ("JOURNAL_FAILURES", "JOURNAL_POINT_FAILURES") for target in node.targets) or
            isinstance(node, ast.AugAssign) and isinstance(node.target, ast.Name) and node.target.id == "RUST_FAILURES"
            and isinstance(node.value, ast.Name) and node.value.id in ("JOURNAL_FAILURES", "JOURNAL_POINT_FAILURES"))]
        condition = ast.dump(ast.parse("status != 0 and not crash", mode="eval").body)
        new = ast.parse('for fixed in fixed_child_failures(content):\n    print("FIXTURE_DIAGNOSTIC " + fixed, flush=True)').body[0]
        old = ast.parse('tokens = set(re.findall(rb"[a-z][a-z-]*", content))\nfor fixed in RUST_FAILURES + volatile.PANICS:\n    if fixed.encode() in tokens:\n        print("FIXTURE_DIAGNOSTIC " + fixed, flush=True)').body
        branches = [node for node in ast.walk(tree) if isinstance(node, ast.If) and ast.dump(node.test) == condition]
        self.assertEqual(len(branches), 1)
        self.assertEqual(ast.dump(branches[0].body[0]), ast.dump(new))
        branches[0].body = old + branches[0].body[1:]
        self.assertEqual(hashlib.sha256(ast.dump(tree, include_attributes=False).encode()).hexdigest(),
                         "c90cb8fe9573be237572c4723c5486313f1ff65b1822f67652c8a258c6a59c0b")

    def test_journal_result_diagnostics_are_the_only_rust_source_change(self):
        source = (LIVE / "linux_secret_service_a3_fixture.rs").read_text()
        source = re.sub(r"// A3_JOURNAL_POINTS_START\n[\s\S]*?// A3_JOURNAL_POINTS_END\n\n", "", source)
        pattern = r"// A3_JOURNAL_DIAGNOSTICS_START\n([\s\S]*?)// A3_JOURNAL_DIAGNOSTICS_END\n\n"
        regions = re.findall(pattern, source)
        self.assertEqual(len(regions), 1)
        helpers = regions[0].split("#[cfg(test)]")[0]
        for forbidden in (".expect(", "{:?}", "{error", "format!", ".display(", ".to_string(", ".raw_os_error(",
                          "fs::", "metadata(", "read(", "set_permissions("):
            self.assertNotIn(forbidden, helpers)
        self.assertIn("source.kind()", helpers)
        self.assertIn("match result", helpers)
        self.assertIn("Ok(value) => value", helpers)
        self.assertIn('panic!("a3-journal-create")', helpers)
        self.assertEqual(source.count("create_durable_cleanup_journal("), 1)
        normalized = re.sub(pattern, "", source)
        new = '''            let mut cleanup = a3_journal_result(create_durable_cleanup_journal(
                &root,
                receipt.into_terminal_handoff(),
            ));'''
        old = '''            let mut cleanup =
                create_durable_cleanup_journal(&root, receipt.into_terminal_handoff())
                    .expect("a3-journal-create");'''
        self.assertEqual(normalized.count(new), 1)
        normalized = normalized.replace(new, old)
        self.assertEqual(hashlib.sha256(normalized.encode()).hexdigest(),
                         "2cd74d2b84ee4ac7ab4743662ee76384d6c215083247a7b990947a39aa927005")
        enum_source = (LIVE / "cleanup_journal.rs").read_text().split("pub enum CleanupJournalError {", 1)[1].split("pub struct DurableCleanupJournal", 1)[0]
        families = set(re.findall(r"^    ([A-Z][A-Za-z0-9_]*) *(?:[,({])", enum_source, re.M))
        self.assertEqual(families, set(re.findall(r"CleanupJournalError::([A-Z][A-Za-z0-9_]*)", helpers)))
        self.assertEqual(len(f.JOURNAL_FAILURES), 39)
        self.assertEqual(len(set(f.JOURNAL_FAILURES)), 39)

    def test_point_batch_freezes_entire_parent_supervisor_except_static_tags(self):
        tree = ast.parse((HERE / "fixture.py").read_text())
        tree.body = [node for node in tree.body if not (
            isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "JOURNAL_POINT_FAILURES" for target in node.targets) or
            isinstance(node, ast.AugAssign) and isinstance(node.target, ast.Name) and node.target.id == "RUST_FAILURES"
            and isinstance(node.value, ast.Name) and node.value.id == "JOURNAL_POINT_FAILURES")]
        self.assertEqual(hashlib.sha256(ast.dump(tree, include_attributes=False).encode()).hexdigest(),
                         "0305c697ecbf5d9f969e8ccf87323f6b5e863a05105b34db189007b102e40351")

    def test_each_initial_return_point_is_bound_without_changing_predicates_or_io(self):
        source = (LIVE / "cleanup_journal.rs").read_text()
        points = re.findall(r"^[ ]*#\[cfg\(test\)\]\n[ ]*journal_conflict\(JournalConflictPoint::([A-Za-z]+)\);\n", source, re.M)
        expected = {
            "publish_event": ("pub(super) fn load(", ["RootBinding", "FinalObjectPresent", "PublishedFileRebound", "PublishedDirectoryRebound"]),
            "load_locked": ("fn open_root(", ["LoadedFileRebound", "NamespaceSnapshot", "LoadedDirectoryRebound"]),
            "verify_private_directory": ("fn require_no_posix_acl(", ["PrivateDirectoryAdmission"]),
            "require_no_posix_acl": ("fn lock_exclusive(", ["PosixAclPresent"]),
            "verify_private_file": ("fn set_mode(", ["PrivateFileAdmission"]),
        }
        self.assertEqual(len(points), 10)
        self.assertEqual(len(set(points)), 10)
        for name, (end, values) in expected.items():
            start = source.index("fn " + name + "(")
            body = source[start:source.index(end, start)]
            self.assertEqual(re.findall(r"journal_conflict\(JournalConflictPoint::([A-Za-z]+)\)", body), values)
        imports = r"    // A3_INITIAL_PUBLISH_IMPORT_START\n[\s\S]*?    // A3_INITIAL_PUBLISH_IMPORT_END\n\n"
        self.assertEqual(len(re.findall(imports, source)), 1)
        normalized = re.sub(imports, "", source)
        scope = r"^        #\[cfg\(test\)\]\n        let _a3_initial_publish = journal_initial_scope\(\);\n"
        self.assertEqual(len(re.findall(scope, normalized, re.M)), 1)
        normalized = re.sub(scope, "", normalized, flags=re.M)
        normalized = re.sub(r"^[ ]*#\[cfg\(test\)\]\n[ ]*journal_conflict\(JournalConflictPoint::[A-Za-z]+\);\n", "", normalized, flags=re.M)
        arm = '''            Ok(_) => {
                return Err(CleanupJournalError::RecoveryConflict(final_path));
            }'''
        self.assertEqual(normalized.count(arm), 1)
        normalized = normalized.replace(arm, "            Ok(_) => return Err(CleanupJournalError::RecoveryConflict(final_path)),")
        self.assertEqual(hashlib.sha256(normalized.encode()).hexdigest(),
                         "28de114b7883c779fa43db54cbdabe936dab5b6ed965fcc052207db8ac52b2c2")
        driver = (LIVE / "linux_secret_service_a3_fixture.rs").read_text()
        region = r"// A3_JOURNAL_POINTS_START\n([\s\S]*?)// A3_JOURNAL_POINTS_END\n\n"
        sections = re.findall(region, driver)
        self.assertEqual(len(sections), 1)
        self.assertEqual(hashlib.sha256(re.sub(region, "", driver).encode()).hexdigest(),
                         "67f0a8466c5dfb150582fa789c44e7ba3cdd1db085325f7ce2936819e3b18eb4")
        helpers = sections[0].split("#[cfg(test)]")[0]
        for forbidden in ("fs::", "fgetxattr(", ".metadata(", ".read(", ".display(", "raw_os_error(", "format!", "std::env", "Path"):
            self.assertNotIn(forbidden, helpers)
        self.assertIn("Cell::new(false)", helpers)
        self.assertIn("Cell::new(None)", helpers)
        self.assertIn("if !ENABLED.with(Cell::get) || !JOURNAL_INITIAL_ACTIVE.with(Cell::get)", helpers)
        self.assertIn('let _ = writeln!(io::stderr(), "{label}");', helpers)
        self.assertIn("std::marker::PhantomData<std::rc::Rc<()>>", helpers)

    def test_point_whitelist_transports_all_known_tags_and_no_unknown_text(self):
        self.assertEqual(len(f.JOURNAL_POINT_FAILURES), 10)
        self.assertEqual(len(set(f.JOURNAL_POINT_FAILURES)), 10)
        payload = b"DO-NOT-PRINT " + b" ".join(value.encode() for value in f.JOURNAL_POINT_FAILURES) + b" a3-journal-point-unknown"
        self.assertEqual(set(f.fixed_child_failures(payload)), set(f.JOURNAL_POINT_FAILURES))


class WriterTests(unittest.TestCase):
    @contextlib.contextmanager
    def environment(self, *, changes=None, states=None, open_error=None, write_result=None, close_error=None):
        info = types.SimpleNamespace(st_mode=f.stat.S_IFREG | 0o600, st_uid=1, st_gid=1,
                                     st_nlink=1, st_size=0, st_dev=2, st_ino=3)
        info.__dict__.update(changes or {})
        with contextlib.ExitStack() as stack:
            for name, flag in (("O_NOFOLLOW", 0x20000), ("O_CLOEXEC", 0x40000)):
                stack.enter_context(mock.patch.object(f.os, name, getattr(f.os, name, flag), create=True))
            opened = stack.enter_context(mock.patch.object(f.os, "open", return_value=31, side_effect=open_error))
            stated = stack.enter_context(mock.patch.object(f.os, "fstat", return_value=info, side_effect=states))
            written = stack.enter_context(mock.patch.object(f.os, "write", side_effect=write_result or (lambda fd, value: len(value))))
            closed = stack.enter_context(mock.patch.object(f.os, "close", side_effect=close_error))
            yield types.SimpleNamespace(info=info, opened=opened, stated=stated, written=written, closed=closed)

    def test_actual_writer_validates_same_fd_before_write_and_handoff(self):
        with self.environment() as env:
            log = f.private_log(Path("DO-NOT-PRINT"), 1, 1)
            self.assertEqual(env.stated.call_count, 1)
            self.assertEqual(env.written.call_count, 0)
            self.assertEqual(env.opened.call_args.args[2], 0o600)
            flags = env.opened.call_args.args[1]
            for bit in (os.O_WRONLY, os.O_CREAT, os.O_EXCL, getattr(os, "O_NOFOLLOW", 0x20000), getattr(os, "O_CLOEXEC", 0x40000)):
                self.assertTrue(flags & bit)
            self.assertEqual(log.write(b"public"), 6)
            log.flush()
            self.assertEqual(log.fileno(), 31)
            log.close()
            log.close()
            self.assertTrue(log.closed)
            self.assertEqual(env.opened.call_count, 1)
            self.assertEqual(env.written.call_args_list, [mock.call(31, b"public")])
            self.assertTrue(all(call == mock.call(31) for call in env.stated.call_args_list))
            self.assertEqual(env.closed.call_args_list, [mock.call(31)])

    def test_invalid_admission_closes_without_write_or_handoff(self):
        for changes in (dict(st_mode=f.stat.S_IFIFO | 0o600), dict(st_uid=2), dict(st_gid=2),
                        dict(st_mode=f.stat.S_IFREG | 0o644), dict(st_nlink=2), dict(st_size=1)):
            with self.subTest(changes=changes), self.environment(changes=changes) as env:
                with self.assertRaises(f.base.PreflightFailure) as error:
                    f.private_log(Path("DO-NOT-PRINT"), 1, 1)
                self.assertEqual(str(error.exception), "a3-private-metadata")
                self.assertEqual(env.written.call_count, 0)
                self.assertEqual(env.closed.call_args_list, [mock.call(31)])

    def test_exclusive_open_and_io_errors_are_fixed_and_never_retried(self):
        with self.environment(open_error=FileExistsError("DO-NOT-PRINT")) as env:
            with self.assertRaises(f.base.PreflightFailure) as error:
                f.private_log(Path("DO-NOT-PRINT"), 1, 1)
            self.assertEqual(str(error.exception), "a3-private-create")
            self.assertEqual(env.stated.call_count, 0)
            self.assertEqual(env.closed.call_count, 0)
        for result in ((lambda fd, value: 1), OSError("DO-NOT-PRINT")):
            with self.subTest(short=callable(result)), self.environment(write_result=result) as env:
                log = f.private_log(Path("DO-NOT-PRINT"), 1, 1)
                with self.assertRaises(f.base.PreflightFailure) as error:
                    log.write(b"public")
                self.assertEqual(str(error.exception), "a3-private-write")
                self.assertTrue(log.closed)
                self.assertEqual(env.written.call_count, 1)
                self.assertEqual(env.closed.call_count, 1)
                with self.assertRaises(f.base.PreflightFailure):
                    log.fileno()
                self.assertEqual(env.closed.call_count, 1)

    def test_close_failure_is_sticky_and_fd_is_not_retried(self):
        with self.environment(close_error=OSError("DO-NOT-PRINT")) as env:
            log = f.private_log(Path("DO-NOT-PRINT"), 1, 1)
            for action in (log.close, log.close, log.flush, log.fileno):
                with self.assertRaises(f.base.PreflightFailure) as error:
                    action()
                self.assertEqual(str(error.exception), "a3-private-write")
                self.assertTrue(error.exception.__suppress_context__)
            self.assertEqual(env.closed.call_count, 1)

    def test_identity_change_before_handoff_or_after_write_fails_closed(self):
        base_info = dict(st_mode=f.stat.S_IFREG | 0o600, st_uid=1, st_gid=1,
                         st_nlink=1, st_size=0, st_dev=2, st_ino=3)
        for field, value in (("st_uid", 2), ("st_gid", 2), ("st_ino", 4), ("st_dev", 4),
                             ("st_nlink", 2), ("st_mode", f.stat.S_IFREG | 0o644)):
            for after_write in (False, True):
                with self.subTest(field=field, after_write=after_write):
                    initial = types.SimpleNamespace(**base_info)
                    changed = types.SimpleNamespace(**(base_info | {field: value}))
                    with self.environment(states=[initial] * (2 if after_write else 1) + [changed]) as env:
                        log = f.private_log(Path("DO-NOT-PRINT"), 1, 1)
                        with self.assertRaises(f.base.PreflightFailure) as error:
                            log.write(b"public") if after_write else log.fileno()
                        self.assertEqual(str(error.exception), "a3-private-metadata")
                        self.assertTrue(log.closed)
                        self.assertEqual(env.written.call_count, int(after_write))
                        self.assertEqual(env.closed.call_count, 1)

    def test_actual_child_capture_completion_requires_eof_wait_sync_and_close(self):
        for fault in (None, "short-write", "sync", "close"):
            with self.subTest(fault=fault), self.environment(write_result=(lambda fd, value: 1) if fault == "short-write" else None,
                    close_error=OSError("DO-NOT-PRINT") if fault == "close" else None) as env:
                process = mock.Mock()
                process.wait.return_value = 0
                process.poll.return_value = 0
                selector = mock.Mock()
                selector.get_map.side_effect = [True, True, False]
                selector.select.return_value = [object()]
                metadata = dict(work="public-dummy", a3_case="exact", uid=1, gid=1,
                                a3_verifier="public-verifier", cpa="public-cpa")
                children = f.Children(metadata, {}, mock.Mock(), f.Report())
                with mock.patch.object(f.subprocess, "Popen", return_value=process), mock.patch.object(
                        f.selectors, "DefaultSelector", return_value=selector), mock.patch.object(
                        f.os, "read", side_effect=[b"A3_LIBSECRET_OK\n", b""]), mock.patch.object(
                        f.os, "fsync", side_effect=OSError("DO-NOT-PRINT") if fault == "sync" else None) as sync, mock.patch.object(f, "mark_complete") as signed:
                    if fault == "short-write":
                        with self.assertRaises(f.base.PreflightFailure):
                            children.command("setup", ["public-verifier"])
                    else:
                        children.command("setup", ["public-verifier"])
                    self.assertEqual(signed.call_count, int(fault is None))
                    self.assertTrue(children.handles["setup"].closed)
                    self.assertEqual(env.closed.call_count, 1)
                    if signed.called:
                        self.assertTrue(selector.unregister.called)
                        self.assertTrue(process.wait.called)
                        self.assertTrue(sync.called)
                    else:
                        self.assertNotEqual(children.report.states["handles"], "ok")

    def test_actual_duplicate_capture_only_signs_after_successful_sync_and_close(self):
        for fault in (None, "short-write", "sync", "close"):
            with self.subTest(fault=fault), self.environment(write_result=(lambda fd, value: 1) if fault == "short-write" else None,
                    close_error=OSError("DO-NOT-PRINT") if fault == "close" else None) as env:
                children = mock.Mock()
                children.cpa.return_value = b"A3_DELETE_SEND_ZERO\n"
                metadata = dict(work="public-dummy", a3_case="duplicate", uid=1, gid=1,
                                verifier="public-verifier", a3_verifier="public-a3-verifier", a2_helper="public-a2-helper")
                def capture(m, environment, values, identity, report):
                    values.append(b"public-duplicate")
                with mock.patch.object(f.a2, "cases", side_effect=capture), mock.patch.object(
                        f.os, "fsync", side_effect=OSError("DO-NOT-PRINT") if fault == "sync" else None) as sync, mock.patch.object(
                        f, "mark_complete") as signed, contextlib.redirect_stdout(io.StringIO()):
                    if fault:
                        with self.assertRaises((f.base.PreflightFailure, OSError)):
                            f.sequence(metadata, {}, mock.Mock(), f.Report(), children)
                    else:
                        f.sequence(metadata, {}, mock.Mock(), f.Report(), children)
                    self.assertEqual(signed.call_count, int(fault is None))
                    self.assertEqual(children.cpa.call_count, int(fault is None))
                    self.assertEqual(env.closed.call_count, 1)
                    if signed.called:
                        self.assertTrue(sync.called)

    def test_actual_daemon_openers_validate_before_handoff_and_close_before_signing(self):
        # Entire inside flow is mocked public control traffic, not a daemon run.
        for fault in (None, "short-write", "close"):
            with self.subTest(fault=fault), self.environment(write_result=(lambda fd, value: 1) if fault == "short-write" else None,
                    close_error=OSError("DO-NOT-PRINT") if fault == "close" else None) as env:
                processes, logs = [], []
                def launch(args, **kwargs):
                    log = kwargs["stdout"]
                    self.assertEqual(log.fileno(), 31)
                    self.assertFalse(log.closed)
                    logs.append(log)
                    process = mock.Mock()
                    process.poll.return_value = None
                    processes.append(process)
                    return process
                def stop(process):
                    process.poll.return_value = 0
                def sign(work, name):
                    self.assertTrue(all(process.poll() == 0 for process in processes))
                    self.assertTrue(all(log.closed for log in logs))
                metadata = dict(work="public-dummy", a3_case="exact", uid=1, gid=1, bus_config="public-policy", observer="public-observer",
                    a3_samples=[list(value) for value in f.samples("exact")], a3_logs=list(f.log_names("exact")),
                    a3_completions=[name + ".complete" for name in f.log_names("exact")])
                with mock.patch.object(f.Path, "read_text", return_value="public-json"), mock.patch.object(f.json, "loads", return_value=metadata), mock.patch.object(
                        f.base, "verify_isolation"), mock.patch.object(f.base, "owned_process"), mock.patch.object(f.Path, "exists", return_value=True), mock.patch.object(
                        f.base, "run", return_value=types.SimpleNamespace(returncode=0, stdout=b"PREFLIGHT_LIBSECRET_OK\n")), mock.patch.object(
                        f.subprocess, "Popen", side_effect=launch), mock.patch.object(f.a2, "stop", side_effect=stop), mock.patch.object(f, "sequence"), mock.patch.object(
                        f, "scan_all", return_value=True), mock.patch.object(f.os, "fsync"), mock.patch.object(f, "mark_complete", side_effect=sign) as signed, contextlib.redirect_stdout(io.StringIO()):
                    if fault:
                        with self.assertRaises(f.base.PreflightFailure):
                            f.inside("public-metadata")
                    else:
                        f.inside("public-metadata")
                self.assertEqual(len(logs), 2)
                self.assertEqual(signed.call_count, 0 if fault else 2)
                self.assertEqual(env.closed.call_count, 2)


class CrashControlTests(unittest.TestCase):
    def setUp(self):
        # Public control-flow model only: Windows has no real POSIX SIGKILL.
        patch = mock.patch.object(f.signal, "SIGKILL", getattr(f.signal, "SIGKILL", 9), create=True)
        patch.start()
        self.addCleanup(patch.stop)

    def run_case(self, *, ready_seen=True, boundary_seen=True, status=None, kill_error=None,
                 owned_error=None, wait_error=None, capture_error=None, close_error=None,
                 extra=b"DO-NOT-PRINT public-private-path", nested_failure=False, crash=True, prior_first=False):
        payload = (b"A3_SNAPSHOT_READY\n" if ready_seen else b"") + (b"A3_BOUNDARY terminal\n" if boundary_seen else b"") + extra
        with WriterTests.environment(self, close_error=close_error) as env:
            process = mock.Mock()
            process.wait.return_value = status
            process.wait.side_effect = wait_error
            process.kill.side_effect = kill_error
            process.poll.return_value = 0  # Cleanup is mocked; never signal a real process.
            selector = mock.Mock()
            selector.get_map.return_value = True
            selector.select.return_value = [object()]
            if capture_error:
                selector.select.side_effect = [[object()], capture_error]
            report = f.Report()
            if prior_first:
                try:
                    report.run("snapshot-target", lambda: (_ for _ in ()).throw(ValueError("DO-NOT-PRINT")))
                except ValueError:
                    pass
            metadata = dict(work="public-dummy", a3_case="complete-terminal", uid=1, gid=1,
                            a3_verifier="public-verifier", cpa="public-cpa")
            children = f.Children(metadata, {}, mock.Mock(), report)
            def ready():
                if nested_failure:
                    report.run("snapshot-target", lambda: (_ for _ in ()).throw(ValueError("DO-NOT-PRINT")))
            output, result, failure = io.StringIO(), None, None
            with mock.patch.object(f.subprocess, "Popen", return_value=process) as launched, mock.patch.object(
                    f.selectors, "DefaultSelector", return_value=selector), mock.patch.object(
                    f.os, "read", side_effect=[payload, b""]) as read, mock.patch.object(f.os, "fsync"), mock.patch.object(
                    f.base, "owned_process", side_effect=owned_error) as owned, mock.patch.object(f, "mark_complete") as signed, contextlib.redirect_stdout(output):
                try:
                    result = children.command("start" if crash else "setup", ["public-cpa"],
                        mode="durable-start" if crash else None, crash="terminal" if crash else None, ready=ready if crash else None)
                except Exception as error:
                    failure = error
            labels = [line.removeprefix("FIXTURE_DIAGNOSTIC ") for line in output.getvalue().splitlines()]
            for label in labels:
                self.assertIn(label, f.Fixture.diagnostics)
            for private in ("DO-NOT-PRINT", "public-private-path", "123456789"):
                self.assertNotIn(private, output.getvalue())
            self.assertEqual(launched.call_count, 1)
            self.assertEqual(env.opened.call_count, 1)
            self.assertLessEqual(read.call_count, 2)  # No diagnostic reread of output.
            self.assertLessEqual(process.wait.call_count, 1)
            self.assertLessEqual(process.kill.call_count, 1)
            return types.SimpleNamespace(result=result, error=failure, labels=labels, report=report,
                killed=process.kill.call_count, owned=owned.call_count, waited=process.wait.call_count,
                signed=signed.call_count, reads=read.call_count)

    def assert_result(self, case, result):
        self.assertEqual([label for label in case.labels if label.startswith("a3-crash-result-")], ["a3-crash-result-" + result])

    def test_expected_sigkill_keeps_exact_gate_and_does_not_screen_error_tags(self):
        case = self.run_case(status=-f.signal.SIGKILL, extra=b"DO-NOT-PRINT a3-journal-create\n")
        self.assertIsNone(case.error)
        self.assertIsNone(case.report.first)
        self.assertEqual((case.owned, case.killed, case.waited, case.signed), (1, 1, 1, 1))
        self.assert_result(case, "expected-sigkill")
        self.assertIn("a3-crash-exit-sigkill", case.labels)
        self.assertIn("a3-crash-rust-labels-not-screened", case.labels)
        self.assertNotIn("a3-journal-create", case.labels)

    def test_early_exit_without_boundary_retains_whitelist_and_complete_capture(self):
        for status in (0, 123456789, -f.signal.SIGTERM, -f.signal.SIGKILL):
            with self.subTest(status=status):
                case = self.run_case(boundary_seen=False, status=status, extra=b"DO-NOT-PRINT a3-journal-create\nUNTRUSTED-a3-fake-tag")
                self.assertIsNotNone(case.error)
                self.assertEqual(case.report.first, "start")
                self.assert_result(case, "early-exit")
                self.assertIn("a3-crash-boundary-not-reached", case.labels)
                self.assertIn("a3-crash-capture-eof-wait-complete", case.labels)
                self.assertIn("a3-crash-rust-labels-present", case.labels)
                self.assertIn("a3-journal-create", case.labels)
                self.assertEqual((case.owned, case.killed, case.waited, case.signed), (0, 0, 1, 1))

    def test_missing_ready_or_other_exit_never_becomes_expected_sigkill(self):
        case = self.run_case(ready_seen=False, status=-f.signal.SIGKILL)
        self.assertIsNotNone(case.error)
        self.assert_result(case, "ready-not-seen")
        for status, exit_class in ((0, "clean"), (123456789, "nonzero"), (-f.signal.SIGTERM, "other-signal")):
            with self.subTest(exit_class=exit_class):
                case = self.run_case(status=status)
                self.assertIsNotNone(case.error)
                self.assert_result(case, "other-exit")
                self.assertIn("a3-crash-exit-" + exit_class, case.labels)
                self.assertIn("a3-crash-kill-returned", case.labels)
                self.assertIn("a3-crash-rust-labels-absent", case.labels)

    def test_kill_identity_or_kill_exception_is_not_completed_and_not_retried(self):
        for identity in (True, False):
            with self.subTest(identity=identity):
                kwargs = {"owned_error" if identity else "kill_error": OSError("DO-NOT-PRINT")}
                case = self.run_case(status=-f.signal.SIGKILL, **kwargs)
                self.assertIsNotNone(case.error)
                self.assert_result(case, "kill-not-completed")
                self.assertIn("a3-crash-boundary-seen", case.labels)
                self.assertIn("a3-crash-kill-not-completed", case.labels)
                self.assertIn("a3-crash-exit-not-observed", case.labels)
                self.assertEqual((case.owned, case.killed, case.waited, case.signed), (1, 0 if identity else 1, 0, 0))

    def test_wait_and_capture_failure_reuse_partial_memory_and_forbid_completion(self):
        for phase, error in (("wait", subprocess.TimeoutExpired("DO-NOT-PRINT", 1)), ("wait", OSError("DO-NOT-PRINT")),
                             ("capture", TimeoutError("DO-NOT-PRINT")), ("capture", OSError("DO-NOT-PRINT"))):
            with self.subTest(phase=phase, kind=type(error).__name__):
                case = self.run_case(status=-f.signal.SIGKILL, extra=b"DO-NOT-PRINT a3-journal-create\n", **{phase + "_error": error})
                self.assertIsNotNone(case.error)
                self.assert_result(case, phase + "-failed")
                self.assertIn("a3-journal-create", case.labels)
                self.assertIn("a3-crash-capture-incomplete", case.labels)
                self.assertIn("a3-crash-exit-not-observed", case.labels)
                self.assertEqual(case.signed, 0)
                self.assertEqual(case.waited, int(phase == "wait"))

    def test_nested_first_fault_and_crash_result_survive_cleanup_failure(self):
        case = self.run_case(status=-f.signal.SIGKILL, nested_failure=True, close_error=OSError("DO-NOT-PRINT"))
        self.assertIsNotNone(case.error)
        self.assertEqual(case.report.first, "snapshot-target")
        self.assert_result(case, "capture-failed")
        self.assertEqual(case.report.states["handles"], "failed")
        self.assertEqual(case.signed, 0)
        case = self.run_case(status=-f.signal.SIGKILL, kill_error=OSError("DO-NOT-PRINT"), close_error=OSError("DO-NOT-PRINT"))
        self.assertEqual(case.report.first, "start")
        self.assert_result(case, "kill-not-completed")

    def test_result_schema_has_no_raw_status_and_boundary_absence_is_explicit(self):
        values = f.crash_result(False, False, False, None, False)
        self.assertEqual(values["result"], "boundary-not-reached")
        self.assertEqual(values["exit"], "not-observed")
        for field, value in values.items():
            self.assertIn(value, f.CRASH_SCHEMA[field])
        with self.assertRaises(f.base.PreflightFailure):
            f.crash_result(False, False, False, None, False, "DO-NOT-PRINT")

    def test_noncrash_existing_digit_labels_and_new_journal_families_are_not_split(self):
        case = self.run_case(crash=False, ready_seen=False, boundary_seen=False, status=123456789,
            extra=b"DO-NOT-PRINT public-private-path a3-journal-create a3-journal-family-io a3-journal-io-permission-denied a3-unregistered-tag")
        self.assertIsNotNone(case.error)
        self.assertEqual(case.report.first, "setup")
        self.assertEqual(set(case.labels), {"a3-journal-create", "a3-journal-family-io", "a3-journal-io-permission-denied"})
        self.assertEqual((case.reads, case.waited, case.killed, case.signed), (2, 1, 0, 1))

    def test_noncrash_unknown_labels_do_not_escape_or_override_first_fault(self):
        case = self.run_case(crash=False, ready_seen=False, boundary_seen=False, status=123456789, prior_first=True,
            extra=b"DO-NOT-PRINT a3-unregistered-tag PUBLIC_RAW_EXCEPTION")
        self.assertIsNotNone(case.error)
        self.assertEqual(case.report.first, "snapshot-target")
        self.assertEqual(case.labels, [])
        self.assertEqual((case.reads, case.waited, case.killed), (2, 1, 0))

    def test_noncrash_success_and_missing_expected_marker_keep_original_judgment(self):
        for valid in (True, False):
            with self.subTest(valid=valid):
                case = self.run_case(crash=False, ready_seen=False, boundary_seen=False, status=0,
                    extra=(b"A3_LIBSECRET_OK\n" if valid else b"") + b"DO-NOT-PRINT a3-journal-create")
                self.assertEqual(case.error is None, valid)
                self.assertEqual(case.report.first, None if valid else "setup")
                self.assertEqual(case.labels, [])
                self.assertEqual((case.reads, case.waited, case.killed, case.signed), (2, 1, 0, 1))


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
                if name == "log":
                    with f.private_log(root / name, os.geteuid(), os.getegid()) as log:
                        log.write(value)
                else:
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
                        with f.private_log(root / name, os.geteuid(), os.getegid()):
                            pass
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
            with f.private_log(path, os.geteuid(), os.getegid()):
                pass
            self.assertEqual(f.private_read(path, os.geteuid(), os.getegid()), b"")
            with self.assertRaises(f.base.PreflightFailure):
                f.private_read(path, os.geteuid(), os.getegid(), 32)


@unittest.skipUnless(sys.platform == "linux", "Linux actual-writer filesystem tests not executed here")
class LinuxWriterTests(unittest.TestCase):
    def test_actual_writer_to_reader_under_different_creation_environments(self):
        for mask in (0, 0o022, 0o077, 0o777):
            with self.subTest(mask=mask), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "public.log"
                previous = os.umask(mask)
                try:
                    if mask == 0o777:
                        with self.assertRaises(f.base.PreflightFailure):
                            f.private_log(path, os.geteuid(), os.getegid())
                        continue  # Refuse, never repair this deliberately unsafe creation.
                    log = f.private_log(path, os.geteuid(), os.getegid())
                finally:
                    os.umask(previous)
                with log:
                    log.write(b"public-dummy")
                    log.flush()
                    os.fsync(log.fileno())
                self.assertTrue(log.closed)
                f.mark_complete(path.parent, path.name)
                self.assertEqual(f.private_read(path, os.geteuid(), os.getegid()), b"public-dummy")
                self.assertEqual(f.private_read(path.with_name(path.name + ".complete"), os.geteuid(), os.getegid(), 9), b"complete\n")

    def test_all_case_registered_logs_use_actual_writer_then_strict_scan(self):
        for case in f.CASES:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for role in ("target", "neighbor"):
                    (root / role).mkdir(mode=0o700)
                for name, length in f.samples(case):
                    LinuxReaderTests.write(root / name, b"q" * length)
                for index, name in enumerate(f.log_names(case)):
                    with f.private_log(root / name, os.geteuid(), os.getegid()) as log:
                        log.write(b"public-output" if index % 2 else b"")
                        log.flush()
                        os.fsync(log.fileno())
                    f.mark_complete(root, name)
                report = f.Report()
                self.assertTrue(f.scan_all(root, case, os.geteuid(), os.getegid(), (), report))
                self.assertEqual([report.integrity[k] for k in ("samples", "logs", "completions")], ["complete"] * 3)

    def test_public_child_receives_validated_actual_writer_fd(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "public.log"
            with f.private_log(path, os.geteuid(), os.getegid()) as log:
                process = subprocess.Popen([sys.executable, "-B", "-c", "import os; os.write(1,b'public-child')"],
                    stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT)
                self.assertEqual(process.wait(timeout=3), 0)
                log.flush()
                os.fsync(log.fileno())
            f.mark_complete(path.parent, path.name)
            self.assertEqual(f.private_read(path, os.geteuid(), os.getegid()), b"public-child")

    def test_existing_symlink_fifo_and_post_creation_hardlink_fail_without_repair(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "public.log"
            log = f.private_log(path, os.geteuid(), os.getegid())
            os.link(path, root / "public.alias")
            with self.assertRaises(f.base.PreflightFailure):
                log.write(b"public")
            self.assertTrue(log.closed)
            self.assertFalse((root / "public.log.complete").exists())
            (root / "public.link").symlink_to(path)
            os.mkfifo(root / "public.fifo", 0o600)
            for name in ("public.log", "public.link", "public.fifo"):
                with self.subTest(name=name), self.assertRaises(f.base.PreflightFailure):
                    f.private_log(root / name, os.geteuid(), os.getegid())
            self.assertEqual(path.stat().st_size, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
