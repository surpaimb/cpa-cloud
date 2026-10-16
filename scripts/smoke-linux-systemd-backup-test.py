"""Independently authored harness checks; no privileged or systemd execution."""

import base64
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "systemd_acceptance", Path(__file__).with_name("smoke-linux-systemd-backup.py"))
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class HarnessTests(unittest.TestCase):
    def test_clean_progress(self):
        harness.clean_log(b"PASS: restore\nEXPECTED_REJECTION wrong-backup\n")

    def test_secret_encodings_rejected_without_echo(self):
        values = list(harness.SENTINELS)
        for key in harness.KEYS:
            values.extend((key, key.hex().encode(), key.hex().upper().encode(),
                           base64.b64encode(key), base64.b64encode(key).rstrip(b"="),
                           base64.urlsafe_b64encode(key).rstrip(b"=")))
        for value in values:
            with self.subTest(length=len(value)):
                with self.assertRaisesRegex(RuntimeError, "^secret-log-rejected$"):
                    harness.clean_log(b"prefix " + value + b" suffix")

    def test_positive_materials_independent_and_same_value_negative(self):
        fixtures = harness.fixtures()
        master = fixtures["master-good"][1]
        backup = fixtures["backup-good"][1]
        same = fixtures["backup-same"][1]
        self.assertEqual(master["instance_id"], backup["instance_id"])
        self.assertNotEqual(master["key_id"], backup["key_id"])
        self.assertNotEqual(master["master_key"], backup["wrapping_key"])
        self.assertNotEqual(master["key_id"], same["key_id"])
        self.assertEqual(master["master_key"], same["wrapping_key"])

    def test_wrong_materials_retain_reference(self):
        fixtures = harness.fixtures()
        for role, field in (("master", "master_key"), ("backup", "wrapping_key")):
            original = fixtures[role + "-good"][1].copy()
            wrong = fixtures[role + "-wrong"][1].copy()
            self.assertNotEqual(original.pop(field), wrong.pop(field))
            self.assertEqual(original, wrong)

    def test_swaps_are_encrypted_for_destination_name_not_name_mismatch(self):
        fixtures = harness.fixtures()
        self.assertEqual(fixtures["master-swapped"][0], harness.MASTER)
        self.assertEqual(fixtures["backup-swapped"][0], harness.BACKUP)
        self.assertEqual(fixtures["master-swapped"][1], fixtures["backup-good"][1])
        self.assertEqual(fixtures["backup-swapped"][1], fixtures["master-good"][1])


if __name__ == "__main__":
    unittest.main()
