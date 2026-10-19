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


class OwnershipRegressions(unittest.TestCase):
    def test_second_directory_failure_cleans_first(self):
        released = []
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        with self.assertRaisesRegex(c.Failure, "c-setup"):
            with owned:
                owned.acquire("root", lambda: "public-root", lambda value: released.append(value))
                def rejected():
                    raise c.Failure("c-setup")
                owned.acquire("host", rejected, lambda value: released.append("unowned"))
        self.assertEqual(released, ["public-root"])

    def test_post_acquire_mode_or_socket_failure_still_cleans(self):
        for stage in ("root", "host", "socket"):
            released = []
            owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
            with self.assertRaisesRegex(c.Failure, "c-isolation"):
                with owned:
                    owned.acquire(stage, lambda: "public", lambda value: released.append(value))
                    raise c.Failure("c-isolation")
            self.assertEqual(released, ["public"])

    def test_cleanup_continues_and_keeps_first(self):
        actions = []
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        owned.own("root", "public", lambda _: actions.append("root"))
        def bad(_):
            actions.append("host")
            raise OSError("public bait " + PUBLIC.decode())
        owned.own("host", "public", bad)
        self.assertEqual(owned.cleanup("c-identity"), ("c-identity", False))
        self.assertEqual(actions, ["host", "root"])

    def test_acquisition_failure_never_takes_unconfirmed_object(self):
        actions = []
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        def bad():
            raise OSError("public object not acquired")
        with self.assertRaises(OSError):
            owned.acquire("socket", bad, lambda _: actions.append("unowned"))
        self.assertEqual(owned.cleanup(), (None, True))
        self.assertEqual(actions, [])

    def test_popen_capture_constructor_failure_is_guarded(self):
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        owner = None
        def bad_capture(*args, **kwargs):
            raise RuntimeError("public capture initialization bait")
        with self.assertRaises(RuntimeError):
            with owned:
                owner = owned.process("helper-process", lambda: subprocess.Popen(
                    [sys.executable, "-c", "import time; time.sleep(2)"], stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE), capture_factory=bad_capture)
                owner.capture("out")
        self.assertIsNotNone(owner.process.poll())
        self.assertTrue(owner.streams["out"].consumed and owner.streams["out"].confirmed)
        self.assertTrue(owner.streams["err"].consumed and owner.streams["err"].confirmed)

    def test_diagnostic_whitelist_and_bait(self):
        lines = []
        diag = fixture.Diagnostics(sink=lines.append)
        with self.assertRaises(OSError):
            diag.run("user-create", lambda: (_ for _ in ()).throw(OSError(PUBLIC.decode())))
        self.assertTrue(lines)
        self.assertTrue(all(line in fixture.DIAGNOSTICS for line in lines))
        self.assertFalse(any(PUBLIC.decode() in line for line in lines))
        before = list(lines)
        with self.assertRaises(c.Failure):
            diag.run(PUBLIC.decode(), lambda: None)
        self.assertEqual(lines, before)

    def test_capture_start_failure_keeps_stream_owner(self):
        class Partial(c.Capture):
            def start(self):
                super().start()
                raise RuntimeError("public start bait")
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        owner = None
        with self.assertRaises(RuntimeError):
            with owned:
                owner = owned.process("helper-process", lambda: subprocess.Popen(
                    [sys.executable, "-c", "import time; time.sleep(2)"], stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE), capture_factory=Partial)
                owner.capture("out")
        self.assertTrue(owner.captures["out"].error)
        self.assertTrue(all(value.consumed and value.confirmed for value in owner.streams.values()))
        self.assertFalse(owner.results["out"][1])

    def test_popen_failure_not_registered_as_acquired(self):
        lines = []
        owned = fixture.Resources(fixture.Diagnostics(sink=lines.append))
        def rejected():
            raise OSError("public popen bait")
        with self.assertRaises(OSError):
            owned.process("helper-process", rejected)
        self.assertEqual(owned.cleanup(), (None, True))
        self.assertEqual(lines, [])

    def test_successful_helper_resource_owned_despite_capture_failure(self):
        box, released = [], []
        def factory(*args, **kwargs):
            process = subprocess.Popen(*args, **kwargs)
            box.append(process)
            return process
        def bad_capture(*args, **kwargs):
            box[0].wait(timeout=2)  # deterministic public helper completed first
            raise RuntimeError("public capture bait")
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        commands = fixture.Commands(owned.diag, factory, bad_capture)
        with self.assertRaises(RuntimeError):
            with owned:
                commands.run([sys.executable, "-c", "pass"], owned_success=lambda:
                    owned.own("user", "public-resource", lambda value: released.append(value)))
        self.assertEqual(released, ["public-resource"])
        self.assertEqual(box[0].returncode, 0)
        self.assertFalse(all(ok for _, ok in commands.inventory.values()))

    def test_stream_close_consumed_before_error(self):
        class Stream:
            closed = False
            calls = 0
            def close(self):
                self.calls += 1
                self.closed = True
                raise OSError("public released stream bait")
        stream = Stream()
        owner = c.OwnedStream(stream)
        with self.assertRaises(OSError):
            owner.close()
        self.assertTrue(owner.consumed)
        self.assertFalse(owner.confirmed)
        with self.assertRaises(c.Failure):
            owner.close()
        self.assertEqual(stream.calls, 1)


@unittest.skipUnless(sys.platform == "linux", "Linux anonymous pipe/procfs required")
class Pipes(unittest.TestCase):
    def setUp(self):
        self.r, self.w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        self.writer = c.OwnedFD(self.w)
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
        if not self.writer.consumed:
            self.writer.close()
        for fd in (self.r, self.neighbor_r, self.neighbor_w):
            try:
                os.close(fd)
            except OSError:
                pass

    def sender(self, io=None, target=None, deadline=None):
        return c.OneShot(self.writer, target or self.target, PURPOSE, deadline or time.monotonic() + 2, io or self.io)

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
            c.OneShot(self.writer, target, purpose, time.monotonic() + 3).send(oracle, purpose)
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


@unittest.skipUnless(sys.platform == "linux", "Linux FD/directory identity regressions")
class SafetyRegressions(unittest.TestCase):
    def test_success_duplicate_and_guard_close_exactly_once(self):
        r, w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        owner = c.OwnedFD(w)
        target = c.Target(os.getpid(), os.getuid(), "public", (1, 2), c.pipe_identity(w), b"public")
        calls = []
        class IO(c.SystemIO):
            identity = staticmethod(lambda _: target)
            @staticmethod
            def close(fd):
                calls.append(True)
                c.close_checked(fd)
        owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
        owned.own("input-write", owner, lambda value: value.close())
        try:
            sender = c.OneShot(owner, target, PURPOSE, time.monotonic() + 2, IO())
            sender.send(PUBLIC, PURPOSE)
            with self.assertRaisesRegex(c.Failure, "c-reused"):
                sender.send(PUBLIC, PURPOSE)
            self.assertTrue(owner.consumed and owner.confirmed)
            self.assertEqual(owned.cleanup(), (None, True))
            self.assertEqual(calls, [True])
            self.assertEqual(os.read(r, 256), PUBLIC)
            self.assertEqual(os.read(r, 1), b"")
        finally:
            os.close(r)

    def test_constructor_close_reuse_not_retried(self):
        r, w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        owner = c.OwnedFD(w)
        target = c.Target(0, 0, "public", (0, 0), (0, 0, 0, 0, 0), b"public")
        replacement = []
        with tempfile.TemporaryFile() as sentinel:
            class IO(c.SystemIO):
                @staticmethod
                def close(fd):
                    os.close(fd)
                    os.dup2(sentinel.fileno(), fd)
                    replacement.append(c.OwnedFD(fd))
                    raise OSError("public close-after-release bait")
            owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
            owned.own("input-write", owner, lambda value: value.close())
            try:
                with self.assertRaisesRegex(c.Failure, "c-pipe"):
                    c.OneShot(owner, target, PURPOSE, time.monotonic() + 2, IO())
                self.assertEqual(owned.cleanup("c-pipe"), ("c-pipe", False))
                self.assertEqual(os.fstat(w).st_ino, os.fstat(sentinel.fileno()).st_ino)
            finally:
                os.close(r)
                for value in replacement:
                    value.close()

    def test_close_released_then_reused_sentinel_not_reclosed(self):
        r, w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        original = c.OwnedFD(w)
        replacement = []
        target = c.Target(os.getpid(), os.getuid(), "public", (1, 2), c.pipe_identity(w), b"public")
        with tempfile.TemporaryFile() as sentinel:
            sentinel.write(b"public-sentinel")
            sentinel.flush()
            class IO(c.SystemIO):
                identity = staticmethod(lambda _: target)
                @staticmethod
                def close(fd):
                    os.close(fd)
                    os.dup2(sentinel.fileno(), fd)
                    replacement.append(c.OwnedFD(fd))  # new, unrelated ownership
                    raise OSError("public synthetic close-after-release fault")
            owned = fixture.Resources(fixture.Diagnostics(sink=lambda _: None))
            continued = []
            owned.own("root", None, lambda _: continued.append(True))
            owned.own("input-write", original, lambda fd: fd.close())
            try:
                with self.assertRaisesRegex(c.Failure, "c-empty"):
                    c.OneShot(original, target, PURPOSE, time.monotonic() + 2, IO()).send(b"", PURPOSE)
                self.assertTrue(original.consumed)
                self.assertFalse(original.confirmed)
                self.assertEqual(owned.cleanup("c-empty"), ("c-empty", False))
                self.assertEqual(continued, [True])
                self.assertEqual(os.fstat(w).st_ino, os.fstat(sentinel.fileno()).st_ino)
                os.lseek(w, 0, os.SEEK_SET)
                self.assertEqual(os.read(w, 32), b"public-sentinel")
            finally:
                os.close(r)
                for fd in replacement:
                    fd.close()

    def test_constructor_rejection_consumes_shared_owner(self):
        r, w = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        owner = c.OwnedFD(w)
        target = c.Target(0, 0, "public", (0, 0), (0, 0, 0, 0, 0), b"public")
        try:
            with self.assertRaisesRegex(c.Failure, "c-pipe"):
                c.OneShot(owner, target, PURPOSE, time.monotonic() + 2)
            self.assertTrue(owner.consumed and owner.confirmed)
            owner.close()  # bookkeeping only, not an OS close retry
        finally:
            os.close(r)

    def test_missing_root_is_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            _, complete = fixture.snapshot_private([Path(name) / "missing"], os.getuid())
            self.assertFalse(complete)

    def test_no_visited_root_is_incomplete(self):
        self.assertEqual(fixture.snapshot_private([], os.getuid()), (b"", False))

    def test_root_enumeration_rejection_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            class Ops(fixture.ScanOps):
                @staticmethod
                def scandir(fd):
                    raise PermissionError("public enumeration refusal")
            self.assertFalse(fixture.snapshot_private([Path(name)], os.getuid(), ops=Ops())[1])

    def test_file_root_is_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name) / "public"
            path.write_bytes(b"public")
            self.assertFalse(fixture.snapshot_private([path], os.getuid())[1])

    def test_symlink_root_is_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name)
            (path / "real").mkdir()
            (path / "link").symlink_to(path / "real", target_is_directory=True)
            self.assertFalse(fixture.snapshot_private([path / "link"], os.getuid())[1])

    def test_real_empty_root_is_complete(self):
        with tempfile.TemporaryDirectory() as name:
            self.assertEqual(fixture.snapshot_private([Path(name)], os.getuid()), (b"", True))

    def test_root_rebound_during_enumeration_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name) / "root"
            path.mkdir()
            class Ops(fixture.ScanOps):
                @staticmethod
                def scandir(fd):
                    iterator = os.scandir(fd)
                    path.rename(Path(name) / "old")
                    path.mkdir()
                    return iterator
            self.assertFalse(fixture.snapshot_private([path], os.getuid(), ops=Ops())[1])

    def test_subtree_enumeration_error_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name)
            (path / "child").mkdir()
            inode = (path / "child").stat().st_ino
            class Ops(fixture.ScanOps):
                @staticmethod
                def scandir(fd):
                    if os.fstat(fd).st_ino == inode:
                        raise OSError("public enumeration bait")
                    return os.scandir(fd)
            self.assertFalse(fixture.snapshot_private([path], os.getuid(), ops=Ops())[1])

    def test_file_disappears_after_stat_incomplete(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name)
            (path / "public").write_bytes(b"public")
            class Ops(fixture.ScanOps):
                @staticmethod
                def open(value, flags, **kwargs):
                    if value == "public":
                        os.unlink(value, dir_fd=kwargs["dir_fd"])
                    return os.open(value, flags, **kwargs)
            self.assertFalse(fixture.snapshot_private([path], os.getuid(), ops=Ops())[1])


if __name__ == "__main__":
    unittest.main()
