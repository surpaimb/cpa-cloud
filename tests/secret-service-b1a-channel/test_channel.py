"""Public, independently authored C tests. No real password or GNOME."""
import base64
from dataclasses import replace
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time
import unittest

import channel as c
import fixture

PUBLIC = b"B1AC" + b"a" * 64
PURPOSE = "b" * 32


class Validation(unittest.TestCase):
    def reject(self, value, label):
        with self.assertRaises(c.Failure) as caught:
            c.validate(value)
        self.assertEqual(caught.exception.args, (label,))
        self.assertNotIn(PUBLIC.decode(), str(caught.exception))

    def test_valid(self):
        c.validate(PUBLIC)

    def test_empty(self):
        self.reject(b"", "c-empty")

    def test_nul(self):
        self.reject(PUBLIC[:-1] + b"\0", "c-encoding")

    def test_cr(self):
        self.reject(PUBLIC[:-1] + b"\r", "c-encoding")

    def test_lf(self):
        self.reject(PUBLIC[:-1] + b"\n", "c-encoding")

    def test_truncated(self):
        self.reject(PUBLIC[:-1], "c-length")

    def test_over_limit(self):
        self.reject(PUBLIC + b"b" * 129, "c-over-limit")

    def test_not_bytes(self):
        self.reject(PUBLIC.decode(), "c-encoding")

    def test_encoding(self):
        self.reject(b"B1AC" + b"Z" * 64, "c-encoding")

    def test_receipt_valid(self):
        c.check_receipt(b"COK1" + c.receipt(PUBLIC, PURPOSE), PUBLIC, PURPOSE)

    def test_receipt_wrong_bytes(self):
        with self.assertRaises(c.Failure):
            c.check_receipt(b"COK1" + c.receipt(PUBLIC, PURPOSE), b"B1AC" + b"b" * 64, PURPOSE)

    def test_receipt_wrong_purpose(self):
        with self.assertRaises(c.Failure):
            c.check_receipt(b"COK1" + c.receipt(PUBLIC, PURPOSE), PUBLIC, "c" * 32)

    def test_receipt_truncated(self):
        with self.assertRaises(c.Failure):
            c.check_receipt(b"COK1", PUBLIC, PURPOSE)

    def test_unknown_error_sanitized(self):
        self.assertEqual(c.Failure(PUBLIC.decode()).args, ("c-internal",))

    def test_scan_clear_complete_separate(self):
        self.assertEqual(c.scan(PUBLIC, {"a": (b"public", True)}, {"a"}), (True, True))
        self.assertEqual(c.scan(PUBLIC, {"a": (b"public", False)}, {"a"}), (True, False))

    def test_scan_missing_surface(self):
        self.assertEqual(c.scan(PUBLIC, {"a": (b"", True)}, {"a", "b"}), (True, False))

    def test_scan_unregistered_surface(self):
        self.assertEqual(c.scan(PUBLIC, {"a": (b"", True)}, set()), (True, False))

    def test_scan_raw_hex_base64(self):
        for data in (PUBLIC, PUBLIC.hex().encode(), base64.b64encode(PUBLIC)):
            self.assertEqual(c.scan(PUBLIC, {"a": (data, True)}, {"a"}), (False, True))

    def test_first_error_rule(self):
        def bad():
            raise OSError("public fault")
        self.assertEqual(fixture.cleanup_action("c-identity", bad), ("c-identity", False))
        self.assertEqual(fixture.cleanup_action(None, bad), ("c-cleanup", False))

    def test_cases_pregistered(self):
        self.assertEqual(set(fixture.CASES), set(fixture.EXPECTED))
        self.assertEqual(len(fixture.CASES), 15)

    def test_cannot_run_acceptance_here(self):
        # This test checks the guard, not the real isolation acceptance.
        if sys.platform != "linux":
            with self.assertRaises(c.Failure):
                fixture.main()

    def test_child_normal_wait(self):
        class Child:
            def poll(self):
                return 0
            def wait(self, timeout):
                return 0
        fixture.stop_child(Child())

    def test_child_wait_failure_not_repaired_by_kill(self):
        class Child:
            calls = 0
            def poll(self):
                return None
            def terminate(self):
                pass
            def kill(self):
                pass
            def wait(self, timeout):
                self.calls += 1
                if self.calls == 1:
                    raise subprocess.TimeoutExpired("public", timeout)
                return 0
        child = Child()
        with self.assertRaisesRegex(c.Failure, "c-wait"):
            fixture.stop_child(child)
        self.assertEqual(child.calls, 2)

    def test_child_wait_and_kill_failure_fixed(self):
        class Child:
            def poll(self):
                return None
            def terminate(self):
                raise OSError("public")
            def kill(self):
                raise OSError("public")
        with self.assertRaisesRegex(c.Failure, "c-wait"):
            fixture.stop_child(Child())


@unittest.skipUnless(sys.platform == "linux", "Linux anonymous pipe/procfs required")
class Pipes(unittest.TestCase):
    def setUp(self):
        self.r, self.w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        self.target = c.Target(os.getpid(), os.getuid(), "public", (1, 2), c.pipe_identity(self.w), b"public")
        self.closed = False
        self.neighbor_r, self.neighbor_w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        os.write(self.neighbor_w, b"public-neighbor")
        outer = self
        class IO(c.SystemIO):
            @staticmethod
            def identity(pid):
                return outer.target
        self.io = IO()

    def tearDown(self):
        for fd in (self.r, self.w, self.neighbor_r, self.neighbor_w):
            try:
                os.close(fd)
            except OSError:
                pass

    def sender(self, io=None, target=None, deadline=None):
        return c.OneShot(self.w, target or self.target, PURPOSE, deadline or time.monotonic() + 2, io or self.io)

    def unchanged(self):
        self.assertEqual(os.read(self.r, 256), b"")  # rejected sender closed: EOF, no bytes
        self.assertEqual(os.read(self.neighbor_r, 256), b"public-neighbor")

    def test_exact_bytes_eof_and_close(self):
        sender = self.sender()
        sender.send(PUBLIC, PURPOSE)
        self.assertTrue(sender.closed)
        self.assertEqual(os.read(self.r, 256), PUBLIC)
        self.assertEqual(os.read(self.r, 1), b"")
        self.assertEqual(os.read(self.neighbor_r, 256), b"public-neighbor")

    def test_empty_no_write_neighbor_unchanged(self):
        with self.assertRaises(c.Failure):
            self.sender().send(b"", PURPOSE)
        self.unchanged()

    def test_identity_no_write_neighbor_unchanged(self):
        with self.assertRaises(c.Failure):
            self.sender(target=replace(self.target, uid=self.target.uid + 1)).send(PUBLIC, PURPOSE)
        self.unchanged()

    def test_purpose_no_write_neighbor_unchanged(self):
        with self.assertRaises(c.Failure):
            self.sender().send(PUBLIC, "c" * 32)
        self.unchanged()

    def test_deadline_no_write_neighbor_unchanged(self):
        with self.assertRaises(c.Failure):
            self.sender(deadline=time.monotonic() - 1).send(PUBLIC, PURPOSE)
        self.unchanged()

    def test_duplicate_no_second_write(self):
        sender = self.sender()
        sender.send(PUBLIC, PURPOSE)
        with self.assertRaisesRegex(c.Failure, "c-reused"):
            sender.send(PUBLIC, PURPOSE)
        self.assertEqual(os.read(self.r, 256), PUBLIC)
        self.assertEqual(os.read(self.r, 1), b"")

    def test_rejected_attempt_consumed(self):
        sender = self.sender()
        with self.assertRaises(c.Failure):
            sender.send(b"", PURPOSE)
        with self.assertRaisesRegex(c.Failure, "c-reused"):
            sender.send(PUBLIC, PURPOSE)
        self.unchanged()

    def test_short_write_not_retried(self):
        class Short(type(self.io)):
            calls = 0
            def write(self, fd, value):
                self.calls += 1
                return os.write(fd, value[:-1])
        adapter = Short()
        with self.assertRaisesRegex(c.Failure, "c-short-write"):
            self.sender(io=adapter).send(PUBLIC, PURPOSE)
        self.assertEqual(adapter.calls, 1)
        self.assertEqual(os.read(self.r, 256), PUBLIC[:-1])
        self.assertEqual(os.read(self.r, 1), b"")

    def test_write_exception_closed(self):
        class Broken(type(self.io)):
            def write(self, fd, value):
                raise OSError("public fault")
        with self.assertRaisesRegex(c.Failure, "c-write"):
            self.sender(io=Broken()).send(PUBLIC, PURPOSE)
        self.unchanged()

    def test_identity_changes_after_write_fails_closed(self):
        class Change(type(self.io)):
            calls = 0
            def identity(self, pid):
                self.calls += 1
                return target if self.calls == 1 else replace(target, start="changed")
        target = self.target
        with self.assertRaisesRegex(c.Failure, "c-identity"):
            self.sender(io=Change()).send(PUBLIC, PURPOSE)
        self.assertEqual(os.read(self.r, 256), PUBLIC)
        self.assertEqual(os.read(self.r, 1), b"")

    def test_close_failure_does_not_replace_first_error(self):
        class BadClose(type(self.io)):
            def close(self, fd):
                c.close_checked(fd)
                raise OSError("public fault")
        with self.assertRaisesRegex(c.Failure, "c-empty"):
            self.sender(io=BadClose()).send(b"", PURPOSE)
        self.unchanged()

    def test_close_failure_prevents_success(self):
        class BadClose(type(self.io)):
            def close(self, fd):
                c.close_checked(fd)
                raise OSError("public fault")
        with self.assertRaisesRegex(c.Failure, "c-close"):
            self.sender(io=BadClose()).send(PUBLIC, PURPOSE)

    def test_fd_identity_mismatch(self):
        with self.assertRaisesRegex(c.Failure, "c-pipe"):
            self.sender(target=replace(self.target, pipe=(0, 0, 0, 0, 0)))
        self.assertEqual(os.read(self.neighbor_r, 256), b"public-neighbor")

    def test_capture_overflow_incomplete_not_clear(self):
        r, w = os.pipe()
        capture = c.Capture(os.fdopen(r, "rb", buffering=0), limit=3)
        os.write(w, b"public-overflow")
        c.close_checked(w)
        data, complete = capture.finish()
        self.assertFalse(complete)
        self.assertEqual(len(data), 3)

    def test_snapshot_symlink_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            (root / "link").symlink_to("missing")
            _, complete = fixture.snapshot_private([root], os.getuid())
            self.assertFalse(complete)

    def test_snapshot_fifo_incomplete_no_block(self):
        with tempfile.TemporaryDirectory() as name:
            os.mkfifo(Path(name) / "fifo", 0o600)
            started = time.monotonic()
            _, complete = fixture.snapshot_private([Path(name)], os.getuid())
            self.assertFalse(complete)
            self.assertLess(time.monotonic() - started, 1)

    def test_actual_receiver_bytes_eof_wait(self):
        purpose = secrets.token_hex(16)
        oracle = b"B1AC" + secrets.token_hex(32).encode("ascii")
        child = subprocess.Popen([sys.executable, "-B", str(Path(__file__).with_name("receiver.py")), purpose],
                                 stdin=self.r, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)
        os.close(self.r)
        self.r = -1
        try:
            hello = json.loads(c.read_line(child.stdout.fileno(), time.monotonic() + 3))
            target = c.observe(child.pid)
            self.assertEqual(target.pid, hello["pid"])
            self.assertEqual(target.pipe, c.pipe_identity(self.w))
            c.OneShot(self.w, target, purpose, time.monotonic() + 3).send(oracle, purpose)
            raw, err = child.communicate(timeout=3)
            self.assertEqual(child.returncode, 0)
            self.assertTrue(err == b"", "unexpected fixed receiver error")
            c.check_receipt(raw, oracle, purpose)
        finally:
            if child.poll() is None:
                child.kill()
            child.wait(timeout=3)

    def test_owned_wait_timeout_is_failure(self):
        commands = fixture.Commands()
        with self.assertRaisesRegex(c.Failure, "c-wait"):
            commands.run([sys.executable, "-c", "import time; time.sleep(2)"], timeout=.05)
        self.assertTrue(all(complete for _, complete in commands.inventory.values()))


if __name__ == "__main__":
    unittest.main()
