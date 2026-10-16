"""Independent static/scanner tests; never call Secret Service."""
import ast
import base64
import importlib.util
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
        self.assertIn("if session != self.session || !parameters.is_empty() || content_type != CONTENT_TYPE", source)
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


if __name__ == "__main__":
    unittest.main()
