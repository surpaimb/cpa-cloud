"""Independent static/scanner tests; never call Secret Service."""
import ast
import base64
import importlib.util
import re
from pathlib import Path
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("interop", HERE / "fixture.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FixtureTests(unittest.TestCase):
    def test_forbidden_method_policy_and_no_activation(self):
        tree = ET.fromstring((HERE / "bus.conf.in").read_text())
        denied = {r.attrib.get("send_member") for r in tree.findall("policy/deny") if r.attrib.get("send_path") == "*"}
        self.assertTrue({"Unlock", "Prompt", "SetSecret", "Set", "CreateCollection", "Delete", "Lock"} <= denied)
        self.assertTrue(all(r.attrib.get("send_type") == "method_call" for r in tree.findall("policy/deny")))
        for forbidden in ("include", "includedir", "servicedir", "standard_session_servicedirs", "servicehelper"):
            self.assertEqual(tree.findall(".//" + forbidden), [])

    def test_original_isolation_is_called_before_any_daemon(self):
        source = (HERE / "fixture.py").read_text()
        self.assertLess(source.index("base.verify_isolation(metadata)"), source.index('bus = start('))
        self.assertNotIn("--unlock", source)
        self.assertNotIn("--login", source)

    def test_no_production_approval_and_no_overwrite(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service.rs").read_text()
        self.assertIn("#[cfg(test)]\nimpl SyntheticSecretServiceApproval", source)
        self.assertIn("replace: false", source)
        for method in ('"Unlock"', '"Prompt"', '"SetSecret"'):
            self.assertNotIn(method, source[:source.index("mod tests {")])

    def test_verifier_explicitly_refuses_prompt_and_never_mutates_items(self):
        source = (HERE / "verifier.c").read_text()
        self.assertIn("->prompt_sync = refuse_prompt", source)
        for forbidden in ("secret_service_unlock", "secret_service_store", "secret_item_set_secret", "secret_item_delete",
                          "secret_password_", "SECRET_SEARCH_UNLOCK"):
            self.assertNotIn(forbidden, source)

    def test_production_read_allowlist_is_exact_and_writes_remain_canonical(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service.rs").read_text()
        self.assertIn('const CONTENT_TYPE: &str = "application/octet-stream";', source)
        self.assertIn("content_type: CONTENT_TYPE,", source)
        self.assertIn("request.content_type.to_owned(),", source)
        start = source.index("fn validate_secret_transfer(")
        helper = source[start:source.index("\n}\n", start) + 3]
        # Compare the complete small guard, not just presence of allowed names:
        # an extra accepted type, normalization or bypass must fail this guard.
        expected = '''fn validate_secret_transfer(
            session_matches: bool, parameters: &[u8], content_type: &str,
            value: Zeroizing<Vec<u8>>,
        ) -> Result<Zeroizing<Vec<u8>>, BackendError> {
            if !session_matches || !parameters.is_empty()
                || !matches!(content_type, CONTENT_TYPE | "text/plain") {
                return Err(BackendError::Corrupt);
            }
            Ok(value)
        }'''
        compact = lambda text: re.sub(r"\s+", "", re.sub(r"//[^\n]*", "", text))
        self.assertEqual(compact(helper), compact(expected))

    def test_independent_verifier_requires_pinned_text_plain_binary_value(self):
        source = (HERE / "verifier.c").read_text()
        self.assertIn('if (!value || g_strcmp0(secret_value_get_content_type(value), "text/plain")) return 38;', source)
        self.assertNotIn("application/octet-stream", source)
        self.assertNotIn("validate_secret_transfer", source)
        self.assertIn("secret_value_get(value, &size)", source)
        self.assertIn("if (size != 148 || memcmp(bytes,", source)
        self.assertIn("memcmp(bytes + 84, expected, 32)", source)

    def test_scan_encodings(self):
        value = bytes(range(32))
        forms = module.secret_forms(value)
        self.assertIn(value, forms)
        self.assertIn(base64.b64encode(value), forms)
        self.assertIn(value.hex().upper().encode(), forms)
        self.assertIn(str(list(value)).encode(), forms)

    def test_scanner_rejects_each_encoding_without_echoing(self):
        for form in module.secret_forms(bytes(range(32))):
            with self.assertRaisesRegex(module.base.PreflightFailure, "^fixture-secret-output$"):
                module.reject_secret_output([bytes(range(32))], [b"before" + form + b"after"])
        module.reject_secret_output([bytes(range(32))], [b"CPA_FIXTURE_OK\n"])

    def test_only_fixed_failure_labels(self):
        for node in ast.walk(ast.parse((HERE / "fixture.py").read_text())):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "require":
                self.assertIsInstance(node.args[1], ast.Constant)
        for label in module.FAILURES + module.PANICS:
            self.assertRegex(label, r"^[a-z-]+$")

    def test_scanner_uses_only_presend_samples_on_success_and_failure(self):
        key, envelope = bytes(range(32)), bytes(range(148))
        for output in (b"CPA_FIXTURE_OK\n", b"fixture-create-failed\n"):
            with patch.object(module, "read_sample", side_effect=[key, envelope]) as reader:
                self.assertTrue(module.scan(Path("isolated"), [output]))
                self.assertEqual(reader.call_args_list[0].args, (Path("isolated/sent.key"), 32))
                self.assertEqual(reader.call_args_list[1].args, (Path("isolated/sent.envelope"), 148))
        for value in (key, envelope):
            for form in module.secret_forms(value):
                with patch.object(module, "read_sample", side_effect=[key, envelope]):
                    with self.assertRaisesRegex(module.base.PreflightFailure, "^fixture-secret-output$"):
                        module.scan(Path("isolated"), [b"fixture-create-failed\n" + form])

    def test_missing_samples_cannot_pass_even_with_response_oracle(self):
        for samples in ([None, None], [bytes(range(32)), None], [None, bytes(range(148))]):
            with patch.object(module, "read_sample", side_effect=samples):
                self.assertFalse(module.scan(Path("isolated"), [b"fixture-create-failed\n"]))
        source = (HERE / "fixture.py").read_text()
        scanner = source[source.index("def scan("):source.index("class Fixture:")]
        self.assertNotIn("expected.", scanner)
        self.assertNotIn("reference.bin", scanner)
        self.assertNotIn("hashlib", scanner)

    def test_same_response_diagnostics_and_presend_order(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service.rs").read_text()
        self.assertLess(source.index("real_fixture::capture_outgoing"), source.index("match backend.create_item(request)"))
        self.assertIn("#[cfg(test)]\n        if !real_fixture::capture_outgoing", source)
        self.assertIn("#[cfg(test)]\n        real_fixture::returned_secret", source)
        self.assertIn("validate_secret_transfer(session == self.session, &parameters, &content_type, value)", source)
        get_secret = source[source.index("impl SecretServiceBackend for DbusBackend"):source.index("fn validate_secret_transfer(")]
        self.assertLess(get_secret.index("let value = Zeroizing::new(value);"), get_secret.index("real_fixture::returned_secret"))
        caller = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service_fixture.rs").read_text()
        self.assertNotIn("backend.search", caller)
        self.assertNotIn("backend.get_secret", caller)
        self.assertIn("libc::O_EXCL", caller)
        self.assertIn("libc::O_NOFOLLOW", caller)
        for label in module.PANICS:
            if label.startswith("native-"):
                self.assertIn('"' + label + '"', caller)

    def test_cleanup_is_recorded_before_scan_failure(self):
        source = (HERE / "fixture.py").read_text()
        cleanup = source[source.index("    finally:"):]
        self.assertLess(cleanup.index("stop(process)"), cleanup.index('print("OWNED_PROCESSES_STOPPED"'))
        self.assertLess(cleanup.index('print("OWNED_PROCESSES_STOPPED"'), cleanup.index("require(scan(work, outputs)"))

    def test_decode_success_is_guarded_before_any_diagnostic(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service.rs").read_text()
        transport = source[source.index("impl SecretServiceBackend for DbusBackend"):source.index("fn validate_secret_transfer(")]
        get = transport[transport.index("    fn get_secret("):transport.index("    fn delete_item(")]
        self.assertNotIn("decoded.is_ok()", get)
        self.assertIn("decoded.map_err(|error| {", get)
        self.assertIn("#[cfg(test)]\n            real_fixture::decoded_result(false);", get)
        guarded = get.index("let value = Zeroizing::new(value);")
        self.assertLess(guarded, get.index("real_fixture::decoded_result(true)"))
        self.assertLess(guarded, get.index("real_fixture::returned_secret"))
        after_decode = get[get.index("Self::classify(&error)"):guarded]
        self.assertNotIn("real_fixture::", after_decode)

    def test_reopen_trace_is_fixed_complete_and_reads_no_extra_oracles(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service_fixture.rs").read_text()
        stages = source[source.index("const REOPEN_STAGES:"):source.index("struct ReopenTrace;")]
        self.assertEqual(set(re.findall(r'"([a-z-]+)"', stages)), set(module.REOPEN_STAGES))
        self.assertIn('(*stage, "not-reached")', source)
        self.assertIn("impl Drop for ReopenTrace", source)
        self.assertIn('ReopenTrace::start(mode == "reopen")', source)
        self.assertEqual(source.count("fs::read(path)"), 1)
        self.assertEqual(source.count("let original = sent_envelope(directory);"), 1)
        self.assertNotIn("backend.search", source)
        self.assertNotIn("backend.get_secret", source)
        for label in module.REOPEN_LABELS:
            self.assertEqual(module.diagnostic_labels((label + "\n").encode()), (label,))
        self.assertEqual(module.diagnostic_labels(b"arbitrary-path uid=123 raw-value unknown-mime"), ())
        self.assertEqual(module.diagnostic_labels(b"reopen-key-oracle-length-148"), ())

    def test_handoff_oracles_require_safe_creation_before_secret_write(self):
        source = (HERE.parent / "crates/employee-config-core/src/live/linux_secret_service_fixture.rs").read_text()
        writer = source[source.index("fn write_handoff_oracle("):source.index("fn write_handoff_oracles(")]
        self.assertIn("libc::O_EXCL", writer)
        self.assertIn("libc::O_NOFOLLOW", writer)
        self.assertIn("0o600", writer)
        self.assertIn("checked_oracle_metadata(&file, uid, 0)", writer)
        self.assertLess(writer.index("trace.check(2, before.is_some())?"), writer.index("file.write_all(bytes)"))
        self.assertLess(writer.index("file.sync_all()"), writer.index("let after = checked_oracle_metadata"))
        self.assertIn("libc::AT_SYMLINK_NOFOLLOW", writer)
        self.assertRegex(writer, r"entry\.st_dev == before\.dev\(\)\s*&& entry\.st_ino == before\.ino\(\)")
        self.assertNotIn("fs::read", writer)
        self.assertNotIn("set_permissions", writer)
        self.assertNotIn("libc::chmod", writer)
        caller = source[source.index("fn volatile_collection_interop() {"):]
        self.assertIn("write_handoff_oracles(&created)", caller)
        self.assertNotIn("OpenOptions", caller)
        stages = source[source.index("const ORACLE_WRITE_STAGES:"):source.index("struct OracleWriteTrace")]
        self.assertEqual(set(re.findall(r'"([a-z-]+)"', stages)), set(module.ORACLE_WRITE_STAGES))
        for label in module.ORACLE_WRITE_LABELS:
            self.assertEqual(module.diagnostic_labels((label + "\n").encode()), (label,))


if __name__ == "__main__":
    unittest.main()
