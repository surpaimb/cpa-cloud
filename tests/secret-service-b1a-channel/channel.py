"""Independently authored B1a-C test-only, one-use anonymous-pipe sender.

Not a product API. See CONTRACT.md and PROVENANCE.md. No global hooks.
"""
import base64
import errno
import hashlib
import hmac
import os
import re
import select
import stat
import threading
import time
from dataclasses import dataclass
from pathlib import Path

LIMIT = 128
SIZE = 68
CAPTURE_LIMIT = 65536
LABELS = frozenset({
    "c-empty", "c-over-limit", "c-encoding", "c-length", "c-reused",
    "c-deadline", "c-identity", "c-pipe", "c-write", "c-short-write",
    "c-close", "c-receiver-read", "c-receiver-eof", "c-receipt",
    "c-capture", "c-content", "c-setup", "c-isolation", "c-bus",
    "c-unit", "c-wait", "c-scan", "c-cleanup", "c-public-state",
    "c-protocol", "c-internal",
})


class Failure(Exception):
    def __init__(self, label):
        super().__init__(label if label in LABELS else "c-internal")


def require(condition, label):
    if not condition:
        raise Failure(label)


def validate(value):
    require(type(value) is bytes, "c-encoding")
    require(bool(value), "c-empty")
    require(len(value) <= LIMIT, "c-over-limit")
    require(not any(c in value for c in (b"\0", b"\r", b"\n")), "c-encoding")
    require(len(value) == SIZE, "c-length")
    require(re.fullmatch(rb"B1AC[0-9a-f]{64}", value) is not None, "c-encoding")


def pipe_identity(fd):
    s = os.fstat(fd)
    require(stat.S_ISFIFO(s.st_mode) and stat.S_IMODE(s.st_mode) == 0o600, "c-pipe")
    return s.st_dev, s.st_ino, s.st_uid, s.st_gid, s.st_mode


def close_checked(fd):
    try:
        os.close(fd)
        try:
            os.fstat(fd)
        except OSError as e:
            require(e.errno == errno.EBADF, "c-close")
        else:
            raise Failure("c-close")
    except Failure:
        raise
    except OSError:
        raise Failure("c-close") from None


@dataclass(frozen=True)
class Target:
    pid: int
    uid: int
    start: str
    executable: tuple
    pipe: tuple
    cgroup: bytes


def observe(pid):
    """Read only this owned target, never enumerate unrelated processes."""
    try:
        proc = Path("/proc") / str(pid)
        status = (proc / "status").read_bytes()
        uids = re.search(rb"^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)$", status, re.M)
        require(uids is not None and len(set(uids.groups())) == 1, "c-identity")
        start = (proc / "stat").read_text().rsplit(")", 1)[1].split()[19]
        exe = (proc / "exe").stat()
        fd = (proc / "fd" / "0").stat()
        flags = (proc / "fdinfo" / "0").read_text()
        match = re.search(r"^flags:\s+([0-7]+)$", flags, re.M)
        require(match is not None and int(match[1], 8) & os.O_ACCMODE == os.O_RDONLY, "c-pipe")
        require(stat.S_ISFIFO(fd.st_mode) and stat.S_IMODE(fd.st_mode) == 0o600, "c-pipe")
        return Target(pid, int(uids[1]), start, (exe.st_dev, exe.st_ino),
                      (fd.st_dev, fd.st_ino, fd.st_uid, fd.st_gid, fd.st_mode),
                      (proc / "cgroup").read_bytes())
    except Failure:
        raise
    except (OSError, ValueError, IndexError):
        raise Failure("c-identity") from None


class SystemIO:
    """Narrow syscall adapter; only public tests replace an instance of it."""
    write = staticmethod(os.write)
    close = staticmethod(close_checked)
    identity = staticmethod(observe)
    clock = staticmethod(time.monotonic)


class OneShot:
    def __init__(self, fd, target, purpose, deadline, io=None):
        self.fd, self.target, self.purpose = fd, target, purpose
        self.deadline, self.io = deadline, io or SystemIO()
        self.used = False
        self.closed = False
        require(re.fullmatch(r"[0-9a-f]{32}", purpose) is not None, "c-protocol")
        require(pipe_identity(fd) == target.pipe and not os.get_inheritable(fd), "c-pipe")

    def send(self, value, purpose):
        require(not self.used, "c-reused")
        self.used = True  # A rejected attempt is consumed too; never retry.
        first = None
        try:
            validate(value)
            require(purpose == self.purpose, "c-identity")
            remaining = self.deadline - self.io.clock()
            require(0 < remaining <= 5, "c-deadline")
            require(self.io.identity(self.target.pid) == self.target, "c-identity")
            require(pipe_identity(self.fd) == self.target.pipe, "c-pipe")
            # Payload is smaller than POSIX PIPE_BUF. No partial-write retry,
            # newline insertion, shell, file, environment, metadata or fallback.
            try:
                count = self.io.write(self.fd, value)
            except OSError:
                raise Failure("c-write") from None
            require(count == len(value), "c-short-write")
            require(self.io.clock() < self.deadline, "c-deadline")
            require(self.io.identity(self.target.pid) == self.target, "c-identity")
        except Failure as e:
            first = e
        finally:
            try:
                self.io.close(self.fd)
                self.closed = True
            except Exception:
                if first is None:
                    first = Failure("c-close")
        if first:
            raise first


def receipt(value, purpose):
    validate(value)
    # This test-only receipt travels solely in the captured PRIVATE binary
    # protocol; never print it, store it in metadata or report it on failure.
    return hmac.new(value, b"B1a-C/EOF/" + purpose.encode("ascii"), hashlib.sha256).digest()


def check_receipt(raw, value, purpose):
    require(len(raw) == 36 and raw[:4] == b"COK1", "c-receipt")
    require(hmac.compare_digest(raw[4:], receipt(value, purpose)), "c-receipt")


def read_line(fd, deadline, limit=4096):
    out = bytearray()
    while len(out) < limit:
        require(time.monotonic() < deadline, "c-deadline")
        ready, _, _ = select.select([fd], [], [], min(.1, max(0, deadline - time.monotonic())))
        if ready:
            chunk = os.read(fd, 1)
            require(bool(chunk), "c-protocol")
            out += chunk
            if chunk == b"\n":
                return bytes(out)
    raise Failure("c-protocol")


class Capture:
    def __init__(self, stream, limit=CAPTURE_LIMIT):
        self.stream, self.limit = stream, limit
        self.data, self.eof, self.overflow, self.error = bytearray(), False, False, False
        self.thread = threading.Thread(target=self._read, daemon=True)
        self.thread.start()

    def _read(self):
        try:
            while True:
                chunk = os.read(self.stream.fileno(), 4096)
                if not chunk:
                    self.eof = True
                    break
                remaining = self.limit - len(self.data)
                self.data += chunk[:remaining]
                self.overflow |= len(chunk) > remaining
        except Exception:
            self.error = True

    def finish(self):
        self.thread.join(3)
        complete = self.eof and not self.thread.is_alive() and not self.overflow and not self.error
        # Never close/reuse an FD under a still-blocked reader. That is an
        # incomplete capture, not successful closure; acceptance must fail.
        if not self.thread.is_alive():
            try:
                self.stream.close()
            except Exception:
                complete = False
        return bytes(self.data), complete


def scan(value, inventory, expected):
    """Content-clear and capture-complete are deliberately separate results."""
    variants = (value, value.hex().encode(), base64.b64encode(value))
    clear = not any(v in data for data, _ in inventory.values() for v in variants)
    complete = set(inventory) == set(expected) and all(ok for _, ok in inventory.values())
    return clear, complete
