#!/usr/bin/env python3
"""Verify and remove only reference-matched pnpm-generated nested bin shims.

Provenance: independently authored for CPA Cloud from pnpm 11 public layout and
install documentation. No pnpm implementation code is copied.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import os
import stat
import sys
import tarfile
import urllib.request
from dataclasses import dataclass
from pathlib import Path

PNPM_VERSION = "11.25.0"
PNPM_URL = "https://registry.npmjs.org/pnpm/-/pnpm-11.25.0.tgz"
PNPM_TARBALL_SHA512 = "XN6SW08HX3Jetx+64YpC/+eEUkeJ8ZthxzHLhyHsKKruFg4BqNWvT+2ypCzb8wDv4j2zVrDUoXtNY+EfirfJVg=="
PNPM_TARBALL_SHA256 = "33DD0748F27E7916C4F1C8B6943461983E3453B06BBDA6312A6280130B4881E5"
PNPM_BIN_SHA256 = "FF3224D46B47FBB24A7E9FE15FEDEDEF7E00892D07D4E376B6762D4899906BFD"
PNPM_DIST_SHA256 = "DDC64218BC85FB88D28B5DEF06EB01FAFB39CB67F7A9465CE736456658CC7F11"
PNPM_TOOL_DIR = "employee-pnpm-tool"
PNPM_TARBALL_NAME = "pnpm-11.25.0.tgz"
MAX_TARBALL_BYTES = 16 * 1024 * 1024
MAX_PACKAGE_BYTES = 32 * 1024 * 1024
MAX_PACKAGE_FILE_BYTES = 16 * 1024 * 1024
MAX_PACKAGE_MEMBERS = 500
PNPM_EXPECTED_MEMBERS = 455
MAX_PACKAGES = 4096
MAX_ENTRIES = 200000
MAX_CHILDREN = 8192
MAX_SHIMS = 4096
MAX_SHIM_BYTES = 65536
MAX_TOTAL_SHIM_BYTES = 16 * 1024 * 1024
REPARSE_POINT = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
KINDS = ("directory", "regular")


class ShimError(Exception):
    """A fixed-message refusal; caller must not print untrusted paths."""


REFUSAL_CATEGORIES = {
    "linked or reparse path refused": "reparse",
    "unexpected path kind": "path_kind",
    "root alias refused": "root_alias",
    "directory entry budget exceeded": "entry_budget",
    "file size budget exceeded": "file_budget",
    "file changed during read": "file_identity",
    "pnpm version not pinned": "version",
    "project pnpm configuration not pinned": "project_config",
    "pnpm config directory not pinned": "config_root",
    "pnpm rc configuration not pinned": "config_rc",
    "pnpm config directory changed": "config_entries",
    "pnpm rc is not empty": "config_nonempty",
    "pnpm environment configuration not pinned": "environment_config",
    "package manager identity differs": "package_manager",
    "lockfile format differs": "lockfile",
    "pnpm tool directory already exists": "tool_exists",
    "pnpm archive size invalid": "archive_size",
    "pnpm archive integrity differs": "archive_integrity",
    "pnpm archive member count differs": "archive_members",
    "pnpm archive member path or type invalid": "archive_member",
    "pnpm executable bytes differ": "tool_payload",
    "generated nested directory regrew": "shim_regrowth",
}

# These names are fixed source literals. Never print exception text, paths,
# environment entries, or shim bytes from a runner refusal.
RECONCILE_REASONS = {
    "reference is not independent": "reference_not_independent",
    "reference inputs are not isolated": "reference_not_isolated",
    "reference input differs": "reference_input_mismatch",
    "directory changed during scan": "directory_changed",
    "package count exceeded": "package_count",
    "pnpm physical package roots absent": "package_roots_absent",
    "package scan budget exceeded": "scan_budget",
    "linked package content refused": "linked_content",
    "unknown nested node_modules shape": "nested_shape",
    "nested node_modules has unknown entries": "nested_entries",
    "empty generated bin directory": "empty_bin",
    "generated shim is hard-linked": "hard_linked_shim",
    "shim total byte budget exceeded": "shim_bytes",
    "duplicate generated shim": "duplicate_shim",
    "shim count budget exceeded": "shim_count",
    "unsupported package content kind": "package_kind",
    "pnpm physical graph differs": "graph_mismatch",
    "pnpm nested entry set differs": "entry_set_mismatch",
    "generated shim bytes differ": "shim_bytes_mismatch",
    "workspace root remained in shim": "root_normalization",
    "generated shim changed before removal": "pre_unlink_changed",
    "generated nested directory regrew": "post_scan_regrowth",
}


def refusal_reason(error: Exception) -> str:
    if isinstance(error, ShimError):
        literal = error.args[0] if error.args else None
        return RECONCILE_REASONS.get(
            literal, REFUSAL_CATEGORIES.get(literal, "unmapped_refusal"))
    return "non_refusal_exception"


def refusal_category(error: Exception) -> str:
    """Return only reviewed fixed categories, never exception text or values."""
    if isinstance(error, ShimError):
        return REFUSAL_CATEGORIES.get(error.args[0]
                                      if error.args else None, "other_refusal")
    if isinstance(error, OSError):
        return "filesystem_error"
    if isinstance(error, (UnicodeError, ValueError)):
        return "data_error"
    if isinstance(error, tarfile.TarError):
        return "archive_error"
    if isinstance(error, EOFError):
        return "end_of_input"
    return "internal_error"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ShimError(message)


def plain_info(path: Path, kind: str) -> os.stat_result:
    require(kind in KINDS, "invalid scanner kind")
    info = path.lstat()
    require(not stat.S_ISLNK(info.st_mode)
            and not path.is_junction()
            and not (getattr(info, "st_file_attributes", 0) & REPARSE_POINT),
            "linked or reparse path refused")
    require((stat.S_ISDIR(info.st_mode) if kind == "directory"
             else stat.S_ISREG(info.st_mode)), "unexpected path kind")
    return info


def identity(info: os.stat_result) -> tuple[int, int, int, int]:
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns)


def absolute_plain(path: Path) -> Path:
    path = Path(os.path.abspath(path))
    for component in reversed((path, *path.parents)):
        plain_info(component, "directory")
    require(path.resolve(strict=True) == path, "root alias refused")
    return path


def children(path: Path) -> list[Path]:
    before = plain_info(path, "directory")
    found: list[Path] = []
    with os.scandir(path) as entries:
        for entry in entries:
            require(len(found) < MAX_CHILDREN,
                    "directory entry budget exceeded")
            found.append(path / entry.name)
    require(identity(plain_info(path, "directory")) == identity(before),
            "directory changed during scan")
    return sorted(found, key=lambda item: item.name)


def read_regular(path: Path, limit: int) -> tuple[bytes, tuple[int, int, int, int]]:
    before = plain_info(path, "regular")
    require(0 <= before.st_size <= limit, "file size budget exceeded")
    with path.open("rb") as stream:
        require(identity(os.fstat(stream.fileno())) == identity(before),
                "file changed during read")
        content = stream.read(limit + 1)
        require(identity(os.fstat(stream.fileno())) == identity(before),
                "file changed during read")
    after = plain_info(path, "regular")
    require(len(content) == before.st_size and len(content) <= limit
            and identity(before) == identity(after), "file changed during read")
    return content, identity(after)


def absent(path: Path) -> bool:
    try:
        path.lstat()
    except FileNotFoundError:
        return True
    return False


def verified_members(archive: bytes) -> list[tuple[str, bytes]]:
    require(0 < len(archive) <= MAX_TARBALL_BYTES,
            "pnpm archive size invalid")
    require(base64.b64encode(hashlib.sha512(archive).digest()).decode("ascii")
            == PNPM_TARBALL_SHA512
            and hashlib.sha256(archive).hexdigest().upper()
            == PNPM_TARBALL_SHA256,
            "pnpm archive integrity differs")
    output: list[tuple[str, bytes]] = []
    seen: set[str] = set()
    total = 0
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as source:
        members = source.getmembers()
        require(len(members) == PNPM_EXPECTED_MEMBERS
                and len(members) <= MAX_PACKAGE_MEMBERS,
                "pnpm archive member count differs")
        for member in members:
            parts = member.name.split("/")
            require(member.isfile() and len(parts) >= 2
                    and parts[0] == "package"
                    and all(part not in ("", ".", "..") for part in parts)
                    and "\\" not in member.name
                    and not member.name.startswith("/"),
                    "pnpm archive member path or type invalid")
            relative = "/".join(parts)
            folded = relative.casefold()
            require(folded not in seen, "pnpm archive path collision")
            seen.add(folded)
            require(0 <= member.size <= MAX_PACKAGE_FILE_BYTES,
                    "pnpm archive member size invalid")
            total += member.size
            require(total <= MAX_PACKAGE_BYTES,
                    "pnpm archive expanded size exceeded")
            stream = source.extractfile(member)
            require(stream is not None, "pnpm archive member unavailable")
            content = stream.read(member.size + 1)
            require(len(content) == member.size,
                    "pnpm archive member bytes differ")
            output.append((relative, content))
    by_name = dict(output)
    require("package/bin/pnpm.mjs" in by_name
            and "package/dist/pnpm.mjs" in by_name,
            "pnpm executable members absent")
    require(hashlib.sha256(by_name["package/bin/pnpm.mjs"]).hexdigest().upper()
            == PNPM_BIN_SHA256
            and hashlib.sha256(by_name["package/dist/pnpm.mjs"]).hexdigest().upper()
            == PNPM_DIST_SHA256,
            "pnpm executable bytes differ")
    return output


def pin_tool(runner_temp: Path) -> None:
    root = runner_temp / PNPM_TOOL_DIR
    require(absent(root), "pnpm tool directory already exists")
    with urllib.request.urlopen(PNPM_URL, timeout=30) as response:
        archive = response.read(MAX_TARBALL_BYTES + 1)
    members = verified_members(archive)  # Verify before creating extraction paths.
    root.mkdir()
    with (root / PNPM_TARBALL_NAME).open("xb") as stream:
        stream.write(archive)
    for relative, content in members:
        target = root / Path(relative)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("xb") as stream:
            stream.write(content)
    verify_tool(runner_temp)


def verify_tool(runner_temp: Path) -> None:
    root = runner_temp / PNPM_TOOL_DIR
    plain_info(root, "directory")
    archive = read_regular(root / PNPM_TARBALL_NAME,
                           MAX_TARBALL_BYTES)[0]
    members = verified_members(archive)
    expected_files = {PNPM_TARBALL_NAME} | {name for name, _ in members}
    expected_dirs: set[str] = set()
    for name, _ in members:
        parent = Path(name).parent
        while str(parent) != ".":
            expected_dirs.add(parent.as_posix())
            parent = parent.parent
    actual_files: set[str] = set()
    actual_dirs: set[str] = set()
    pending = [root]
    while pending:
        for child in children(pending.pop()):
            info = child.lstat()
            relative = child.relative_to(root).as_posix()
            if stat.S_ISDIR(info.st_mode):
                plain_info(child, "directory")
                actual_dirs.add(relative)
                pending.append(child)
            else:
                plain_info(child, "regular")
                actual_files.add(relative)
    require(actual_files == expected_files and actual_dirs == expected_dirs,
            "pnpm extracted entry set differs")
    for name, content in members:
        require(read_regular(root / Path(name), MAX_PACKAGE_FILE_BYTES)[0]
                == content, "pnpm extracted member differs")
    require(hashlib.sha256(read_regular(
                root / "package" / "bin" / "pnpm.mjs",
                MAX_PACKAGE_FILE_BYTES)[0]).hexdigest().upper()
            == PNPM_BIN_SHA256
            and hashlib.sha256(read_regular(
                root / "package" / "dist" / "pnpm.mjs",
                MAX_PACKAGE_FILE_BYTES)[0]).hexdigest().upper()
            == PNPM_DIST_SHA256, "pnpm executable bytes differ")


def check_config(workspace: Path, runner_temp: Path, version: str) -> None:
    require(version == PNPM_VERSION, "pnpm version not pinned")
    for directory in (workspace, workspace.parent, workspace.parent.parent):
        require(absent(directory / ".npmrc")
                and absent(directory / "pnpm-workspace.yaml")
                and absent(directory / ".pnpmfile.cjs"),
                "project pnpm configuration not pinned")
    config = runner_temp / "employee-pnpm-config"
    require(Path(os.environ.get("XDG_CONFIG_HOME", "")) == config,
            "pnpm config directory not pinned")
    empty = config / "empty.npmrc"
    require(Path(os.environ.get("NPM_CONFIG_USERCONFIG", "")) == empty
            and Path(os.environ.get("NPM_CONFIG_GLOBALCONFIG", "")) == empty,
            "pnpm rc configuration not pinned")
    require([item.name for item in children(config)] == ["empty.npmrc"],
            "pnpm config directory changed")
    require(read_regular(empty, 0)[0] == b"", "pnpm rc is not empty")
    allowed = {"npm_config_userconfig", "npm_config_globalconfig"}
    require(not {key.lower() for key in os.environ
                 if key.lower().startswith(("npm_config_", "pnpm_config_"))}
            - allowed, "pnpm environment configuration not pinned")


def lockfile_header_valid(content: bytes) -> bool:
    """Accept only the locked version line with LF or CRLF bytes."""
    return content.startswith((b"lockfileVersion: '9.0'\n",
                               b"lockfileVersion: '9.0'\r\n"))


def check_inputs(workspace: Path, reference: Path) -> None:
    require(reference != workspace
            and not reference.is_relative_to(workspace)
            and not workspace.is_relative_to(reference),
            "reference is not independent")
    require([item.name for item in children(reference)] ==
            ["node_modules", "package.json", "pnpm-lock.yaml"],
            "reference inputs are not isolated")
    for name, limit in (("package.json", 65536),
                        ("pnpm-lock.yaml", 2 * 1024 * 1024)):
        source = read_regular(workspace / name, limit)[0]
        copied = read_regular(reference / name, limit)[0]
        require(source == copied, "reference input differs")
        if name == "package.json":
            package = json.loads(source.decode("utf-8"))
            require(isinstance(package, dict)
                    and package.get("packageManager") == "pnpm@" + PNPM_VERSION,
                    "package manager identity differs")
        else:
            require(lockfile_header_valid(source),
                    "lockfile format differs")


def check_source_inputs(workspace: Path) -> None:
    package = json.loads(read_regular(workspace / "package.json", 65536)[0])
    require(isinstance(package, dict)
            and package.get("packageManager") == "pnpm@" + PNPM_VERSION,
            "package manager identity differs")
    require(lockfile_header_valid(
        read_regular(workspace / "pnpm-lock.yaml", 2 * 1024 * 1024)[0]),
            "lockfile format differs")


def package_roots(store: Path) -> set[Path]:
    roots: set[Path] = set()
    for unit in children(store):
        if unit.name == "lock.yaml":
            plain_info(unit, "regular")
            continue
        if unit.name == "node_modules":
            plain_info(unit, "directory")
            continue
        plain_info(unit, "directory")
        virtual_nm = unit / "node_modules"
        plain_info(virtual_nm, "directory")
        for entry in children(virtual_nm):
            if entry.name == ".bin":
                plain_info(entry, "directory")
                continue
            info = entry.lstat()
            if (stat.S_ISLNK(info.st_mode) or entry.is_junction()
                    or getattr(info, "st_file_attributes", 0) & REPARSE_POINT):
                continue  # pnpm dependency link; never traverse or mutate it.
            if entry.name.startswith("@"):
                plain_info(entry, "directory")
                candidates = children(entry)
            else:
                candidates = [entry]
            for candidate in candidates:
                info = candidate.lstat()
                if (stat.S_ISLNK(info.st_mode) or candidate.is_junction()
                        or getattr(info, "st_file_attributes", 0) & REPARSE_POINT):
                    continue  # Link target must have its own physical store root.
                plain_info(candidate, "directory")
                plain_info(candidate / "package.json", "regular")
                roots.add(candidate)
                require(len(roots) <= MAX_PACKAGES, "package count exceeded")
    require(bool(roots), "pnpm physical package roots absent")
    return roots


@dataclass
class Snapshot:
    roots: set[str]
    shape: set[str]
    shims: dict[str, bytes]
    shim_ids: dict[str, tuple[int, int, int, int]]
    nested: set[str]


@dataclass
class ReconcileTrace:
    stage: str = "reference_inputs"
    mutation_possible: bool = False
    byte_difference: str = "none"


def scan_store(workspace: Path) -> Snapshot:
    plain_info(workspace / "node_modules", "directory")
    store = workspace / "node_modules" / ".pnpm"
    plain_info(store, "directory")
    roots: set[str] = set()
    shape: set[str] = set()
    shims: dict[str, bytes] = {}
    shim_ids: dict[str, tuple[int, int, int, int]] = {}
    nested: set[str] = set()
    scanned = 0
    total_bytes = 0
    for package in sorted(package_roots(store)):
        roots.add(package.relative_to(store).as_posix())
        pending = [package]
        while pending:
            directory = pending.pop()
            for child in children(directory):
                scanned += 1
                require(scanned <= MAX_ENTRIES, "package scan budget exceeded")
                info = child.lstat()
                require(not stat.S_ISLNK(info.st_mode)
                        and not child.is_junction()
                        and not (getattr(info, "st_file_attributes", 0)
                                 & REPARSE_POINT),
                        "linked package content refused")
                if child.name == "node_modules":
                    require(child == package / "node_modules",
                            "unknown nested node_modules shape")
                    plain_info(child, "directory")
                    relative = child.relative_to(store).as_posix()
                    nested.add(relative)
                    shape.add(relative)
                    require([entry.name for entry in children(child)] == [".bin"],
                            "nested node_modules has unknown entries")
                    bin_dir = child / ".bin"
                    plain_info(bin_dir, "directory")
                    shape.add(bin_dir.relative_to(store).as_posix())
                    leaves = children(bin_dir)
                    require(bool(leaves), "empty generated bin directory")
                    for leaf in leaves:
                        leaf_info = plain_info(leaf, "regular")
                        require(leaf_info.st_nlink == 1,
                                "generated shim is hard-linked")
                        content, fingerprint = read_regular(leaf, MAX_SHIM_BYTES)
                        total_bytes += len(content)
                        require(total_bytes <= MAX_TOTAL_SHIM_BYTES,
                                "shim total byte budget exceeded")
                        key = leaf.relative_to(store).as_posix()
                        require(key not in shims, "duplicate generated shim")
                        shims[key] = content
                        shim_ids[key] = fingerprint
                        shape.add(key)
                        require(len(shims) <= MAX_SHIMS,
                                "shim count budget exceeded")
                    continue
                if stat.S_ISDIR(info.st_mode):
                    pending.append(child)
                else:
                    require(stat.S_ISREG(info.st_mode),
                            "unsupported package content kind")
    return Snapshot(roots, shape, shims, shim_ids, nested)


def root_literals(root: Path) -> tuple[bytes, ...]:
    text = str(root)
    if os.name == "nt":
        require(len(root.drive) == 2 and root.drive[1] == ":",
                "unsupported Windows root form")
        forward = text.replace("\\", "/")
        wsl = "/mnt/" + root.drive[0].lower() + forward[2:]
        extra = _extra_windows_root(root)
        require(extra is not None, "unsupported Windows root form")
        values = (text.encode("utf-8"), forward.encode("utf-8"),
                  wsl.encode("utf-8"), extra)
    else:
        values = (text.encode("utf-8"),)
    return values


def _replace_exact_extra_root(content: bytes, literal: bytes, marker: bytes) -> bytes:
    """Replace only a complete, root-derived Git Bash spelling."""
    left_boundary = b"='\" \t\r\n([{"
    right_boundary = b"/\\'\" \t\r\n)]};"
    result = bytearray()
    position = 0
    while True:
        found = content.find(literal, position)
        if found < 0:
            result.extend(content[position:])
            return bytes(result)
        end = found + len(literal)
        result.extend(content[position:found])
        if ((found == 0 or content[found - 1] in left_boundary)
                and (end == len(content) or content[end] in right_boundary)):
            result.extend(marker)
        else:
            result.extend(literal)
        position = end


def normalized(content: bytes, root: Path) -> bytes:
    result = content
    extra = _extra_windows_root(root) if os.name == "nt" else None
    for literal in sorted(root_literals(root), key=len, reverse=True):
        if literal == extra:
            result = _replace_exact_extra_root(
                result, literal, b"@CPA_PNPM_WORKSPACE_ROOT@")
        else:
            result = result.replace(literal, b"@CPA_PNPM_WORKSPACE_ROOT@")
    require(all(literal not in result for literal in root_literals(root)),
            "workspace root remained in shim")
    return result


BYTE_DIFFERENCE_CLASSES = (
    "line_endings_only", "terminal_eol_only",
    "extra_root_spelling_only", "other",
)


def _strict_crlf_variant(crlf: bytes, lf: bytes) -> bool:
    if b"\r\n" not in crlf or b"\r" in lf:
        return False
    without_pairs = crlf.replace(b"\r\n", b"")
    return (b"\r" not in without_pairs and b"\n" not in without_pairs
            and crlf.replace(b"\r\n", b"\n") == lf)


def _extra_windows_root(root: Path) -> bytes | None:
    drive = root.drive
    if (len(drive) != 2 or drive[1] != ":"
            or drive[0] not in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"):
        return None
    forward = str(root).replace("\\", "/")
    if not forward.startswith(drive + "/"):
        return None
    return ("/" + drive[0].lower() + forward[2:]).encode("utf-8")


def byte_difference_class(left: bytes, right: bytes,
                          workspace: Path, reference: Path) -> str:
    """Classify only already-read unequal bytes; never authorize a mutation."""
    if (_strict_crlf_variant(left, right)
            or _strict_crlf_variant(right, left)):
        return "line_endings_only"
    if any(left == right + suffix or right == left + suffix
           for suffix in (b"\n", b"\r\n")):
        return "terminal_eol_only"
    workspace_extra = _extra_windows_root(workspace)
    reference_extra = _extra_windows_root(reference)
    if workspace_extra is not None and reference_extra is not None:
        marker = b"@CPA_PNPM_WORKSPACE_ROOT@"
        if (workspace_extra in left or reference_extra in right) and (
                left.replace(workspace_extra, marker)
                == right.replace(reference_extra, marker)):
            return "extra_root_spelling_only"
    return "other"


def reconcile(workspace: Path, reference: Path,
              trace: ReconcileTrace | None = None) -> tuple[int, int]:
    trace = trace if trace is not None else ReconcileTrace()
    trace.stage = "reference_inputs"
    trace.mutation_possible = False
    trace.byte_difference = "none"
    check_inputs(workspace, reference)
    trace.stage = "source_scan"
    actual = scan_store(workspace)
    trace.stage = "reference_scan"
    expected = scan_store(reference)
    trace.stage = "graph"
    require(actual.roots == expected.roots, "pnpm physical graph differs")
    trace.stage = "shape"
    require(actual.shape == expected.shape
            and actual.shims.keys() == expected.shims.keys(),
            "pnpm nested entry set differs")
    trace.stage = "byte_compare"
    for key in sorted(actual.shims):
        left = normalized(actual.shims[key], workspace)
        right = normalized(expected.shims[key], reference)
        if left != right:
            try:
                trace.byte_difference = byte_difference_class(
                    left, right, workspace, reference)
            except Exception:
                trace.byte_difference = "other"
        require(left == right, "generated shim bytes differ")

    store = workspace / "node_modules" / ".pnpm"
    # All graph and byte comparisons above complete before the first unlink.
    trace.stage = "pre_unlink"
    for key in sorted(actual.shims):
        leaf = store / Path(key)
        plain_info(leaf.parent.parent, "directory")
        plain_info(leaf.parent, "directory")
        before = plain_info(leaf, "regular")
        require(before.st_nlink == 1
                and identity(before) == actual.shim_ids[key]
                and read_regular(leaf, MAX_SHIM_BYTES)[0] == actual.shims[key],
                "generated shim changed before removal")
        trace.stage = "unlink"
        trace.mutation_possible = True
        leaf.unlink()
        trace.stage = "pre_unlink"
    trace.stage = "remove_dirs"
    for key in sorted(actual.nested):
        nested = store / Path(key)
        bin_dir = nested / ".bin"
        plain_info(bin_dir, "directory")
        trace.mutation_possible = True
        bin_dir.rmdir()
        plain_info(nested, "directory")
        nested.rmdir()
    trace.stage = "post_scan"
    after = scan_store(workspace)
    require(after.roots == actual.roots
            and not after.nested and not after.shims,
            "generated nested directory regrew")
    return len(actual.nested), len(actual.shims)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("preflight", "pin-tool",
                                             "verify-tool", "reconcile",
                                             "check-clean"))
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--reference", type=Path)
    parser.add_argument("--runner-temp", type=Path, required=True)
    parser.add_argument("--pnpm-version", required=True)
    args = parser.parse_args()
    phase = "runner_root"
    try:
        runner_temp = absolute_plain(args.runner_temp)
        phase = "workspace_root"
        workspace = absolute_plain(args.workspace)
        phase = "config"
        check_config(workspace, runner_temp, args.pnpm_version)
        phase = "source_inputs"
        check_source_inputs(workspace)
        phase = "command_contract"
        if args.command == "preflight":
            require(args.reference is None, "unexpected reference")
            print("pnpm configuration preflight accepted")
        elif args.command == "pin-tool":
            require(args.reference is None, "unexpected reference")
            phase = "pin_tool"
            pin_tool(runner_temp)
            print("verified pnpm 11.25.0 tool archive and extracted payload")
        elif args.command == "verify-tool":
            require(args.reference is None, "unexpected reference")
            phase = "verify_tool"
            verify_tool(runner_temp)
            print("verified pnpm 11.25.0 tool payload")
        elif args.command == "reconcile":
            require(args.reference is not None, "reference absent")
            phase = "reference_root"
            reference = absolute_plain(args.reference)
            require(reference.parent == runner_temp
                    and reference.name == "employee-pnpm-reference",
                    "reference location not pinned")
            phase = "reconcile"
            trace = ReconcileTrace()
            directories, leaves = reconcile(workspace, reference, trace)
            print(f"verified generated nested dirs={directories} shims={leaves}")
        else:
            require(args.reference is None, "unexpected reference")
            phase = "check_clean"
            snapshot = scan_store(workspace)
            require(not snapshot.nested and not snapshot.shims,
                    "generated nested directory regrew")
            print("verified generated nested dirs=0 shims=0")
    except Exception as error:
        difference = (
            f" byte_difference={trace.byte_difference}"
            if phase == "reconcile" and trace.stage == "byte_compare"
            and refusal_reason(error) == "shim_bytes_mismatch"
            and trace.byte_difference in BYTE_DIFFERENCE_CLASSES else "")
        detail = (f" stage={trace.stage} reason={refusal_reason(error)}"
                  f" mutation_possible={str(trace.mutation_possible).lower()}"
                  f"{difference}"
                  if phase == "reconcile" else "")
        print("pnpm generated-shim verification refused "
              f"phase={phase} category={refusal_category(error)}{detail}",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
