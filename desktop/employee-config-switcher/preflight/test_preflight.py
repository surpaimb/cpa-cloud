"""Independent nonprivileged static guard tests; never start a bus or service."""
import ast
import importlib.util
import json
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("preflight", HERE / "run.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PreflightTests(unittest.TestCase):
    def test_bus_denies_secret_interfaces_and_property_writes(self):
        tree = ET.fromstring((HERE / "bus.conf.in").read_text())
        denied = {element.attrib.get("send_interface") for element in tree.findall("policy/deny")}
        for interface in ("Service", "Collection", "Item", "Prompt"):
            self.assertIn("org.freedesktop.Secret." + interface, denied)
        self.assertTrue(any(e.attrib.get("send_member") == "Set" for e in tree.findall("policy/deny")))
        members = {e.attrib.get("send_member") for e in tree.findall("policy/deny") if "send_interface" not in e.attrib}
        self.assertTrue({"OpenSession", "CreateCollection", "CreateItem", "GetSecret", "GetSecrets",
                         "SetSecret", "Delete", "Unlock", "Prompt", "Set"}.issubset(members))
        for forbidden in ("include", "includedir", "servicedir", "standard_session_servicedirs", "servicehelper"):
            self.assertEqual(tree.findall(".//" + forbidden), [])
        self.assertEqual(tree.findtext("auth"), "EXTERNAL")

    def test_observer_has_no_secret_or_prompt_calls(self):
        source = (HERE / "libsecret-observer.c").read_text()
        self.assertIn("secret_service_get_sync(SECRET_SERVICE_NONE", source)
        for forbidden in ("secret_password_", "secret_item_", "secret_collection_", "secret_prompt_",
                          "secret_service_unlock", "secret_service_store", "secret_service_search",
                          '"OpenSession"', '"Unlock"', '"Prompt"', '"SetSecret"', '"CreateItem"', '"Delete"'):
            self.assertNotIn(forbidden, source)

    def test_all_failure_labels_are_source_literals(self):
        tree = ast.parse((HERE / "run.py").read_text())
        for node in ast.walk(tree):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name):
                if node.func.id == "require":
                    self.assertIsInstance(node.args[1], ast.Constant)
                elif node.func.id == "PreflightFailure" and not isinstance(node.args[0], ast.Name):
                    self.assertIsInstance(node.args[0], ast.Constant)

    def test_exact_component_versions_and_sources(self):
        manifest = json.loads((HERE / "components.json").read_text())
        self.assertEqual(set(manifest), {"dbus-daemon", "gnome-keyring", "libsecret-1-0", "libsecret-1-dev"})
        for value in manifest.values():
            self.assertRegex(value["version"], r"^\d[^\s*]+$")
            self.assertTrue(value["source"].startswith("https://packages.ubuntu.com/"))
            self.assertTrue(value["license"])

    def test_guard_failure_is_fixed_label(self):
        with self.assertRaisesRegex(module.PreflightFailure, "^fixed-label$"):
            module.require(False, "fixed-label")

    def test_baseline_runner_does_not_depend_on_sbin_path(self):
        source = (HERE / "run.py").read_text()
        self.assertIn('["/usr/sbin/runuser", "-u", USER', source)
        self.assertNotIn('["runuser",', source)

    def test_diagnostics_emit_only_fixed_categories(self):
        for label, fragment in module.BUS_DIAGNOSTICS.items():
            self.assertRegex(label, r"^[a-z-]+$")
            self.assertIsInstance(fragment, bytes)
            self.assertNotIn("/", label)


if __name__ == "__main__":
    unittest.main()
