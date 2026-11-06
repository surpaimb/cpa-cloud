"""Synthetic stdin receiver only; not GNOME or a password provisioning API."""
import json
import os
import select
import sys
import time

from channel import Failure, SIZE, close_checked, pipe_identity, receipt, require, validate


def receive(purpose):
    identity = pipe_identity(0)
    hello = {"pid": os.getpid(), "uid": os.getuid(), "pipe": list(identity)}
    os.write(1, json.dumps(hello, sort_keys=True).encode("ascii") + b"\n")
    value = bytearray()
    deadline = time.monotonic() + 20
    eof = False
    while time.monotonic() < deadline:
        ready, _, _ = select.select([0], [], [], .1)
        if not ready:
            continue
        try:
            chunk = os.read(0, SIZE + 1)
        except OSError:
            raise Failure("c-receiver-read") from None
        if not chunk:
            eof = True
            break
        value += chunk
        require(len(value) <= SIZE, "c-over-limit")
    require(eof, "c-receiver-eof")
    validate(bytes(value))
    close_checked(0)  # The private receipt attests byte validation AND EOF/close.
    proof = b"COK1" + receipt(bytes(value), purpose)
    require(os.write(1, proof) == len(proof), "c-receipt")
    close_checked(1)
    close_checked(2)


if __name__ == "__main__":
    try:
        require(len(sys.argv) == 2, "c-protocol")
        receive(sys.argv[1])
    except Failure as e:
        try:
            os.write(2, ("FAIL: " + e.args[0] + "\n").encode("ascii"))
        except OSError:
            pass
        sys.exit(1)
    except Exception:
        try:
            os.write(2, b"FAIL: c-internal\n")
        except OSError:
            pass
        sys.exit(1)
