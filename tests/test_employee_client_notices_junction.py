"""Synthetic path-boundary checks for the employee notice collector.

Provenance: independently authored CPA Cloud fixtures using Python's standard
library. No package script, registry access, or product binary is involved.
"""

from __future__ import annotations

import base64
import importlib.util
import hashlib
import io
import json
import os
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "employee-client-notices.py"
SPEC = importlib.util.spec_from_file_location("employee_client_notices", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
notices = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(notices)

MANIFEST = json.dumps({"name": "synthetic-notice", "version": "1.0.0",
                       "license": "MIT"}, separators=(",", ":")).encode()
LICENSE = b"Synthetic full license text for path-boundary tests.\n"
FALLBACKS = SCRIPT.parents[1] / "desktop" / "employee-config-switcher" / "third_party" / "notice-fallbacks"


def committed_fixture(raw: bytes, length: int, digest: str) -> bytes:
    """Model Git blob bytes from only pure LF or a complete CRLF checkout."""
    if raw.startswith(b"\xef\xbb\xbf"):
        raise AssertionError("committed fixture has a BOM")
    normalized = raw.replace(b"\r\n", b"\n")
    if b"\r" in normalized or (b"\r\n" in raw and
                             raw != normalized.replace(b"\n", b"\r\n")):
        raise AssertionError("committed fixture has mixed or bare CR endings")
    if len(normalized) != length or hashlib.sha256(normalized).hexdigest() != digest:
        raise AssertionError("committed fixture differs from pinned Git blob")
    return normalized


BLOCK2_DECLARATION_BYTES = committed_fixture(
    (FALLBACKS / "block2-0.6.2-UPSTREAM-LICENSE.md").read_bytes(), 1339,
    "7f976f7e9cb2d87df7230606feb932c3f21ac0e664045a775b600046ff850c54",
)
MIT_TEMPLATE_BYTES = committed_fixture(
    (FALLBACKS / "block2-0.6.2-LICENSE-MIT.txt").read_bytes(), 1078,
    "b05785f9f18e6716bab63424b11454513b9943a222595b70411009202fc592b5",
)
JNI_MIT_BYTES = committed_fixture(
    (FALLBACKS / "jni-sys-macros-0.4.1-LICENSE-MIT.txt").read_bytes(), 1071,
    "1d85bd754b04ceec93e98e890edd1fa3c6a22e81bcb32135806beeccefa51cd1",
)
JNI_APACHE_BYTES = committed_fixture(
    (FALLBACKS / "jni-sys-macros-0.4.1-LICENSE-APACHE.txt").read_bytes(), 11358,
    "c6596eb7be8581c18be736c846fb9173b69eccf6ef94c5135893ec56bd92ba08",
)


MIT_DLOPEN2_BYTES = committed_fixture(
    (FALLBACKS / "dlopen2-rs-LICENSE.txt").read_bytes(), 1184,
    "39fa265207450e77c62e90c5594a06c085b655d8374c7ced4bf7894b6bd95dd2",
)


def block2_archive(vcs: str, path: str) -> bytes:
    content = json.dumps({"git": {"sha1": vcs}, "path_in_vcs": path}).encode()
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        item = tarfile.TarInfo("block2-0.6.2/.cargo_vcs_info.json")
        item.size = len(content)
        tar.addfile(item, io.BytesIO(content))
    return buffer.getvalue()


def archive(files: dict[str, bytes], directories: set[str] | None = None) -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        for name in ["package", *(f"package/{d}" for d in sorted(directories or set()))]:
            entry = tarfile.TarInfo(name)
            entry.type = tarfile.DIRTYPE
            tar.addfile(entry)
        for name, data in sorted(files.items()):
            entry = tarfile.TarInfo(f"package/{name}")
            entry.size = len(data)
            tar.addfile(entry, io.BytesIO(data))
    return buffer.getvalue()


def junction(alias: Path, target: Path) -> None:
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(alias), str(target)],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0 or not alias.is_junction():
        raise AssertionError("native Windows junction fixture unavailable")
    info = alias.lstat()
    if not (getattr(info, "st_file_attributes", 0)
            & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)):
        raise AssertionError("junction fixture lacks reparse attribute")


class Block2FallbackTests(unittest.TestCase):
    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(prefix="cpa-block2-notice-")).resolve(strict=True)
        self.case_number = 0

    def test_committed_fixture_accepts_only_pinned_uniform_line_endings(self) -> None:
        sources = (
            (BLOCK2_DECLARATION_BYTES, 1339,
             "7f976f7e9cb2d87df7230606feb932c3f21ac0e664045a775b600046ff850c54"),
            (MIT_TEMPLATE_BYTES, 1078,
             "b05785f9f18e6716bab63424b11454513b9943a222595b70411009202fc592b5"),
        )
        for content, length, digest in sources:
            with self.subTest(length=length):
                self.assertEqual(committed_fixture(content, length, digest), content)
                self.assertEqual(committed_fixture(
                    content.replace(b"\n", b"\r\n"), length, digest), content)
                for bad in (b"\xef\xbb\xbf" + content,
                            b"\r" + content,
                            content.replace(b"\n", b"\r\n", 1),
                            content + b"changed"):
                    with self.assertRaises(AssertionError):
                        committed_fixture(bad, length, digest)

    def cargo_case(self, *, label: str = "MIT",
                   repository: str = "https://github.com/madsmtm/objc2",
                   vcs: str = "b4167b582b2f75f9a1be75495c41b765344fd03c",
                   vcs_path: str = "crates/block2", checksum_valid: bool = True,
                   declaration: bytes = BLOCK2_DECLARATION_BYTES,
                   license_text: bytes = MIT_TEMPLATE_BYTES,
                   archive_texts: dict[str, bytes] | None = None) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / "block2-0.6.2"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        (source / "Cargo.toml").write_text("[package]\nname='block2'\nversion='0.6.2'\n")
        local = []
        for name, version in sorted(notices.FIRST_PARTY_CARGO):
            local_manifest = workspace / name / "Cargo.toml"
            local_manifest.parent.mkdir()
            local_manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": name, "version": version, "source": None,
                          "manifest_path": str(local_manifest)})
        data = block2_archive(vcs, vcs_path)
        (cache / "block2-0.6.2.crate").write_bytes(data)
        metadata = {"packages": local + [{
            "name": "block2", "version": "0.6.2", "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"),
            "license": label, "repository": repository,
        }]}
        locked = notices.sha(data) if checksum_valid else "0" * 64

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata), stderr="")

        committed = {
            notices.BLOCK2_DECLARATION: declaration,
            notices.VENDORED_CARGO[("block2", "0.6.2", "LICENSE-MIT.txt")]:
                license_text,
        }
        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "archive_files",
                          return_value=(archive_texts or {}, None)), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed.get(name, b"")):
            return notices.cargo_packages(workspace, {("block2", "0.6.2"): locked})[0]

    def test_block2_fallback_requires_two_distinct_sources(self) -> None:
        self.assertFalse(notices.has_authorization_text({
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
        }))
        package = self.cargo_case()
        self.assertEqual(package["notice_status"], "vendored_external")
        self.assertEqual(package["license"], "MIT")
        self.assertEqual(package["texts"], {
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
            "EXTERNAL_ORIGIN/LICENSE-MIT.txt": MIT_TEMPLATE_BYTES,
        })
        self.assertEqual([entry["role"] for entry in package["external_sources"]],
                         ["upstream-license-declaration", "full-license-text"])
        self.assertEqual([entry["path"] for entry in package["external_sources"]],
                         ["EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md",
                          "EXTERNAL_ORIGIN/LICENSE-MIT.txt"])
        self.assertIn(b"<year> <copyright holders>", MIT_TEMPLATE_BYTES)

    def test_block2_missing_or_changed_source_is_refused(self) -> None:
        for field in ("declaration", "license_text"):
            for bad in (b"", b"changed"):
                with self.subTest(field=field, bad=bad):
                    with self.assertRaisesRegex(notices.NoticeError,
                                                "vendored committed notice differs"):
                        self.cargo_case(**{field: bad})

    def test_block2_identity_and_archive_checksum_are_bound(self) -> None:
        cases = (
            ({"label": "Apache-2.0"}, "external license identity differs"),
            ({"repository": "https://example.invalid/objc2"},
             "external license identity differs"),
            ({"vcs": "0" * 40}, "archive VCS origin differs"),
            ({"vcs_path": "crates/other"}, "archive VCS origin differs"),
            ({"checksum_valid": False}, "archive checksum mismatch"),
        )
        for change, reason in cases:
            with self.subTest(change=change):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    self.cargo_case(**change)

    def test_existing_full_archive_text_does_not_need_fallback(self) -> None:
        package = self.cargo_case(archive_texts={"LICENSE": MIT_TEMPLATE_BYTES},
                                  declaration=b"", license_text=b"")
        self.assertEqual(package["notice_status"], "archive_text")
        self.assertEqual(package["texts"], {"LICENSE": MIT_TEMPLATE_BYTES})

    def test_full_synthetic_collect_verify_and_source_readback(self) -> None:
        package = self.cargo_case()
        workspace = self.scratch / "distribution-workspace"
        workspace.mkdir()
        output = self.scratch / "notice-output"
        source_names = [
            "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
        ]
        committed = {name: (name + "\n").encode() for name in source_names}
        committed[notices.BLOCK2_DECLARATION] = BLOCK2_DECLARATION_BYTES
        committed[notices.VENDORED_CARGO[("block2", "0.6.2", "LICENSE-MIT.txt")]] = (
            MIT_TEMPLATE_BYTES
        )
        cargo = {("block2", "0.6.2"): package["integrity"].removeprefix("sha256-")}

        def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[0], "git")
            if argv[-1] == "--show-toplevel":
                value = str(workspace)
            elif argv[-1] == "HEAD":
                value = "a" * 40
            elif "status" in argv:
                value = ""
            else:
                self.fail("unexpected synthetic Git operation")
            return SimpleNamespace(returncode=0, stdout=value, stderr="")

        with patch.object(notices.subprocess, "run", side_effect=fake_git), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed[name]), \
             patch.object(notices, "cargo_lock", return_value=cargo), \
             patch.object(notices, "pnpm_lock", return_value={}), \
             patch.object(notices, "pnpm_graph", return_value=([], {})), \
             patch.object(notices, "bundle_packages", return_value=set()), \
             patch.object(notices, "pnpm_packages", return_value=([], [])), \
             patch.object(notices, "cargo_packages", return_value=[package]):
            notices.collect(workspace, output, workspace / "synthetic-bundle")
            manifest_data = (output / "manifest.json").read_bytes()
            manifest = json.loads(manifest_data)
            prefix = "cargo/block2@0.6.2/EXTERNAL_ORIGIN/"
            self.assertEqual((output / (prefix + "UPSTREAM_DECLARATION.md")).read_bytes(),
                             BLOCK2_DECLARATION_BYTES)
            self.assertEqual((output / (prefix + "LICENSE-MIT.txt")).read_bytes(),
                             MIT_TEMPLATE_BYTES)
            self.assertEqual(manifest["source_files"][notices.BLOCK2_DECLARATION],
                             notices.sha(BLOCK2_DECLARATION_BYTES))
            notices.verify(output, workspace, notices.sha(manifest_data),
                           workspace / "synthetic-bundle")
            declaration_output = output / (prefix + "UPSTREAM_DECLARATION.md")
            declaration_output.write_bytes(b"tampered")
            with self.assertRaisesRegex(notices.NoticeError, "notice hash/size mismatch"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")
            declaration_output.write_bytes(BLOCK2_DECLARATION_BYTES)
            committed[notices.BLOCK2_DECLARATION] = b"changed"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "committed source file changed"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")



# Test-only offline fixture: complete official crate bytes from
# https://static.crates.io/crates/cesu8/cesu8-1.1.0.crate
# SHA-256 6d43a04d8753f35258c91f8ec639f792891f748a1edbd759cf1dcea3382ad83c.
# The crate retains its own third-party license; these bytes are never product code.
CESU8_ARCHIVE = base64.b64decode(
    b'H4sICAAAAAACA2Nlc3U4LTEuMS4wLmNyYXRlAO192XobObJmXfMpYNX5bNJFcdNimV6mKYq02dbWImWVj1qfnCQhMsvJTFYuolRV'
    b'fodzN08wF+dirucBzrvM9TzCxAJkIsmkFtvtr+u06M8SyQQCQCAQ8UcEAA1kEG2tVkvVUqVcGtmhPXI9X/7wTV8VeG1WKvi7+mwj'
    b'9ZtftfUfqrX19cqz9Y31Gn5frT1b2xSVH77DKwpCy4cmv8UgK5V4UD/8SV5lGP1IhrlyE357JccbfMr9Wfr+8Pr618Bc/6FvXdpB'
    b'6XrifNM2aP1vri9b/9UKrPlqbWOtWll/trlJ63+t8qzysP6/w8ux3FFkjWRd+MCJXBANPXgrf41sXw5z+F09typcezQOnetcX16A'
    b'dTgPBr49pQd/5ISY2lNhu8BGxxFPWIRWB6hNXlZKtSdidTUKpC8eP4ai8mrq+aE4bPTevvq3twd7rTJqHMsp9223/m/4dS5N2yQn'
    b'+pHtDJlQ6vtQBmHG133pDsYZ3w+9Qc66CKV/HkQDkP8AG0uVWF31XOdajxorrEZTx7OG8wUH3qX0YdxBTrqXdWho5Hh9y8F3QgRy'
    b'EPnA2PagZ0nn3brzc3my86bxy9j7ufvz4KT9fPzrh+G4vfGutzc69E8q//5u7603e2Zd9Yfh39be2r+tH8vZpBls9XbG9nptt3MZ'
    b'hv3RT9W14+7sOny3/+vx5r9v29cfhpOLcq+8bU1/rjy/rDwbbkfeWhhMhrP9cN+v2dZv0+rxht0/atcOt663D36RV9WTVrjxtwv7'
    b'r1cb3kVjenhVHq47H961t1496P5/sZep/5sHhx+OOm/e9laPjru9UngVfps2btb/gPhqNcJ/oP+rGxs10v9Q/kH/f4dXd4wKGbRY'
    b'YHuuAO0uXM9ddazZNXxVz+V6YymOgEfi0Pd+kYNQ2IEYRpaz6tgD6QZyKCJ3COq9MbUGULRWqgjLHYq9Ti8HGnYSlHK53K7njqCI'
    b'amQJ0YE3vfZR4YpapbpeFPNlcjvyUjreFIiI/Mi+lC4YHRFCqQvbkaJx3Ht7cNRFoS1Am7vp3mEx1UP1pCjeqzFDl3MvdzvN1n63'
    b'tdo4bDTftoTn58ZhOK2Xy7PZrGRRzZLnj8pq1EFZV4Dar6E4tYCDVgVETBG+xOeKHPTfDbzIH8yRg1KvizkrFNfwUHjTEHpWEg2w'
    b'qDi6QA91qvgF1se/tt2RAAs2zrleCHTExAKT5YVg95CZthwWxcQb2hf0DrowtIPQt/tRCGyRVwM5DXPWYOD5QyQUghkde9DxeNYW'
    b'J8kdONEQOgPs+ASQIRAz3w5DmIj+NVS2/SE88UNbQnWsfOE5jjcj4vHD66Sy5UtNEvqHQkOjysE4bWDBzDVEgkcYUCnNYeooCNNT'
    b'0Zt5YiwtnGjmVjgGTiJ9bFJ4F8S795Yz8m13iIiF+1BCIQukqoTlLzyQFwGVA39Q9sPy5ah8qaqVxtg6VE4eTeQE5GLwqTQuipkN'
    b'g8RJwvWhGU2TaLQBtbEVQFbGVCQSajAMxwZMCXDkUMZ2EcSM5QTGK2iVZnWQHgpoEPpAi8IOYg7o0QNRMbx2rYk9EAC6LGA41kH8'
    b'5kcT6YZWSIrAtyZy5vmfSkyzGU9FvlmAFVqprMIyrYi/Ro5tuaIrZ5Y/LAmSWCoXUDUfhu5fyqGiciRN1tBsAjZE4eY1Qd9wr3CQ'
    b'yALkK9WFMeN7LwqVVA+op0WeZ+AXiuIQV8ilDRLFMgAso8oJZweeO7SxIk/4RIaKa9XSXPcCZJvq18AbQllcDL4MLbUYrT6gP6oL'
    b'r3lZLbIoOEAQ6ZjNusO4X6muQeMDx7In0lfsqpHwwMjtETRJYgwkA+8inFHfsT+84DWxiR34copcd4EZL1CbJMWIOPMFv5/5IFpm'
    b'T7gdy4lbgPnsXFBZnKV049AhS1cEng+jQYiLWMDKcr2ZI4cjFCZDb2EJXQEAtSFrMy8CWA9ay5pC1we2hfMIc4DiC/3WlbRTopiz'
    b'huoR1gmUVZOk7EvAIwZ6Uxgw4viJ5X+CYlagSaHWZJVj8HCOebj4+pK11w0s4s6s80zBspJa3VgRCKtvKuUI7RHoWekOPT/AeTUY'
    b'OMHJUGwCDSJ9sHBDWIbeZI7xahHEQ5kCx2AxQF0bl4jSyLQgAmSH6mHvbacrugft3knjqCXg/eHRwfvOTmtHbH+Ahy1lQMXHj40u'
    b'PH7yRDT2d6hmY/+DaP18eNTqdgUU6Owd7nagGtA5auz3Oq1uUXT2m7vHO539N0WxfdwT+wc9qrnbAbMGRXsHRWpisao4aIu91lHz'
    b'LXxsbHd2O70Pqs0d0e709rHNNjTaAJfxqNdpHu82jsTh8dHhQRd6DAPZ6XSbu43OXmsHhXWfKu8fiNb71n5PdN82dnfNwW23oE+N'
    b'7d0WE4WB7XSOWs1ekep19tUnHA+wBrq0WxTdw1azg29aP7eg/42jD0VgAyvFAzDwfzuGglBA7DT2Gm9gSPlFbpicoJrA/ObxUWsP'
    b'ewks6B5vd3ud3nGvJd4cHOwQn7uto/cAIbovxO4Bcr5NFY+7rSK01GtgJ5AMcAmK4NiOux3iV2e/1zo6Oj7sdQ72C+LtwQkwQ/W3'
    b'AdV3iLkH+zR84M3B0QfsAvKF+F8UJ29b8P2RZieMsnfUQLZ0e0edZs8sCu32Do56xph5BlpvdjtvWvvNFpY4QGonnW6rAFPWgV6+'
    b'AdLU/EkD2j4mFkAZPUD+aIhrkeZTdNqisfO+g0PgCgLEoNsxxIZY2XyrpqK0aCsTi32TrdyTkyaWQlsJuOLyeiZJsxsIAqyx5zlE'
    b'fShDQEeoKIC6B5ZL+j6s7wfD+WA4Hwzng+F8MJwPhvPPZzif0rK0oisbzBQaFTSRaEdlOChPP4El9YZTKxyXYK2gjkbdHpBic+y+'
    b'jzV8WGu2ixZgMoXK4HhClKDjuh7YvDCaFkHDUcxBihMwqd4MdYzKIoA7zLaslLbPzmJQZ/fN4S5Khg5grRVRASJxgYEUR45A50BL'
    b'0p+iAWLDx066J7WWtC4uML5Bymc08qEOqJSVAdgX+BqaQz94JY47KDWFoRGom7LLeRDWcqO7WysAZR/05LXwoKxPTPBcUI9kG+2w'
    b'JE5QNfalY0NUKza57PeLEWYVXGXZEurcMFZjQ6/4ZnINEiQh6z5zfkD7cfjAp/QNM40MBSrY29qFqtAyjeUijrclrQ5xXtEuoBFL'
    b'upKnyJRAY6C5pUXBR0jiR24IFluJi01BGzkJpHMpg0IsgAq5TWzX/q00oJaLSfALG5xMHZlYRO8CmjxqN6vPNypl/l0VO632bqPX'
    b'IgE8sqGjb6Qz9KVN/Xvpw++RdJ4//8toYtlOCTr5miNvYHaCGDyQDDIjE8xEcS8p5KQvhwidViJXScmKQjNQPC9JvMAsOdd1MY36'
    b'UASkb4JoCG2SAOvkWy6ExhIcwxzY3X2/VwIEOZQENKkfKnSFy4GDVYgBgUuOczlZhJwDgpxrCDnXxLFr0zIJaSY7jmO7HpCx2CQe'
    b'+33LtVabY2syteyRm8Kk83hUx2KRBQoGkt2CDouetCbJV0sbXWgwqaNipTQkiJLyg8PYMOPYUZaB+yNkHOqWC1/SyhyMcTtDEdEC'
    b'SCxVxIgxikYfdRSJDWHORViIsMYKAo+BVG4Rb7Hg53HKV7qq1kqBGhtK0DM0mxr0dOdwB7IQ1tSAVRAHPrE3JixxbBAsbgsbUbwH'
    b'8hEGrLHXKqSLvyUOlEeIMhUAMEvCikVYkn1Hh7phZGX0gyRMKIWGA14ppBTinjKwg9ZYwBXjqP3ZmOBUUpaDhiCDESiMYMz4bOgB'
    b'M6llihcr9R7j9BzDfY3nDbl5ek8PIq4o2Je4uxNh1mR3ItOP0N5bduegE4avpTvHGFR+n/5p/yAloOnKOONsfSYgz75tOUHi5KVE'
    b'VfkWZtTaHP6+tIlMqIB6oA1BvNyL6WVuEL1pwRfJSKao2qHJAcQV1CNA/eQPZPoC2gkwKt7gDnSXuAP38gha2Q7BCnkAKwAEO4Ca'
    b'AZ4pvP5Bg1CEbu8ALRcz/ALDGZhHv9zqATV8owtQTGN/1psZ+B9w7P7Bfme/fQStEZgmJyB2AFiBxU4AAdkOdAoSbNjjOE8MIHl3'
    b'pwVfxv4B1dQ+AjkYxRjfa0SbDZmhBvOoifgbB5fgZ4LKJiAuxoC4fXQATQCzNY49IGJQd7/FlHAy0nOG4Bs+E2JmgM/92mk1doFm'
    b'd7EKmePGkBcpaPoEtegMFi5IWg+ETkDVQjDJIqHc7u6sIvKBJdp3pIaRQQlwsIxNt84nYv4StQoiS1R3TkBNa+S06jO2xWrGdzop'
    b'O4eQc2yRn8TIWadm60uAwnMECusa1qSWH6oqnYiCmketHfCfSr2fe0zqASo8QIUHqPAAFR6gwgNU+JeHCljs1Re+vrll/hMYQ0NT'
    b'P5jCu5nCnk6jLdgLZQwwVJTMfByMAtU1xn3afZlCrpYxRB+7AmE8iM/B3OB+7djaKGXeTedw/nsqueU6Tm01vEW9adXGq/Ke6k2p'
    b'Nq2o7qfeWLWpzMw91BsTZdCu1RsQWPCDDpVIaHFotzsMvTBISbF2i/PhGjmA0cbwp4Cx5kj+52ytFE0HTDaYYtBLkwlW1ccSII89'
    b'xpAtelU2baBL4xy1WJcl27c42d6zBuNrWAY9ORi7nuONaI/izX7LQ8r9vin3L8a+9wS+ah+m0Jo4q1eZoFdp9DgnfRvizcK5y9PB'
    b'qQTwXPKXF+OdEsDzyd8lVTO03WLyd6nCm0sAG7ou0XNKEbxv7R4comZD6il495AU/pdLCr/1JKQcKYUqaXcIfsJ9U6A6iyotq3J2'
    b'Y13UDigrrAJTVqDWa7AsW7VVFPtWCBrbIkvzX/+ZWaxaLYr3SDQUexaU+t/ZpSAfDAEoUAY9z/dlrND0QNRmk2ABKmejR6ydoEXS'
    b'T4ypNGq9UFnfaeRPcTM9aRLDGlxImaBwqhoj8QVjcAO+UyA0C+PhXiBp+WlQd0egRrKcIBxWAEpNwPoHtWDoHwITR603jaMdxmKG'
    b'iCkFoOEb1ryzBmO3kLXYEs20dFcKAzWtYhL9o98RYsvSMmqwpMPUVydvG73uAa52GGX3eLenMZbWIIn24NUSK5BsLJfGccma1pUZ'
    b'EOL67xwcd1WlBN3FuM5QJAu4TuE4UNfAkL0GaYy51U/LGMLXfcgjhwDUpLlsze/r2ZCqWmNPtI2OY+yttTEVrTBOxx0wtjpJTod0'
    b'IFW/a7kgnj3r2vFAV7zxvJEj/wRYC1/5auFLw45fgW2Wghsj5JavFe6Hu1LEvzTieFO88eZw472DjUtCjfm1wt03Dsa1FiOBN8T/'
    b'4lrzOwgftg6ayPFh5+ADSExAIiQBymhhfpETgB/ewLQu+rsllqWiLMtfLQw7ti4tpXBe/oJfyL8MLNdzKeyUbNFacN0zCT9DwjWx'
    b'5/1mO45lGKsbYgCZhFQUtg3mse95nwxb983jCIZdIzL3sm232LWvCCEsGIt8UCgmzviNUYQltuxLowf/oA7daL/uar7uFzlQ1mA+'
    b'mJjvFqjiTYGFLNOQRt9LAwvZpoEq3xpYWGIaUh6D2lmeZRuWjXSpsbjFUFDllLG4u6FYsJr3MRRced4fuKOhoMrKWNzTUBjB3T9x'
    b'NMHYUJOsYpW8ZcXDe2xT6S5aexQHBp9CqfRissdYH1ZPbgso6uP56nA6XkPjwtL2LU6bJft7S6KL+5iTvoDJwb7gCS/QtS4lt1CH'
    b'w4NArvavV/G36FsBEIB+yitrwqkyA46C5pEWbTVWqRTaTa32tKuMFUQoEDau0kA95MYEU0m+xNP5QS5OvKjj/zQi5ksQORRqj8/f'
    b'cKQdCeBuc6hKao0tgR4WBD6AL2N7yvEKHjsqqTZ8xEFMHVlU+8KvEj9oZTEGo9w4soMrWI6jDwGbt4naK17UCjc+ax+qPORQupyO'
    b'15bL9jXf6EIA3eDq2HMQRgCfjRaxwQ7HOWgDOs5GUFS2AZgxINdAXk0h+gTs5BgUH6+/Cum4wRAtqr4wYC6zCjlRug/BS24coP7h'
    b'aQB1sDBl2RnogBmAxNkARJr31Mf3KihRhZpjiIERbVdt68Zm6awBTh2SL5oSqo8GeFMtCLG40iE3PKgF2OACOIojtgMlUyF2RO+J'
    b'TybfDiGheVGEGJQzzMgnlx5uAPqXeqXu/6ErAEPvG1//dtv9b5Vnz6o/VGvPqpuVzbXNSpXvf6xVH+7/+Q6vU3Wg5CyXo1jKK7FC'
    b'IrGS00eq4BsSj5WcCpbDN6crLR/iH+/s4VC8HNnhXyCKPfQmY6AVlFwZvl4BekPJd7kpIk3PBZIhbzFRcRbAUMerW6DtwONAxZwP'
    b'wFY5EL6GQse99upWYSW+UQdI8BU+eOcOXpizkvOlNeQ+H7UaO3ut0mQIVghsoRfYIR7Fhie4QzSol8vQy3HUR9+xLCefyiz3frCS'
    b'S6cIVQ2oAKVKqpLtxeX5TRlMh7wqjcOJAw2eXkgrjMD+nuV+RE/QmwmLLpajNAIeicKNv7oQZLQxKEonZPnAFnkNVnzdnN7YWwRq'
    b'QOCTlNP0rXNjsLLXxETpEumkNbrzDhMzYIIvxAzMCaQX6GhVKRf3BObvLJex/mMmfmMZu2X9155V1tT9jxuV2sYGrf/a+sP6/x6v'
    b'H1NrUPrloaTfBE3ojGPu9NHpNt282IU1EgVneb2ktFDadJeVuapKweWocJeCBQHUdy26wVEpnKSaPRmVAKYCUAloCSIeCsqXXHmu'
    b'CX5oFKNCTJ4VyG10nfvQBa7smHrj7NSa2qBJzgCU6rd1cT9NkstpFdmX4UyCkqC7uTAEQrqQVvwpT9fZ6VmsNTG9l5rEgLbzWYMQ'
    b'd/9CACkAUEo4bxs8lYHYQ5/BsXGDjSMOHcuVdEpz5uWwmeomgGnf9+gQLNBBgIxOFO/DwRhSiPrrIvIpCOLLVW6UUru2e2k59rCY'
    b'WwPvCOpzv5PelNSNGxNpuQp1r5slA/lrhCgWbxAANShzm/xUDS9+jO7j032+hgFoqMdAmNAw7kFysUfxhiR0MSOVj0WnizUt+FsR'
    b'uDrDXHAdhHKi/BTMHRC4hyQ014b+uyNZevoUL0WDRmJzRbsgIaRm455kV0oKASF3EMTHO6D+al2Ch3oALHAo9bB33f0bxCywLSoc'
    b'8mlW0N5BiMdz5cT7xQaAbkuYBed6OmYXVp0kNmaWM03daKrPwIKkZEyu8jHS5TpDibutpjCrXAwFV8lWLLh4+V3k2ji9tHLRsvph'
    b'AOu5tkk/wBSz5P74o75YL5frejonQ9f6DcknsYbWNNTpFdQrTwJx+lElHcHNlWVwpUp+8JFcm7NT/nRWYo5rKnTfKgcF9P16+qrK'
    b'tMOIJW66QdCik7NDG8J2yCh1PDkJM9Ddawxg+jpKAIKmPGXKPMmZ6paKIEyNCxIzr3cLEF/p6/X0AJnXcwAFr91dxS1w9K7cd7x+'
    b'eWKBjPrlRY59ncNm2n9FG2h+T/u/XgHkH+P/jdoa4/9K7cH+f4dXuWykOZL0fm95PI3WTgz/QaPjOTZUREhq4QZZdXA+jmGsOkgI'
    b'VggEtwiia0WRjpiXy0jufrd4ioxbPIHIl13kqYM+SOFL7vIU6i5PrK+v80wus/iCqzqBzk23dZbLjyAsH9B9CPEdHPHtCDomqTS8'
    b'0OcRwVKE17E9IyIBcGiiwNI9TEBBm3bU9UQI75HFbeRDyx+KSwsyJS7lYdjSkzqFspSi1zYabDdeJoH2Q5lkokRl0bziuONIp1Bo'
    b'hew0aOut1T4E+VJY4wOHA6lXRCkCe4rC9Yg+HQeJnTJtOiaDYOrAK44jZMfMAlgrMOUQxo0mmNxAI3bwjmghYkhZfXkFc0gENQBp'
    b'HHYCvnYD+hmM6fImc6PCBcnrI/T13ABTe1gbt+PDOqEcFQw06fzHjx95SDCIIBzW630PUNusXm96sxfxE1Lv9frvaHbP6QMeu+B3'
    b'n1/ExEC4qpvEPz1QA2cozMf2C6xdAkZZmqhPeAIEoOu5/PVRvond2KbuyGF+xfqv//y///N/rZSs4BzBXJAvQIYOqxgv3ae4dKHw'
    b'4m5kF2klY81sG8R5BsAnr1rQ4yfhxVXHMqAWCgmXut+Gz8AoRJpg5Kll+0VNhVEy+1DkkFtzAJcaYI45MmT6r8Tj08oVHnmoXDUq'
    b'+HOrij/5m231zdltDPl79HsVrGn18808wSbnmaClid7/CGCuQRfPYooV97vgRfagKOISDRAKMZIu3n0v/MhJtgwpvRMkGBx4cGHZ'
    b'DqJVSP9jLhe+VJIeuRCc5zt2bHcahbCmEigfAF711BE+vOPMjVcS386PpKyQR0pwDuMdT0BWA0mxlaQ1HHERgyHAu2iifAYbF686'
    b'rmjBqsSFygoQ1E/fG14/YdhJ+hjvZwtDiG5xDiJB7KBhfbSKjtKikmc09IiUvLSGMmafoKsHg2QJqzvixhaE/S1erphPQlXieEFA'
    b'CR4dhdMBMepO8j3rHrrwx76CoVKZsiG8RQEmgKhKMLt03AsdjaiPCSZLTCPcuIBuVRDGeuxjIi3n1JFzvbXpUn5U0aopjtvHE18w'
    b'OTACG1MHpgSh10PzcvwT4iH2rdiHUcYgiMtTWbpuhw6DzAUFU0ickv6sRXmbhCuJRJIBqfPGMBxYrMRUJ3Du5m6qg5lFn8LxlPlL'
    b'+6HiY7MitiofS7FtI6n0jct/mFm/wAiYYx9pCx+tKVBq5vfgKI69YZBmUzelRxhcUR/iUjgaZepilhCvVtI6KFiZM49ak8MPIjT1'
    b'bLyASicd0adl0Tn+qUrcQRmDt214kTXHfB0qf2UaICXVB6kjWikilaudLa4O77By3Pen6Wfb7XZdtKGrIQi9c0E7TlJjgGZP0CVO'
    b'QgpKkbFMFFUSkYMD4unTarVawZ+VKn7AtwS+1QdG4noxiuzy8FX8gV5zvW+aI6sD2MV9JMu73zM6eEOz1Xt2s3pDN0/sT/ZUDm1L'
    b'nGKuE29ii+GbdEsz/ZjwG34qszgVFGp/xH4siYfWMyBshvKBtc5/J0U3+ZqAkK1BeSyXCO9AgtbXnon8//uP//g/hToXV5Wegrj3'
    b'cXdxCPxkFpAA4geowwlNEF+IYeBaq1xV8Gswf1iSf0AtRUzAN8CTiuYF04dcb6ggn75fbgy+jahWSIYhPBXJeP8PRurNB3WDtP7j'
    b'RFSY3lXTTTUg95FId9wQt4BWD7ctxV/HglLXlX5KGruihl7xk8UmmkkT2OPFFvDbdAPNhQZgSqiBJvAZ4jWPTmeW7+ZJq7ujc4hX'
    b'BgXI2eSy4WT8Ld1tW6+38Jfx9cUkND7xDNbrR/TbeBCgj2R+Dn2NTaPwYqso4rfnkUvWUg4/c3nlg9Tr9BQV+/nMHobjF7kceE76'
    b'8Qt0hMpizwo+6T0TzC2Y4oA3XiCYs92Isz2IzEo53EcR0l6a871G911dRFvIqz7O+TmutWjrBdF9T7QU4dAaMdk8vptgkyB1j2Iy'
    b'hVva6zXenFPh4624RVwT5yhs2CI12UsMosKihtvAOJPsGMVdlfViR+HHU97SnG+CcQOT2aSjLDsSoA7M9DTqIxiNYCk20TztSF7C'
    b'amZz6DoK+kCQYLGM+J130LvCyPPlH+PmgoJYfS0eA3Xxu1gZqkp8LfKK+KyrDSyYV6PCAVF4+Ziov4aq+9BtKP5ZdQZkrF7fsYMp'
    b'nhW9rU9QmEmDTNXF4wmevcH6bQKNgAioSfqKpVRVxRfu55aP8lB1JeH2QIXmzbnQmGyFt3JRV3HOTsgJMFzelI6kneQy5Sz+j2Sy'
    b'DnGTkuW0fi2K1q8wT7gRBOSOSXEfsYkjOYowT6rtImGhJ3+vPEnhG4AovN2Dd1l2lTdejMloVBPiwVjctQI4FsEYYN+VPRWQUGPU'
    b'GQADmTvXMSHH/iTjznDEQsNsJZQ6NYG8oI7SWgAn+skEdZeMSaFzLCXidiIKjoYHCxxsjRWHkSDeziOi0LpmezN7iiwOZLHnVYwT'
    b'FmSuKFtwAbEFNG944WiOmQuJWtomykKLRWhTJy1Ay1Wrbw5JGiuQyBAp9qvKS730sumlJ7D7RUxg0TPldqBB5quXCEHgIfPwYlP3'
    b'CUVCXLzrQFGhdIOkLW1alu0wPtJAG7Mt4xZz5sTdvO/yF3vfy8Z4f++7nHjfDL6C5a53+Ytc7/LdXe/y3V1vFhHUxqi1kqLEL9Bc'
    b'p9HWGekqVlMvoeGXMBIIMC6qv9da/yXum3aZmV5Ra5J6XeuCwvzqUct/klr991hMRCxZUF+zmIjUVy+oxAW736pKc+CfaXUlI/qK'
    b'JZY1wf8tlprBnaz19mXs4HACGLBV9qIzx9bkMdBP/f728VT+XvnCoSyoDqP8P1p/oJqIdccOOyfg12EyFZENoh6N/Eq5lGqbo6n6'
    b'WNR4qa7bwF6rEzS39zzHPQdsB1IZexHcQsHAdgef8kCjIF69xrfpqcAHydkRcY6Ffs+Zk4LzjVBS4+5XkH4a1Ou4zM8HmFyCKB+3'
    b'WXKkS9NkVrcVT+Q59RBQpp9/bNArMlBlAvS0ELOlMNcVRkviHW0OQ1FmCaNrnhCSY0Bzgm49qmWdqwAcDbEroJZFSu2BTpPCWGeE'
    b'8TZJcJ21P4dRef9GFiXckG0qAk4hkIYtLZSnrp5zkwC646l7rJhyWiqdFUp2cO59WmComlGaxoOZC3MYuYF1IcF56FLLygSkPcq8'
    b'olwQnwuFFL3PQjowyEVOg5DlF6VurnIu/Y7cgR9PcWvTGa4AfHNuWPhCDOf5qnOtZ+P9NEn0si/Rw8fkob4wp9M81tH2J4EmYw+i'
    b'Micky9gY/UC9UU5COZSSpd1WGGrm2ZjTYuukj1ub+PP5M9LWG4meXq658eez9hnP0HJ9twfmKtbh8OZZmzT5HMNvAk16uCfJFX8U'
    b'Qqdh0UCSe4XUviF1YFednqPgMW3T0V4H6SzMQuAK8L0IAjYTC5x28IcocTBS9+TPfI/unQ9m0q+ruppEBzMYdZHYuFZiDVKcqpzp'
    b'Ktu0u6suqhRbghAQ/McgU0VUVfgp9YgjZvrRXPMQPIAi51z/XNUGvY9h163KfCkiki7VXCCZz3NlsapiVgXx8iW0XxB/iLwKMq2q'
    b'qgWm0jJ6Fsf4ftIPuEw7o/e0AmCW+LiK2ho24/CzmQgayTh0WCS/XJEraUJdG8P2HURiT/gEjTvAjDsneflsCwtJfFYPT4Mkf1yB'
    b'qMwJTAc1tK2yWagGLbWG4hVKEoeLEv8CgrRAz5FpFAeRsQNNba0s4okhTIK5eiMUaDdUUXN/zYDs5oLB4Pv1WIOpyAYYoZfRFhhG'
    b'LKC+o1Bbvd6Bb/jZgnklQjAhfc9zYvM58L1zTOgFjxCtG5owTzbzdzxXAnAeUnqoKLWiW6jrIksXKqcWOZtq7HEJS+ezDBzuJMtb'
    b'VPmpVcwyNSdS77vjZBcubTpcJGb0xzNdCIeS2/FooTaFl4AyPH6Uv02VZ47wHAHiLcNcHBSqW8LKr4gItL1o1AApEI4oiMdGRFG8'
    b'emVGDWE2iI62WmokRu+zx8JK3/OmRuewUxeUmHl168zQrDzu02D76VmhGcEcrIrUSMz5sb+H9KxQHeyyCEtzxqFPx7EhazwHJgiS'
    b'6F1zEPEhDyzM8NrSmEJPqxJUhC8JB14kN0gAi3WEDtiawrXi8WPNDAjKzo0eupXtIusdHfKS/uALuQ7i7xW11Qn3nZhkeK6SmVeT'
    b'CL3ilkHP1rYWmz4Ewyoa3WanExuqbPpKQ5SmUTDOE8kbGsNhXg0qOO6lPMlavUp+M4QczRzOQqoblUU7zyibmVFcJrdZqAwFdgbC'
    b'OpcOWBiqLhtw3u5VsnAXFh6PaZYNtHszT/nD8RZldTqUMEj2JNDRdtabmhHQOiyK/Cn1s6i6dabQfmmAofohopy5VYyvtUXtYo6Q'
    b'/8jyTQNMDzSf6kJhCeXEPqtksBHmpBU5RYnEAyZaIEtL6eQRBsSfGE2KUqmE0KgNoOLGilVVstVkEHXnijupFuOKz2+t2NItthda'
    b'XDoR88svc7qLPFd61m8klPFKpGRpzc83zaWRgLfmwzs4o9z30j2YGk9j4y68oYA7K45HrwgcL7NdWWJ+YV8AbruLmBvtcaWXhMHF'
    b'H3/QLMJH/voejQf21T0bp1pQA5h6HvM6yKcEocgdKTL5WwjOSVawqDq+QCrOlwCh5TU/5+5KI9MvNiPOhMFnHu57pJMLHABhSG3F'
    b'LlwspxSF9jn/RXSSR5Tr1bA5YbfiNmZZFcfxLXks0VpNSSu4MZh3hc/g3KgagL6MdC4ASnhMPtAmFmKFm1Eka4A3jwnXHm9YsEwF'
    b'y8NTBgf/IJcKvNBmOQP/zA84yB6xkjN+S7IW8wFCcC/E+lkSkjBPa4q1Gu+tjbeJJIGDoDov3GnZVuJIRWsLRTPkHksOaKuCdh7z'
    b'MB3VTB8UKMb+p6peLk8BHIaO+yi/8nu98nqr/1nM/4bx4tvqZv/zCnvnxJD0kgyqX03RHB38qmUQVBXXalRxUDCjJ49A0VaYCS9f'
    b'AU8AnQ3wHXIG92gZoRBjtlS+RD3A3QuVq6srQT8z31DRUwwMVKtq7wEtAWgQwwUUKziPwwXJj4J4/VpUt1jotxR4M90TYZDBKmoz'
    b'RSaZ2t3J6MpEK+4ckRGb9yejfqjOFVhTKTJn8ytZpb8+4uaGj8httZI5ZPtlSSq99flPk/NNbeRetnN72UgyM0XFhXRS6tyfJnVJ'
    b'R0Zu6eWdkkyL+ZZkGEaCaTHNEhejm1FokwvpT0xOYBpDYx/AHBDUoWkzKpggO91rfGryNZfp+BiBbt2RJJuCJLISvIVsq7sgytle'
    b'7VdK9pcmX/+ZxDsj3fotZVwzXBPjSv9gWTfHtCDwWcOK/8YvZkLnx1JLj8Tc8cPHim4ZQ2bi9PZ+U+Y0c40axe6xUOdqfc/VSulU'
    b'Y6UaqiZdh4eSnS3VgWDVBx1oRPdjrscJ1MJYsU443ZbOBCxmfgKDW5ujZCNw469AQvCGK/CyzDrpiGMfitPTU/vsxV0jc/2sqJwa'
    b'goozQSxrzg2ae741/9wWP70S1awIWf8eobhfI3vwaSHSZzbdv1u7C4217VEEwRfUa2NIBU7wknGe2xlHwJO/EBwnJUp3CpjN90hj'
    b'zxlCzfUlD6HbggrcmOyeoX+/nh1PO+SwEfMNQqd+uHAFQORqhbIYi9BMVV6wEqJSyf5pdnaLR7w0yQu9avEZnvX5UB+Ypk1tCpd5'
    b'+HHqOSvl/DjVwwJGorPooOMTlHB2YJGq6Huc+lTeJbk85BZk06DsD+Qho+rmC1E7w9teKvhmsXRwWsHHiIoRg1e4BTwYgGCZ3a2s'
    b'WlVdC7D01Ro4InG9P5Q39uK2CYOPpscIHSncIZBxC5HqrUQ+Ly6+2YvMZI9qKkZNOI+YNFbnxYxdcIlpZBhj7F0saZOUgQUNm4QZ'
    b'uMTzPsHNcM61ut4BMm0DtZXWaJN3VIwii/56QHxGjnfNKTqGI4i6oU9ndtAMKBtgrABKN6VSTYXFXJPayC7N0DRUXNQn4jWEqtOZ'
    b'whcp3lJO5v6MXb4zcZHPywGAwexHxA7dSh53JRfQwmQh96zNHGYhzc5YQ6ZpmMAxs9gSkGmWfTRPMw3csosuB3o39TaGVncgqot+'
    b'zvGMKh0KoWWYQ8fcQGgF8YnhCzrjZa7e5PgKaRIdk1o7m2OsOj0DtichDHOWfKAIyQ7HR5IoSOXW4AfvheDoX0JZcGDE2C5xh5DF'
    b'Ig0+QFTVOy/uEK/IphFHK7KDFV9//4c+6/8t7wC57f6vjfWNH6q1zVptfW2zsk73/61tbKw93P/xT3n/R0lf+JHc9oE7XfQ9H+rg'
    b'YXyFDZ06jEs+3OyR3OyBBNRRmRtu9sB9WEQ+Ss7Cq9O3NEGRPuaL15MZh3RccBNGdPALYigz+cRxkNQvWEX95UcMltguZFxkqZDT'
    b'/KTr2sBEQl5Jhhc0fLzeo+xfDNY2a89zAe5NGqDt3Tpvvm0cnZ90dnpv66ysaxubhDdz1eLN/24toLaPtb8VpbVvRmnjm1F61s5V'
    b'ijf/u7UAU3r+zShtM6XaDf9yteLN/5jSTju3Vrz5HxdstXPrRf3vpr6127kzdSryjX0pXbq5GlPZCGiLyS3d8x4yQmV9SdfchXQA'
    b'6XgFnGkEuQBnk1RhYP+m3UaFbedWwWmfLDGWO4vfvfgis/zweng9vB5e//DX/wf+qsnkAK4AAA=='
)
CESU8_APACHE = committed_fixture(
    (FALLBACKS / "cesu8-1.1.0-LICENSE-APACHE.txt").read_bytes(), 10847,
    "a60eea817514531668d7e00765731449fe14d059d3249e0bc93b36de45f759f2",
)
CESU8_MIT = committed_fixture(
    (FALLBACKS / "cesu8-1.1.0-LICENSE-MIT.txt").read_bytes(), 1071,
    "0621878e61f0d0fda054bcbe02df75192c28bde1ecc8289cbd86aeba2dd72720",
)


class Dispatch2FallbackTests(unittest.TestCase):
    """Exercise the shared objc2 declaration with an isolated synthetic crate."""

    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(prefix="cpa-dispatch2-notice-")).resolve(strict=True)
        self.case_number = 0

    def cargo_case(self, *, label: str = "Zlib OR Apache-2.0 OR MIT",
                   repository: str = "https://github.com/madsmtm/objc2",
                   vcs: str = "8852b424193ca41602281b3d7540d7c8ed51e49a",
                   vcs_path: str = "crates/dispatch2",
                   checksum_valid: bool = True,
                   declaration: bytes = BLOCK2_DECLARATION_BYTES,
                   license_text: bytes = MIT_TEMPLATE_BYTES,
                   member_count: int = 26,
                   nonregular: bool = False) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / "dispatch2-0.3.1"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        (source / "Cargo.toml").write_text("[package]\nname='dispatch2'\nversion='0.3.1'\n")
        local = []
        for name, version in sorted(notices.FIRST_PARTY_CARGO):
            local_manifest = workspace / name / "Cargo.toml"
            local_manifest.parent.mkdir()
            local_manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": name, "version": version, "source": None,
                          "manifest_path": str(local_manifest)})
        vcs_bytes = json.dumps({"git": {"sha1": vcs},
                                "path_in_vcs": vcs_path}).encode()
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for index in range(member_count):
                name = ("dispatch2-0.3.1/.cargo_vcs_info.json" if index == 0
                        else f"dispatch2-0.3.1/src/synthetic-{index:02d}.txt")
                item = tarfile.TarInfo(name)
                if nonregular and index == member_count - 1:
                    item.type = tarfile.DIRTYPE
                    tar.addfile(item)
                else:
                    data = vcs_bytes if index == 0 else b"synthetic"
                    item.size = len(data)
                    tar.addfile(item, io.BytesIO(data))
        data = buffer.getvalue()
        (cache / "dispatch2-0.3.1.crate").write_bytes(data)
        metadata = {"packages": local + [{
            "name": "dispatch2", "version": "0.3.1", "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"), "license": label,
            "repository": repository,
        }]}
        checksum = notices.sha(data) if checksum_valid else "0" * 64
        committed = {
            notices.BLOCK2_DECLARATION: declaration,
            notices.VENDORED_CARGO[("dispatch2", "0.3.1", "LICENSE-MIT.txt")]:
                license_text,
        }

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata), stderr="")

        # The fixed official checksum is independently pinned. Overriding it here
        # covers the fallback branch with a small synthetic 26-member archive.
        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "archive_files", return_value=({}, None)), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed.get(name, b"")), \
             patch.object(notices, "DISPATCH2_ARCHIVE_SHA256", notices.sha(data)), \
             patch.object(notices, "DISPATCH2_ARCHIVE_BYTES", len(data)):
            return notices.cargo_packages(workspace,
                                          {("dispatch2", "0.3.1"): checksum})[0]

    def test_shared_declaration_and_distinct_full_text(self) -> None:
        self.assertFalse(notices.has_authorization_text({
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
        }))
        package = self.cargo_case()
        self.assertEqual(package["license"], "Zlib OR Apache-2.0 OR MIT")
        self.assertEqual(package["notice_status"], "vendored_external")
        self.assertEqual(package["texts"], {
            "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md": BLOCK2_DECLARATION_BYTES,
            "EXTERNAL_ORIGIN/LICENSE-MIT.txt": MIT_TEMPLATE_BYTES,
        })
        self.assertEqual([item["role"] for item in package["external_sources"]],
                         ["upstream-license-declaration", "full-license-text"])
        self.assertEqual(package["external_sources"][0]["url"],
                         "https://raw.githubusercontent.com/madsmtm/objc2/"
                         "8852b424193ca41602281b3d7540d7c8ed51e49a/LICENSE.md")

    def test_archive_identity_and_member_shape_are_bound(self) -> None:
        for change, reason in (
            ({"checksum_valid": False}, "archive checksum mismatch"),
            ({"member_count": 25}, "archive member shape differs"),
            ({"member_count": 27}, "archive member shape differs"),
            ({"nonregular": True}, "archive member shape differs"),
            ({"vcs": "0" * 40}, "archive VCS origin differs"),
            ({"vcs_path": "crates/other"}, "archive VCS origin differs"),
            ({"repository": "https://example.invalid/objc2"},
             "external license identity differs"),
            ({"label": "MIT"}, "external license identity differs"),
        ):
            with self.subTest(change=change):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    self.cargo_case(**change)

    def test_both_committed_sources_are_required(self) -> None:
        for field in ("declaration", "license_text"):
            for bad in (b"", b"changed"):
                with self.subTest(field=field, bad=bad):
                    with self.assertRaisesRegex(notices.NoticeError,
                                                "vendored committed notice differs"):
                        self.cargo_case(**{field: bad})

    def test_synthetic_collect_verify_and_source_readback(self) -> None:
        package = self.cargo_case()
        workspace = self.scratch / "distribution-workspace"
        workspace.mkdir()
        output = self.scratch / "notice-output"
        source_names = [
            "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
        ]
        committed = {name: (name + "\n").encode() for name in source_names}
        committed[notices.BLOCK2_DECLARATION] = BLOCK2_DECLARATION_BYTES
        committed[notices.VENDORED_CARGO[("dispatch2", "0.3.1", "LICENSE-MIT.txt")]] = (
            MIT_TEMPLATE_BYTES
        )
        cargo = {("dispatch2", "0.3.1"):
                 package["integrity"].removeprefix("sha256-")}

        def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[0], "git")
            if argv[-1] == "--show-toplevel":
                value = str(workspace)
            elif argv[-1] == "HEAD":
                value = "a" * 40
            elif "status" in argv:
                value = ""
            else:
                self.fail("unexpected synthetic Git operation")
            return SimpleNamespace(returncode=0, stdout=value, stderr="")

        with patch.object(notices.subprocess, "run", side_effect=fake_git), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed[name]), \
             patch.object(notices, "cargo_lock", return_value=cargo), \
             patch.object(notices, "pnpm_lock", return_value={}), \
             patch.object(notices, "pnpm_graph", return_value=([], {})), \
             patch.object(notices, "bundle_packages", return_value=set()), \
             patch.object(notices, "pnpm_packages", return_value=([], [])), \
             patch.object(notices, "cargo_packages", return_value=[package]):
            notices.collect(workspace, output, workspace / "synthetic-bundle")
            manifest_data = (output / "manifest.json").read_bytes()
            manifest = json.loads(manifest_data)
            prefix = "cargo/dispatch2@0.3.1/EXTERNAL_ORIGIN/"
            declaration_file = output / (prefix + "UPSTREAM_DECLARATION.md")
            license_file = output / (prefix + "LICENSE-MIT.txt")
            self.assertEqual(declaration_file.read_bytes(), BLOCK2_DECLARATION_BYTES)
            self.assertEqual(license_file.read_bytes(), MIT_TEMPLATE_BYTES)
            notices.verify(output, workspace, notices.sha(manifest_data),
                           workspace / "synthetic-bundle")
            license_file.write_bytes(b"tampered")
            with self.assertRaisesRegex(notices.NoticeError,
                                        "notice hash/size mismatch"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")
            license_file.write_bytes(MIT_TEMPLATE_BYTES)
            committed[notices.BLOCK2_DECLARATION] = b"changed"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "committed source file changed"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")


class Dlopen2SharedLicenseTests(unittest.TestCase):
    """Each crate proves its own archive/VCS before using one MIT source.

    These are small synthetic archives; the two official archive checksums and
    member inventories are separate frozen source evidence, not mocked here.
    """

    CASES = (("dlopen2", "0.8.2", "dlopen2"),
             ("dlopen2_derive", "0.4.3", "dlopen2-derive"))
    VCS = "cc80e4a0a90d499b677fdf7743699b4b3a43a989"
    REPO = "https://github.com/OpenByteDev/dlopen2"

    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(prefix="cpa-dlopen2-notice-")).resolve(strict=True)
        self.case_number = 0

    def cargo_case(self, name: str, version: str, vcs_path: str, *,
                   checksum_valid: bool = True, pin_valid: bool = True,
                   vcs: str = VCS, path: str | None = None,
                   label: str = "MIT", repository: str = REPO,
                   license_text: bytes | None = None) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / f"{name}-{version}"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        cargo_manifest = ("[package]\n" + f"name='{name}'\n" +
                          f"version='{version}'\nlicense='MIT'\n").encode()
        vcs_bytes = json.dumps({"git": {"sha1": vcs},
                                "path_in_vcs": path or vcs_path}).encode()
        (source / "Cargo.toml").write_bytes(cargo_manifest)
        (source / ".cargo_vcs_info.json").write_bytes(vcs_bytes)
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for relative, data in (("Cargo.toml", cargo_manifest),
                                   (".cargo_vcs_info.json", vcs_bytes)):
                item = tarfile.TarInfo(f"{name}-{version}/{relative}")
                item.size = len(data)
                tar.addfile(item, io.BytesIO(data))
        data = buffer.getvalue()
        (cache / f"{name}-{version}.crate").write_bytes(data)
        local = []
        for local_name, local_version in sorted(notices.FIRST_PARTY_CARGO):
            local_manifest = workspace / local_name / "Cargo.toml"
            local_manifest.parent.mkdir()
            local_manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": local_name, "version": local_version,
                          "source": None, "manifest_path": str(local_manifest)})
        metadata = {"packages": local + [{
            "name": name, "version": version, "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"),
            "license": label, "repository": repository,
        }]}
        checksum = notices.sha(data) if checksum_valid else "0" * 64
        pinned = (notices.sha(data) if pin_valid else "0" * 64, len(data))
        vendored = notices.VENDORED_CARGO[(name, version, "LICENSE")]
        committed = {vendored: (MIT_DLOPEN2_BYTES if license_text is None
                                else license_text)}

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata), stderr="")

        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, item: committed.get(item, b"")), \
             patch.dict(notices.DLOPEN2_ARCHIVES, {(name, version): pinned}):
            return notices.cargo_packages(workspace, {(name, version): checksum})[0]

    def test_each_archive_needs_its_own_identity_and_full_text(self) -> None:
        for name, version, vcs_path in self.CASES:
            with self.subTest(name=name):
                package = self.cargo_case(name, version, vcs_path)
                self.assertEqual(package["license"], "MIT")
                self.assertEqual(package["texts"],
                                 {"EXTERNAL_ORIGIN/LICENSE": MIT_DLOPEN2_BYTES})
                self.assertEqual(package["notice_status"], "vendored_external")
                self.assertEqual(package["external_sources"][0]["role"],
                                 "full-license-text")
                self.assertEqual(package["external_sources"][0]["vendored_source"],
                                 notices.VENDORED_CARGO[(name, version, "LICENSE")])

    def test_each_archive_rejects_checksum_vcs_path_meta_and_source(self) -> None:
        for name, version, vcs_path in self.CASES:
            cases = (
                ({"checksum_valid": False}, "archive checksum mismatch"),
                ({"pin_valid": False}, "official archive identity differs"),
                ({"vcs": "0" * 40}, "archive VCS origin differs"),
                ({"path": "wrong/path"}, "archive VCS origin differs"),
                ({"label": "Apache-2.0"}, "external license identity differs"),
                ({"repository": "https://example.invalid/dlopen2"},
                 "external license identity differs"),
                ({"license_text": b""}, "vendored committed notice differs"),
                ({"license_text": b"changed"}, "vendored committed notice differs"),
            )
            for change, reason in cases:
                with self.subTest(name=name, change=change):
                    with self.assertRaisesRegex(notices.NoticeError, reason):
                        self.cargo_case(name, version, vcs_path, **change)

    def test_each_notice_collect_verify_and_tamper_refusal(self) -> None:
        for name, version, vcs_path in self.CASES:
            with self.subTest(name=name):
                package = self.cargo_case(name, version, vcs_path)
                workspace = self.scratch / f"distribution-{name}"
                workspace.mkdir()
                output = self.scratch / f"notices-{name}"
                source_names = [
                    "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
                    "third_party/cc-switch/LICENSE", "PROVENANCE.md",
                    "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
                ]
                committed = {item: (item + "\n").encode() for item in source_names}
                vendored = notices.VENDORED_CARGO[(name, version, "LICENSE")]
                committed[vendored] = MIT_DLOPEN2_BYTES
                cargo = {(name, version): package["integrity"].removeprefix("sha256-")}

                def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
                    self.assertEqual(argv[0], "git")
                    if argv[-1] == "--show-toplevel":
                        value = str(workspace)
                    elif argv[-1] == "HEAD":
                        value = "a" * 40
                    elif "status" in argv:
                        value = ""
                    else:
                        self.fail("unexpected synthetic Git operation")
                    return SimpleNamespace(returncode=0, stdout=value, stderr="")

                with patch.object(notices.subprocess, "run", side_effect=fake_git), \
                     patch.object(notices, "git_blob",
                                  side_effect=lambda _w, item: committed[item]), \
                     patch.object(notices, "cargo_lock", return_value=cargo), \
                     patch.object(notices, "pnpm_lock", return_value={}), \
                     patch.object(notices, "pnpm_graph", return_value=([], {})), \
                     patch.object(notices, "bundle_packages", return_value=set()), \
                     patch.object(notices, "pnpm_packages", return_value=([], [])), \
                     patch.object(notices, "cargo_packages", return_value=[package]):
                    notices.collect(workspace, output, workspace / "synthetic-bundle")
                    manifest_bytes = (output / "manifest.json").read_bytes()
                    prefix = f"cargo/{name}@{version}/EXTERNAL_ORIGIN/"
                    license_file = output / (prefix + "LICENSE")
                    self.assertEqual(license_file.read_bytes(), MIT_DLOPEN2_BYTES)
                    notices.verify(output, workspace, notices.sha(manifest_bytes),
                                   workspace / "synthetic-bundle")
                    license_file.write_bytes(b"tampered")
                    with self.assertRaisesRegex(notices.NoticeError,
                                                "notice hash/size mismatch"):
                        notices.verify(output, workspace, notices.sha(manifest_bytes),
                                       workspace / "synthetic-bundle")
                    license_file.write_bytes(MIT_DLOPEN2_BYTES)
                    committed[vendored] = b"changed"
                    with self.assertRaisesRegex(notices.NoticeError,
                                                "committed source file changed"):
                        notices.verify(output, workspace, notices.sha(manifest_bytes),
                                       workspace / "synthetic-bundle")


class JniSysMacrosFallbackTests(unittest.TestCase):
    """Synthetic Cargo identity refusals; official archive is verified separately."""

    VCS = "64d77b7a5f119d7b55b4e2c169a4668067ff59e6"
    REPO = "https://github.com/jni-rs/jni-sys"

    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(prefix="cpa-jni-macros-notice-")).resolve(strict=True)
        self.case_number = 0

    def cargo_case(self, *, checksum_valid: bool = True,
                   pin_valid: bool = True, vcs: str = VCS,
                   vcs_path: str = "jni-sys-macros",
                   label: str = "MIT OR Apache-2.0",
                   repository: str = REPO,
                   mit: bytes | None = None,
                   apache: bytes | None = None) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / "jni-sys-macros-0.4.1"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        cargo_manifest = (
            b'[package]\nname="jni-sys-macros"\nversion="0.4.1"\n'
            b'license="MIT OR Apache-2.0"\n'
            b'repository="https://github.com/jni-rs/jni-sys"\n')
        vcs_bytes = json.dumps({"git": {"sha1": vcs},
                                "path_in_vcs": vcs_path}).encode()
        (source / "Cargo.toml").write_bytes(cargo_manifest)
        (source / ".cargo_vcs_info.json").write_bytes(vcs_bytes)
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for relative, content in (("Cargo.toml", cargo_manifest),
                                      (".cargo_vcs_info.json", vcs_bytes)):
                item = tarfile.TarInfo("jni-sys-macros-0.4.1/" + relative)
                item.size = len(content)
                tar.addfile(item, io.BytesIO(content))
        data = buffer.getvalue()
        (cache / "jni-sys-macros-0.4.1.crate").write_bytes(data)
        local = []
        for local_name, local_version in sorted(notices.FIRST_PARTY_CARGO):
            local_manifest = workspace / local_name / "Cargo.toml"
            local_manifest.parent.mkdir()
            local_manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": local_name, "version": local_version,
                          "source": None, "manifest_path": str(local_manifest)})
        metadata = {"packages": local + [{
            "name": "jni-sys-macros", "version": "0.4.1",
            "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"),
            "license": label, "repository": repository,
        }]}
        checksum = notices.sha(data) if checksum_valid else "0" * 64
        committed = {
            notices.VENDORED_CARGO[("jni-sys-macros", "0.4.1", "LICENSE-MIT")]:
                JNI_MIT_BYTES if mit is None else mit,
            notices.VENDORED_CARGO[("jni-sys-macros", "0.4.1", "LICENSE-APACHE")]:
                JNI_APACHE_BYTES if apache is None else apache,
        }

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata), stderr="")

        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, item: committed.get(item, b"")), \
             patch.object(notices, "JNI_SYS_MACROS_ARCHIVE_SHA256",
                          notices.sha(data) if pin_valid else "0" * 64), \
             patch.object(notices, "JNI_SYS_MACROS_ARCHIVE_BYTES", len(data)):
            return notices.cargo_packages(
                workspace, {("jni-sys-macros", "0.4.1"): checksum})[0]

    def test_exact_identity_needs_both_full_texts(self) -> None:
        package = self.cargo_case()
        self.assertEqual(package["license"], "MIT OR Apache-2.0")
        self.assertEqual(package["notice_status"], "vendored_external")
        self.assertEqual(package["texts"], {
            "EXTERNAL_ORIGIN/LICENSE-MIT": JNI_MIT_BYTES,
            "EXTERNAL_ORIGIN/LICENSE-APACHE": JNI_APACHE_BYTES,
        })
        self.assertEqual(len(package["external_sources"]), 2)
        self.assertTrue(all(item["role"] == "full-license-text"
                            for item in package["external_sources"]))

    def test_checksum_vcs_path_metadata_and_both_texts_refuse(self) -> None:
        cases = (
            ({"checksum_valid": False}, "archive checksum mismatch"),
            ({"pin_valid": False}, "official archive identity differs"),
            ({"vcs": "0" * 40}, "archive VCS origin differs"),
            ({"vcs_path": "wrong/path"}, "archive VCS origin differs"),
            ({"label": "MIT"}, "external license identity differs"),
            ({"repository": "https://example.invalid/jni"},
             "external license identity differs"),
            ({"mit": b""}, "vendored committed notice differs"),
            ({"mit": b"tampered"}, "vendored committed notice differs"),
            ({"apache": b""}, "vendored committed notice differs"),
            ({"apache": b"tampered"}, "vendored committed notice differs"),
        )
        for change, reason in cases:
            with self.subTest(change=change):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    self.cargo_case(**change)

    def test_collect_verify_and_both_tamper_paths(self) -> None:
        package = self.cargo_case()
        workspace = self.scratch / "distribution"
        workspace.mkdir()
        output = self.scratch / "notices"
        names = [
            "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
        ]
        committed = {item: (item + "\n").encode() for item in names}
        for filename, content in (("LICENSE-MIT", JNI_MIT_BYTES),
                                  ("LICENSE-APACHE", JNI_APACHE_BYTES)):
            committed[notices.VENDORED_CARGO[
                ("jni-sys-macros", "0.4.1", filename)]] = content
        cargo = {("jni-sys-macros", "0.4.1"):
                 package["integrity"].removeprefix("sha256-")}

        def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[0], "git")
            if argv[-1] == "--show-toplevel":
                value = str(workspace)
            elif argv[-1] == "HEAD":
                value = "a" * 40
            elif "status" in argv:
                value = ""
            else:
                self.fail("unexpected synthetic Git operation")
            return SimpleNamespace(returncode=0, stdout=value, stderr="")

        with patch.object(notices.subprocess, "run", side_effect=fake_git), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _w, item: committed[item]), \
             patch.object(notices, "cargo_lock", return_value=cargo), \
             patch.object(notices, "pnpm_lock", return_value={}), \
             patch.object(notices, "pnpm_graph", return_value=([], {})), \
             patch.object(notices, "bundle_packages", return_value=set()), \
             patch.object(notices, "pnpm_packages", return_value=([], [])), \
             patch.object(notices, "cargo_packages", return_value=[package]):
            notices.collect(workspace, output, workspace / "synthetic-bundle")
            manifest_bytes = (output / "manifest.json").read_bytes()
            prefix = "cargo/jni-sys-macros@0.4.1/EXTERNAL_ORIGIN/"
            notices.verify(output, workspace, notices.sha(manifest_bytes),
                           workspace / "synthetic-bundle")
            for filename, original in (("LICENSE-MIT", JNI_MIT_BYTES),
                                       ("LICENSE-APACHE", JNI_APACHE_BYTES)):
                target = output / (prefix + filename)
                self.assertEqual(target.read_bytes(), original)
                target.write_bytes(b"tampered")
                with self.assertRaisesRegex(notices.NoticeError,
                                            "notice hash/size mismatch"):
                    notices.verify(output, workspace, notices.sha(manifest_bytes),
                                   workspace / "synthetic-bundle")
                target.write_bytes(original)
                vendored = notices.VENDORED_CARGO[
                    ("jni-sys-macros", "0.4.1", filename)]
                committed[vendored] = b"changed"
                with self.assertRaisesRegex(notices.NoticeError,
                                            "committed source file changed"):
                    notices.verify(output, workspace, notices.sha(manifest_bytes),
                                   workspace / "synthetic-bundle")
                committed[vendored] = original


class Cesu8LegacyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.scratch = Path(tempfile.mkdtemp(
            prefix="cpa-cesu8-notice-")).resolve(strict=True)
        self.case_number = 0

    def members(self) -> dict[str, bytes]:
        with tarfile.open(fileobj=io.BytesIO(CESU8_ARCHIVE), mode="r:gz") as tar:
            return {
                member.name.removeprefix("cesu8-1.1.0/"):
                    tar.extractfile(member).read()
                for member in tar.getmembers()
            }

    @staticmethod
    def synthetic_archive(rows: list[tuple[str, bytes | None]]) -> bytes:
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for name, body in rows:
                member = tarfile.TarInfo("cesu8-1.1.0/" + name)
                if body is None:
                    member.type = tarfile.SYMTYPE
                    member.linkname = "../outside"
                    tar.addfile(member)
                else:
                    member.size = len(body)
                    tar.addfile(member, io.BytesIO(body))
        return buffer.getvalue()

    def cargo_case(self, *, label: str = "Apache-2.0/MIT",
                   repository: str = "https://github.com/emk/cesu8-rs",
                   locked: str | None = None, apache: bytes = CESU8_APACHE,
                   mit: bytes = CESU8_MIT) -> dict:
        self.case_number += 1
        case = self.scratch / str(self.case_number)
        home = case / "cargo-home"
        source = home / "registry" / "src" / "index" / "cesu8-1.1.0"
        cache = home / "registry" / "cache" / "index"
        workspace = case / "workspace"
        for directory in (source, cache, workspace):
            directory.mkdir(parents=True)
        for name, body in self.members().items():
            path = source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        (cache / "cesu8-1.1.0.crate").write_bytes(CESU8_ARCHIVE)
        local = []
        for name, version in sorted(notices.FIRST_PARTY_CARGO):
            manifest = workspace / name / "Cargo.toml"
            manifest.parent.mkdir()
            manifest.write_text("[package]\nname='synthetic'\nversion='0.1.0'\n")
            local.append({"name": name, "version": version, "source": None,
                          "manifest_path": str(manifest)})
        metadata = {"packages": local + [{
            "name": "cesu8", "version": "1.1.0", "source": notices.CRATES_IO,
            "manifest_path": str(source / "Cargo.toml"),
            "license": label, "repository": repository,
        }]}
        committed = {
            notices.VENDORED_CARGO[("cesu8", "1.1.0", "LICENSE-APACHE")]:
                apache,
            notices.VENDORED_CARGO[("cesu8", "1.1.0", "LICENSE-MIT")]:
                mit,
        }

        def cargo_metadata(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[:2], ["cargo", "metadata"])
            return SimpleNamespace(returncode=0, stdout=json.dumps(metadata),
                                   stderr="")

        with patch.dict(os.environ, {"CARGO_HOME": str(home)}), \
             patch.object(notices.subprocess, "run", side_effect=cargo_metadata), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name:
                              committed.get(name, b"")):
            packages = notices.cargo_packages(
                workspace, {("cesu8", "1.1.0"):
                            locked or notices.sha(CESU8_ARCHIVE)})
        self.assertEqual(len(packages), 1)
        return packages[0]

    def test_exact_official_crate_and_both_complete_texts(self) -> None:
        self.assertEqual(len(CESU8_ARCHIVE), notices.CESU8_ARCHIVE_BYTES)
        self.assertEqual(notices.sha(CESU8_ARCHIVE),
                         notices.CESU8_ARCHIVE_SHA256)
        self.assertEqual(set(notices.cesu8_legacy_origin(CESU8_ARCHIVE)),
                         set(notices.CESU8_MEMBERS))
        package = self.cargo_case()
        self.assertEqual(package["notice_status"], "vendored_external")
        self.assertEqual(package["license"], "Apache-2.0/MIT")
        self.assertEqual(package["texts"]["EXTERNAL_ORIGIN/LICENSE-APACHE"],
                         CESU8_APACHE)
        self.assertEqual(package["texts"]["EXTERNAL_ORIGIN/LICENSE-MIT"],
                         CESU8_MIT)
        self.assertEqual([item["role"] for item in package["external_sources"]],
                         ["legacy-crate-archive-origin",
                          "full-license-text", "full-license-text"])

    def test_outer_checksum_and_lock_are_independent_gates(self) -> None:
        with self.assertRaisesRegex(notices.NoticeError,
                                    "cesu8 legacy archive checksum or size differs"):
            notices.cesu8_legacy_origin(CESU8_ARCHIVE + b"x")
        changed = bytearray(CESU8_ARCHIVE)
        changed[-1] ^= 1
        with self.assertRaisesRegex(notices.NoticeError,
                                    "cesu8 legacy archive checksum or size differs"):
            notices.cesu8_legacy_origin(bytes(changed))
        with self.assertRaisesRegex(notices.NoticeError,
                                    "Cargo archive checksum mismatch"):
            self.cargo_case(locked="0" * 64)

    def test_exact_member_inventory_type_and_git_blob(self) -> None:
        rows = list(self.members().items())
        self.assertEqual(len(rows), 7)
        self.assertEqual(
            set(notices.exact_regular_git_members(
                CESU8_ARCHIVE, "cesu8-1.1.0", notices.CESU8_MEMBERS)),
            set(notices.CESU8_MEMBERS))
        name, body = rows[0]
        cases = (
            ("missing", rows[:-1], "member count differs"),
            ("extra", rows + [("extra", b"x")], "member count differs"),
            ("duplicate", rows[:-1] + [rows[0]], "member identity differs"),
            ("nonregular", [(name, None), *rows[1:]], "member type or root differs"),
            ("content", [(name, body[:-1] + bytes([body[-1] ^ 1])),
                         *rows[1:]], "member Git blob differs"),
        )
        # Directly test the inner member gate: synthetic tar changes have new
        # outer checksums and cannot reach this branch through the production entry.
        for kind, modified, reason in cases:
            with self.subTest(kind=kind):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    notices.exact_regular_git_members(
                        self.synthetic_archive(modified), "cesu8-1.1.0",
                        notices.CESU8_MEMBERS)

    def test_declared_license_must_match_even_after_synthetic_pin_override(self) -> None:
        declarations = (
            ("Cargo.toml", b'license = "Apache-2.0/MIT"', b'license = "MIT"'),
            ("README.md", b"## License", b"## Notice"),
            ("COPYRIGHT-RUST.txt",
             b"dual-licensed under Apache 2.0 and MIT",
             b"single-licensed under Apache 2.0"),
        )
        originals = self.members()
        for name, old, new in declarations:
            with self.subTest(name=name):
                self.assertIn(old, originals[name])
                mutated = originals.copy()
                mutated[name] = originals[name].replace(old, new, 1)
                archive_data = self.synthetic_archive(list(mutated.items()))
                expected = notices.CESU8_MEMBERS.copy()
                content = mutated[name]
                expected[name] = hashlib.sha1(
                    b"blob " + str(len(content)).encode() + b"\x00" + content,
                    usedforsecurity=False).hexdigest()
                # Mock only exact archive/member pins to reach the later
                # semantic declaration check. This is not an accepted archive.
                with patch.object(notices, "CESU8_ARCHIVE_BYTES", len(archive_data)), \
                     patch.object(notices, "CESU8_ARCHIVE_SHA256",
                                  notices.sha(archive_data)), \
                     patch.object(notices, "CESU8_MEMBERS", expected):
                    with self.assertRaisesRegex(
                            notices.NoticeError,
                            "cesu8 legacy source license declaration differs"):
                        notices.cesu8_legacy_origin(archive_data)

    def test_metadata_identity_and_both_vendored_texts_fail_closed(self) -> None:
        for change, reason in (
            ({"label": "MIT"}, "external license identity differs"),
            ({"repository": "https://example.invalid/other"},
             "external license identity differs"),
            ({"apache": b"changed"}, "vendored committed notice differs"),
            ({"mit": b"changed"}, "vendored committed notice differs"),
        ):
            with self.subTest(change=tuple(change)):
                with self.assertRaisesRegex(notices.NoticeError, reason):
                    self.cargo_case(**change)

    def test_synthetic_collect_verify_and_both_tamper_paths(self) -> None:
        package = self.cargo_case()
        workspace = self.scratch / "distribution-workspace"
        workspace.mkdir()
        output = self.scratch / "notice-output"
        source_names = [
            "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md", *sorted(notices.VENDORED_FILES),
        ]
        committed = {name: (name + "\n").encode() for name in source_names}
        for filename, body in (("LICENSE-APACHE", CESU8_APACHE),
                               ("LICENSE-MIT", CESU8_MIT)):
            committed[notices.VENDORED_CARGO[
                ("cesu8", "1.1.0", filename)]] = body
        cargo = {("cesu8", "1.1.0"):
                 package["integrity"].removeprefix("sha256-")}

        def fake_git(argv: list[str], **_kwargs: object) -> SimpleNamespace:
            self.assertEqual(argv[0], "git")
            if argv[-1] == "--show-toplevel":
                value = str(workspace)
            elif argv[-1] == "HEAD":
                value = "a" * 40
            elif "status" in argv:
                value = ""
            else:
                self.fail("unexpected synthetic Git operation")
            return SimpleNamespace(returncode=0, stdout=value, stderr="")

        with patch.object(notices.subprocess, "run", side_effect=fake_git), \
             patch.object(notices, "git_blob",
                          side_effect=lambda _workspace, name: committed[name]), \
             patch.object(notices, "cargo_lock", return_value=cargo), \
             patch.object(notices, "pnpm_lock", return_value={}), \
             patch.object(notices, "pnpm_graph", return_value=([], {})), \
             patch.object(notices, "bundle_packages", return_value=set()), \
             patch.object(notices, "pnpm_packages", return_value=([], [])), \
             patch.object(notices, "cargo_packages", return_value=[package]):
            notices.collect(workspace, output, workspace / "synthetic-bundle")
            manifest_data = (output / "manifest.json").read_bytes()
            manifest = json.loads(manifest_data)
            prefix = "cargo/cesu8@1.1.0/EXTERNAL_ORIGIN/"
            for filename, body in (("LICENSE-APACHE", CESU8_APACHE),
                                   ("LICENSE-MIT", CESU8_MIT)):
                self.assertEqual((output / (prefix + filename)).read_bytes(),
                                 body)
            notices.verify(output, workspace, notices.sha(manifest_data),
                           workspace / "synthetic-bundle")
            for filename, body in (("LICENSE-APACHE", CESU8_APACHE),
                                   ("LICENSE-MIT", CESU8_MIT)):
                path = output / (prefix + filename)
                path.write_bytes(b"tampered")
                with self.assertRaisesRegex(notices.NoticeError,
                                            "notice hash/size mismatch"):
                    notices.verify(output, workspace,
                                   notices.sha(manifest_data),
                                   workspace / "synthetic-bundle")
                path.write_bytes(body)
            vendored = notices.VENDORED_CARGO[
                ("cesu8", "1.1.0", "LICENSE-MIT")]
            committed[vendored] = b"changed"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "committed source file changed"):
                notices.verify(output, workspace, notices.sha(manifest_data),
                               workspace / "synthetic-bundle")


class NoticeJunctionTests(unittest.TestCase):
    def setUp(self) -> None:
        # Keep each isolated fixture for inspection; never remove a computed tree.
        self.scratch = Path(tempfile.mkdtemp(
            prefix="cpa-notice-junction-")).resolve(strict=True)
        self.root = self.scratch / "package"
        self.root.mkdir()

    def populate(self, files: dict[str, bytes]) -> None:
        for name, data in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)

    def basic(self) -> bytes:
        files = {"package.json": MANIFEST, "LICENSE": LICENSE}
        self.populate(files)
        return archive(files)

    def test_ordinary_package_and_readback(self) -> None:
        data = self.basic()
        texts, manifest = notices.archive_files(data, "package",
                                                installed_root=self.root)
        self.assertEqual(texts, {"LICENSE": LICENSE})
        self.assertEqual(manifest, MANIFEST)
        self.assertEqual(notices.source_file(self.root / "LICENSE", self.root),
                         LICENSE)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_canonical_package_root_alias_is_allowed(self) -> None:
        data = self.basic()
        alias = self.scratch / "root-alias"
        junction(alias, self.root)
        texts, manifest = notices.archive_files(data, "package",
                                                installed_root=alias)
        self.assertEqual(texts["LICENSE"], LICENSE)
        self.assertEqual(manifest, MANIFEST)
        self.assertEqual(notices.source_file(alias / "LICENSE", alias), LICENSE)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_archived_in_root_alias_license_is_refused(self) -> None:
        self.populate({"package.json": MANIFEST, "real/LICENSE": LICENSE})
        junction(self.root / "alias", self.root / "real")
        data = archive({"package.json": MANIFEST, "real/LICENSE": LICENSE,
                        "alias/LICENSE": LICENSE}, {"real", "alias"})
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.archive_files(data, "package", installed_root=self.root)
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.source_file(self.root / "alias" / "LICENSE", self.root)

    @unittest.skipUnless(sys.platform == "win32", "native junction requires Windows")
    def test_unarchived_junction_is_refused_by_inventory(self) -> None:
        self.populate({"package.json": MANIFEST, "real/LICENSE": LICENSE})
        junction(self.root / "extra", self.root / "real")
        data = archive({"package.json": MANIFEST, "real/LICENSE": LICENSE},
                       {"real"})
        with self.assertRaisesRegex(notices.NoticeError, "reparse path refused"):
            notices.archive_files(data, "package", installed_root=self.root)

    def test_synthetic_symlink_to_same_license_is_refused(self) -> None:
        data = self.basic()
        target = self.scratch / "unvisited-symlink-target"
        hits = 0
        original = Path.is_symlink

        def is_symlink(path: Path) -> bool:
            nonlocal hits
            if path == target:
                hits += 1
                return True
            return original(path)

        with patch.object(Path, "is_symlink", is_symlink):
            texts, manifest = notices.archive_files(
                data, "package", installed_root=self.root)
            self.assertEqual(texts, {"LICENSE": LICENSE})
            self.assertEqual(manifest, MANIFEST)
            self.assertEqual(hits, 0)
            target = self.root / "LICENSE"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "reparse path refused"):
                notices.archive_files(data, "package", installed_root=self.root)
            self.assertGreater(hits, 0, "synthetic symlink target was not visited")

    def test_synthetic_windows_reparse_attribute_is_refused(self) -> None:
        data = self.basic()
        target = self.scratch / "unvisited-reparse-target"
        hits = 0
        original = Path.lstat

        def lstat(path: Path, *args: object, **kwargs: object) -> object:
            nonlocal hits
            info = original(path, *args, **kwargs)
            if path != target:
                return info
            hits += 1
            return SimpleNamespace(st_mode=info.st_mode, st_size=info.st_size,
                                   st_file_attributes=0x400)

        with patch.object(Path, "lstat", lstat):
            texts, manifest = notices.archive_files(
                data, "package", installed_root=self.root)
            self.assertEqual(texts, {"LICENSE": LICENSE})
            self.assertEqual(manifest, MANIFEST)
            self.assertEqual(hits, 0)
            target = self.root / "LICENSE"
            with self.assertRaisesRegex(notices.NoticeError,
                                        "reparse path refused"):
                notices.archive_files(data, "package", installed_root=self.root)
            self.assertGreater(hits, 0, "synthetic reparse target was not visited")

    def test_walk_error_is_not_silently_ignored(self) -> None:
        data = self.basic()

        def failed_walk(*_args: object, **kwargs: object) -> object:
            kwargs["onerror"](OSError("synthetic scandir failure"))
            yield from ()

        with patch.object(notices.os, "walk", failed_walk):
            with self.assertRaisesRegex(notices.NoticeError,
                                        "directory inventory traversal failed"):
                notices.archive_files(data, "package", installed_root=self.root)

    def test_nested_node_modules_remains_rejected(self) -> None:
        data = self.basic()
        nested = self.root / "node_modules" / ".bin"
        nested.mkdir(parents=True)
        (nested / "generated").write_text("synthetic", encoding="utf-8")
        with self.assertRaisesRegex(notices.NoticeError,
                                    "nested node_modules in installed package"):
            notices.archive_files(data, "package", installed_root=self.root)


if __name__ == "__main__":
    unittest.main()
