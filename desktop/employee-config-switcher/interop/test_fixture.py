"""Independent static/scanner tests; never call Secret Service."""
import ast
import base64
import importlib.util
from pathlib import Path
import unittest
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


if __name__ == "__main__":
    unittest.main()
