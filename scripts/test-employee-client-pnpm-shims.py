#!/usr/bin/env python3
"""Synthetic positive and refusal cases for bounded pnpm shim verification."""

from __future__ import annotations

import importlib.util
import base64
import hashlib
import io
import json
import os
import sys
import tarfile
import tempfile
from pathlib import Path
from unittest import mock

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("employee-client-pnpm-shims.py")
compile(SCRIPT.read_bytes(), str(SCRIPT), "exec")
SPEC = importlib.util.spec_from_file_location("employee_pnpm_shims", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


def project(root: Path, *, shim: bytes | None = None) -> Path:
    root.mkdir(parents=True)
    (root / "package.json").write_text(
        '{"name":"synthetic","packageManager":"pnpm@11.25.0"}',
        encoding="utf-8")
    (root / "pnpm-lock.yaml").write_bytes(b"lockfileVersion: '9.0'\n")
    package = (root / "node_modules" / ".pnpm" / "fake@1.0.0"
               / "node_modules" / "fake")
    package.mkdir(parents=True)
    (package / "package.json").write_bytes(b'{"name":"fake","version":"1.0.0"}')
    (package / "index.js").write_bytes(b"synthetic")
    (root / "node_modules" / ".pnpm" / "lock.yaml").write_bytes(b"lock")
    if shim is not None:
        bin_dir = package / "node_modules" / ".bin"
        bin_dir.mkdir(parents=True)
        (bin_dir / "fake").write_bytes(shim)
    return package


def paired(temp: Path):
    actual = temp / "actual"
    reference = temp / "reference"
    project(actual)
    project(reference)
    suffix = b"/node_modules/.pnpm/fake@1.0.0"
    actual_shim = str(actual).encode() + suffix
    ref_shim = str(reference).encode() + suffix
    a_bin = actual / "node_modules" / ".pnpm" / "fake@1.0.0" / "node_modules" / "fake" / "node_modules" / ".bin"
    r_bin = reference / "node_modules" / ".pnpm" / "fake@1.0.0" / "node_modules" / "fake" / "node_modules" / ".bin"
    a_bin.mkdir(parents=True)
    r_bin.mkdir(parents=True)
    (a_bin / "fake").write_bytes(b"#!/bin/sh\n" + actual_shim + b"\n")
    (r_bin / "fake").write_bytes(b"#!/bin/sh\n" + ref_shim + b"\n")
    return actual, reference, a_bin, r_bin


def refused(call, code: str):
    try:
        call()
    except MODULE.ShimError as exc:
        assert str(exc) == code, (str(exc), code)
    else:
        raise AssertionError("invalid shim tree was accepted")


def lockfile_crlf_source_case(root: Path) -> None:
    workspace = root / "workspace"
    project(workspace)
    (workspace / "pnpm-lock.yaml").write_bytes(b"lockfileVersion: '9.0'\r\n")
    assert MODULE.check_source_inputs(workspace) is None


def lockfile_crlf_reference_case(root: Path) -> None:
    actual, reference, a_bin, _ = paired(root)
    for workspace in (actual, reference):
        (workspace / "pnpm-lock.yaml").write_bytes(b"lockfileVersion: '9.0'\r\n")
    assert MODULE.check_inputs(actual, reference) is None
    assert (a_bin / "fake").exists()


def lockfile_rejection_cases(root: Path) -> None:
    prefix = b"lockfileVersion: '9.0'\n"
    invalid = {
        "bom": b"\xef\xbb\xbf" + prefix,
        "bare_cr": b"lockfileVersion: '9.0'\r",
        "double_cr": b"lockfileVersion: '9.0'\r\r\n",
        "wrong_version": b"lockfileVersion: '8.0'\n",
        "wrong_quotes": b'lockfileVersion: "9.0"\n',
        "missing_newline": b"lockfileVersion: '9.0'",
    }
    for label, data in invalid.items():
        actual, reference, a_bin, _ = paired(root / label)
        for workspace in (actual, reference):
            (workspace / "pnpm-lock.yaml").write_bytes(data)
        refused(lambda: MODULE.check_source_inputs(actual),
                "lockfile format differs")
        refused(lambda: MODULE.check_inputs(actual, reference),
                "lockfile format differs")
        assert (a_bin / "fake").exists()

    actual, reference, a_bin, _ = paired(root / "newline_mismatch")
    (reference / "pnpm-lock.yaml").write_bytes(b"lockfileVersion: '9.0'\r\n")
    refused(lambda: MODULE.check_inputs(actual, reference),
            "reference input differs")
    assert (a_bin / "fake").exists()

    actual, reference, a_bin, _ = paired(root / "tail_mismatch")
    (actual / "pnpm-lock.yaml").write_bytes(prefix + b"source")
    (reference / "pnpm-lock.yaml").write_bytes(prefix + b"reference")
    refused(lambda: MODULE.check_inputs(actual, reference),
            "reference input differs")
    assert (a_bin / "fake").exists()

    actual, reference, a_bin, _ = paired(root / "oversized")
    too_large = prefix + b"x" * (2 * 1024 * 1024)
    for workspace in (actual, reference):
        (workspace / "pnpm-lock.yaml").write_bytes(too_large)
    refused(lambda: MODULE.check_source_inputs(actual),
            "file size budget exceeded")
    refused(lambda: MODULE.check_inputs(actual, reference),
            "file size budget exceeded")
    assert (a_bin / "fake").exists()


def tiny_archive(extra_link: bool = False,
                 extra_member: str | None = None) -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as output:
        for name, data in (("package/bin/pnpm.mjs", b"bin"),
                           ("package/dist/pnpm.mjs", b"dist")):
            member = tarfile.TarInfo(name)
            member.size = len(data)
            output.addfile(member, io.BytesIO(data))
        if extra_member is not None:
            member = tarfile.TarInfo(extra_member)
            member.size = 4
            output.addfile(member, io.BytesIO(b"text"))
        if extra_link:
            member = tarfile.TarInfo("package/unsafe")
            member.type = tarfile.SYMTYPE
            member.linkname = "../../outside"
            output.addfile(member)
    return buffer.getvalue()


def main():
    names = []
    with tempfile.TemporaryDirectory(prefix="employee-pnpm-shim-test-") as tempdir:
        temp = Path(tempdir)

        actual, reference, a_bin, _ = paired(temp / "positive")
        assert MODULE.check_source_inputs(actual) is None
        assert MODULE.check_inputs(actual, reference) is None
        assert MODULE.normalized((a_bin / "fake").read_bytes(), actual) == MODULE.normalized(
            (reference / "node_modules" / ".pnpm" / "fake@1.0.0"
             / "node_modules" / "fake" / "node_modules" / ".bin" / "fake").read_bytes(),
            reference)
        assert MODULE.reconcile(actual, reference) == (1, 1)
        assert not (a_bin / "fake").exists() and not a_bin.exists()
        assert not a_bin.parent.exists()
        assert not MODULE.scan_store(actual).nested
        names.append("matched_reference_removed_only_verified_shim")

        lockfile_crlf_source_case(temp / "crlf_source")
        names.append("source_lockfile_crlf_exact_prefix")
        lockfile_crlf_reference_case(temp / "crlf_reference")
        names.append("reference_lockfile_crlf_exact_bytes")
        lockfile_rejection_cases(temp / "lockfile_refusals")
        names.append("lockfile_prefix_mismatch_size_and_shim_refusals")

        config = temp / "employee-pnpm-config"
        config.mkdir()
        (config / "empty.npmrc").write_bytes(b"")
        pinned_env = {"XDG_CONFIG_HOME": str(config),
                      "NPM_CONFIG_USERCONFIG": str(config / "empty.npmrc"),
                      "NPM_CONFIG_GLOBALCONFIG": str(config / "empty.npmrc")}
        with mock.patch.dict(os.environ, pinned_env, clear=True):
            assert MODULE.check_config(actual, temp, "11.25.0") is None
        with mock.patch.dict(os.environ,
                             {**pinned_env, "PNPM_CONFIG_NODE_LINKER": "hoisted"},
                             clear=True):
            refused(lambda: MODULE.check_config(actual, temp, "11.25.0"),
                    "pnpm environment configuration not pinned")
        names.append("pinned_config_and_unpinned_env_refusal")

        tiny = tiny_archive()
        pins = {
            "PNPM_EXPECTED_MEMBERS": 2,
            "PNPM_TARBALL_SHA512":
                base64.b64encode(hashlib.sha512(tiny).digest()).decode(),
            "PNPM_TARBALL_SHA256": hashlib.sha256(tiny).hexdigest().upper(),
            "PNPM_BIN_SHA256": hashlib.sha256(b"bin").hexdigest().upper(),
            "PNPM_DIST_SHA256": hashlib.sha256(b"dist").hexdigest().upper(),
        }
        with mock.patch.multiple(MODULE, **pins):
            assert dict(MODULE.verified_members(tiny)) == {
                "package/bin/pnpm.mjs": b"bin",
                "package/dist/pnpm.mjs": b"dist"}
            rejected = temp / "rejected_tool"
            rejected.mkdir()
            with mock.patch.object(MODULE.urllib.request, "urlopen",
                                   return_value=io.BytesIO(tiny + b"x")):
                refused(lambda: MODULE.pin_tool(rejected),
                        "pnpm archive integrity differs")
            assert not (rejected / MODULE.PNPM_TOOL_DIR).exists()
            tool_root = temp / "valid_tool"
            tool_root.mkdir()
            with mock.patch.object(MODULE.urllib.request, "urlopen",
                                   return_value=io.BytesIO(tiny)):
                MODULE.pin_tool(tool_root)
            MODULE.verify_tool(tool_root)
            (tool_root / MODULE.PNPM_TOOL_DIR / "package" / "extra").write_bytes(b"x")
            refused(lambda: MODULE.verify_tool(tool_root),
                    "pnpm extracted entry set differs")
        names.append("verified_archive_before_extraction_and_exact_payload")

        linked = tiny_archive(extra_link=True)
        link_pins = {**pins,
                     "PNPM_EXPECTED_MEMBERS": 3,
                     "PNPM_TARBALL_SHA512":
                         base64.b64encode(hashlib.sha512(linked).digest()).decode(),
                     "PNPM_TARBALL_SHA256":
                         hashlib.sha256(linked).hexdigest().upper()}
        with mock.patch.multiple(MODULE, **link_pins):
            refused(lambda: MODULE.verified_members(linked),
                    "pnpm archive member path or type invalid")
        names.append("archive_link_refused")

        license_archive = tiny_archive(extra_member="package/LICENSE")
        license_pins = {**pins,
                        "PNPM_EXPECTED_MEMBERS": 3,
                        "PNPM_TARBALL_SHA512":
                            base64.b64encode(
                                hashlib.sha512(license_archive).digest()).decode(),
                        "PNPM_TARBALL_SHA256":
                            hashlib.sha256(license_archive).hexdigest().upper()}
        with mock.patch.multiple(MODULE, **license_pins):
            assert dict(MODULE.verified_members(license_archive))[
                "package/LICENSE"] == b"text"
        names.append("safe_two_component_regular_member_accepted")

        traversal = tiny_archive(extra_member="package/../escape")
        traversal_pins = {**pins,
                          "PNPM_EXPECTED_MEMBERS": 3,
                          "PNPM_TARBALL_SHA512":
                              base64.b64encode(
                                  hashlib.sha512(traversal).digest()).decode(),
                          "PNPM_TARBALL_SHA256":
                              hashlib.sha256(traversal).hexdigest().upper()}
        with mock.patch.multiple(MODULE, **traversal_pins):
            refused(lambda: MODULE.verified_members(traversal),
                    "pnpm archive member path or type invalid")
        names.append("archive_traversal_refused")

        actual, reference, a_bin, _ = paired(temp / "entry_budget")
        with mock.patch.object(MODULE, "MAX_CHILDREN", 1):
            refused(lambda: MODULE.reconcile(actual, reference),
                    "directory entry budget exceeded")
        assert (a_bin / "fake").exists()
        names.append("directory_budget_before_sort_preserved")

        actual, reference, a_bin, _ = paired(temp / "byte_mismatch")
        (a_bin / "fake").write_bytes(b"unexpected untrusted bytes")
        refused(lambda: MODULE.reconcile(actual, reference),
                "generated shim bytes differ")
        assert (a_bin / "fake").exists()
        names.append("mismatched_bytes_preserved")

        actual, reference, a_bin, _ = paired(temp / "changed_before_unlink")
        original_normalized = MODULE.normalized
        def change_after_comparison(content: bytes, root: Path) -> bytes:
            result = original_normalized(content, root)
            if root == reference:
                (a_bin / "fake").write_bytes(b"changed before unlink")
            return result
        with mock.patch.object(MODULE, "normalized",
                               side_effect=change_after_comparison):
            refused(lambda: MODULE.reconcile(actual, reference),
                    "generated shim changed before removal")
        assert (a_bin / "fake").exists()
        names.append("changed_before_unlink_preserved")

        actual, reference, a_bin, _ = paired(temp / "extra_shim")
        (a_bin / "extra").write_bytes(b"extra")
        refused(lambda: MODULE.reconcile(actual, reference),
                "pnpm nested entry set differs")
        assert (a_bin / "extra").exists()
        names.append("extra_shim_preserved")

        actual, reference, a_bin, _ = paired(temp / "shape")
        (a_bin.parent / "other").mkdir()
        refused(lambda: MODULE.reconcile(actual, reference),
                "nested node_modules has unknown entries")
        assert (a_bin / "fake").exists()
        names.append("unknown_nested_shape_preserved")

        actual, reference, a_bin, _ = paired(temp / "deep_nested")
        (a_bin.parent.parent / "lib" / "node_modules").mkdir(parents=True)
        refused(lambda: MODULE.reconcile(actual, reference),
                "unknown nested node_modules shape")
        assert (a_bin / "fake").exists()
        names.append("deep_nested_shape_preserved")

        actual, reference, a_bin, _ = paired(temp / "input_mismatch")
        (reference / "pnpm-lock.yaml").write_bytes(b"lockfileVersion: '9.0'\nchanged")
        refused(lambda: MODULE.reconcile(actual, reference),
                "reference input differs")
        assert (a_bin / "fake").exists()
        names.append("input_mismatch_preserved")

        actual, reference, a_bin, _ = paired(temp / "oversized")
        (a_bin / "fake").write_bytes(b"x" * (MODULE.MAX_SHIM_BYTES + 1))
        refused(lambda: MODULE.reconcile(actual, reference),
                "file size budget exceeded")
        assert (a_bin / "fake").exists()
        names.append("oversized_shim_preserved")

        actual, reference, a_bin, _ = paired(temp / "junction")
        original = Path.is_junction
        with mock.patch.object(Path, "is_junction",
                               lambda self: self.name == "fake"
                               and self.parent.name == ".bin"
                               or original(self)):
            refused(lambda: MODULE.reconcile(actual, reference),
                    "linked or reparse path refused")
        assert (a_bin / "fake").exists()
        names.append("junction_shim_preserved")

        actual, reference, a_bin, _ = paired(temp / "count_budget")
        (a_bin / "extra").write_bytes(b"extra")
        (reference / "node_modules" / ".pnpm" / "fake@1.0.0"
         / "node_modules" / "fake" / "node_modules" / ".bin" / "extra").write_bytes(b"extra")
        with mock.patch.object(MODULE, "MAX_SHIMS", 1):
            refused(lambda: MODULE.reconcile(actual, reference),
                    "shim count budget exceeded")
        assert (a_bin / "fake").exists() and (a_bin / "extra").exists()
        names.append("shim_budget_preserved")

        actual, reference, a_bin, _ = paired(temp / "regrowth")
        MODULE.reconcile(actual, reference)
        a_bin.mkdir(parents=True)
        (a_bin / "fake").write_bytes(b"regrew")
        assert MODULE.scan_store(actual).nested
        names.append("post_build_regrowth_detected")

        actual, reference, a_bin, _ = paired(temp / "reference_inside")
        refused(lambda: MODULE.check_inputs(actual, actual / "node_modules"),
                "reference is not independent")
        assert (a_bin / "fake").exists()
        names.append("reference_inside_workspace_refused")

        # Each exact spelling is normalized. An unknown root-like spelling is not.
        with tempfile.TemporaryDirectory(prefix="employee-pnpm-variants-") as variant_dir:
            root = Path(variant_dir)
            for spelling in MODULE.root_literals(root):
                assert MODULE.normalized(spelling, root) == b"@CPA_PNPM_WORKSPACE_ROOT@"
            assert MODULE.normalized(b"UNTRUSTED-OTHER-ROOT", root) == b"UNTRUSTED-OTHER-ROOT"
        names.append("exact_root_variants_only")

    print(json.dumps({"passed": names, "count": len(names)}, sort_keys=True))


if __name__ == "__main__":
    main()
