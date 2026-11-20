#!/usr/bin/env python3
"""Collect and verify full third-party notice texts for the employee client.

Provenance: independently authored for CPA Cloud using Python's standard
library, Cargo.lock/Cargo metadata, and pnpm's lockfile/virtual-store formats.
No application or upstream implementation code is used.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import os
import posixpath
import re
import stat
import subprocess
import sys
import tarfile
import tomllib
import urllib.request
from html.parser import HTMLParser
from pathlib import Path, PurePosixPath
from urllib.parse import quote, urlparse

SCHEMA = 1
CRATES_IO = "registry+https://github.com/rust-lang/crates.io-index"
FIRST_PARTY_CARGO = {
    ("cpa-cloud-employee-config", "0.1.0"),
    ("employee-config-core", "0.1.0"),
}
EXTERNAL_CARGO = {
    ("dlopen2", "0.8.2"): {
        "repository": "https://github.com/OpenByteDev/dlopen2",
        "vcs": "cc80e4a0a90d499b677fdf7743699b4b3a43a989",
        "path": "dlopen2", "license": "MIT",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/OpenByteDev/dlopen2/"
            "cc80e4a0a90d499b677fdf7743699b4b3a43a989/LICENSE",
            "39fa265207450e77c62e90c5594a06c085b655d8374c7ced4bf7894b6bd95dd2",
            1184)],
    },
    ("dlopen2_derive", "0.4.3"): {
        "repository": "https://github.com/OpenByteDev/dlopen2",
        "vcs": "cc80e4a0a90d499b677fdf7743699b4b3a43a989",
        "path": "dlopen2-derive", "license": "MIT",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/OpenByteDev/dlopen2/"
            "cc80e4a0a90d499b677fdf7743699b4b3a43a989/LICENSE",
            "39fa265207450e77c62e90c5594a06c085b655d8374c7ced4bf7894b6bd95dd2",
            1184)],
    },
    ("cesu8", "1.1.0"): {
        "repository": "https://github.com/emk/cesu8-rs",
        "license": "Apache-2.0/MIT",
        "files": [
            ("LICENSE-APACHE",
             "https://raw.githubusercontent.com/rust-lang/rust/"
             "db2939409db26ab4904372c82492cd3488e4c44e/LICENSE-APACHE",
             "a60eea817514531668d7e00765731449fe14d059d3249e0bc93b36de45f759f2",
             10847),
            ("LICENSE-MIT",
             "https://raw.githubusercontent.com/rust-lang/rust/"
             "db2939409db26ab4904372c82492cd3488e4c44e/LICENSE-MIT",
             "0621878e61f0d0fda054bcbe02df75192c28bde1ecc8289cbd86aeba2dd72720",
             1071),
        ],
    },
    ("block2", "0.6.2"): {
        "repository": "https://github.com/madsmtm/objc2",
        "vcs": "b4167b582b2f75f9a1be75495c41b765344fd03c",
        "path": "crates/block2", "license": "MIT",
        "files": [("LICENSE-MIT.txt",
            "https://raw.githubusercontent.com/spdx/license-list-data/"
            "ab9b0904b43db6f4b67431efd8821feb821f1c8b/text/MIT.txt",
            "b05785f9f18e6716bab63424b11454513b9943a222595b70411009202fc592b5",
            1078)],
    },
    ("dispatch2", "0.3.1"): {
        "repository": "https://github.com/madsmtm/objc2",
        "vcs": "8852b424193ca41602281b3d7540d7c8ed51e49a",
        "path": "crates/dispatch2", "license": "Zlib OR Apache-2.0 OR MIT",
        "files": [("LICENSE-MIT.txt",
            "https://raw.githubusercontent.com/spdx/license-list-data/"
            "ab9b0904b43db6f4b67431efd8821feb821f1c8b/text/MIT.txt",
            "b05785f9f18e6716bab63424b11454513b9943a222595b70411009202fc592b5",
            1078)],
    },
    ("alloc-stdlib", "0.3.0"): {
        "repository": "https://github.com/dropbox/rust-alloc-no-stdlib",
        "vcs": "0a81fd6928ea3b33c8cd484aa4575d50ffb98012",
        "path": "alloc-stdlib", "license": "BSD-3-Clause",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/dropbox/rust-alloc-no-stdlib/"
            "0a81fd6928ea3b33c8cd484aa4575d50ffb98012/LICENSE",
            "c0c56f26d9c051cac4d200c34c84e7ae9aaa853e01a982a1df08b09931e518ae",
            1483)],
    },
    ("defmt-parser", "1.0.0"): {
        "repository": "https://github.com/knurling-rs/defmt",
        "vcs": "4a8cdb44891ed57b8ff5a023b6bec7137c48708f",
        "path": "parser", "license": "MIT OR Apache-2.0",
        "files": [
            ("LICENSE-MIT",
             "https://raw.githubusercontent.com/knurling-rs/defmt/"
             "4a8cdb44891ed57b8ff5a023b6bec7137c48708f/LICENSE-MIT",
             "2710a622a896bba67356913d4d0492cab5465f61b2ecce6d880aeb483834fb50",
             1053),
            ("LICENSE-APACHE",
             "https://raw.githubusercontent.com/knurling-rs/defmt/"
             "4a8cdb44891ed57b8ff5a023b6bec7137c48708f/LICENSE-APACHE",
             "8173d5c29b4f956d532781d2b86e4e30f83e6b7878dce18c919451d6ba707c90",
             10850),
        ],
    },
    ("selectors", "0.38.0"): {
        "repository": "https://github.com/servo/stylo",
        "vcs": "572ecba2d1600e7c3d490586692a209faf703baa",
        "path": "selectors", "license": "MPL-2.0",
        "files": [("MPL-2.0.txt",
            "https://www.mozilla.org/media/MPL/2.0/index.txt",
            "3f3d9e0024b1921b067d6f7f88deB4a60cbe7a78e76c64e3f1d7fC3b779b9d04".lower(),
            16726)],
    },
    ("webview2-com", "0.39.1"): {
        "repository": "https://github.com/wravery/webview2-rs",
        "vcs": "edc2caf886175ccaebe86078c9cfe1ae2a187328",
        "path": "crates/webview2-com", "license": "MIT",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/wravery/webview2-rs/"
            "edc2caf886175ccaebe86078c9cfe1ae2a187328/LICENSE",
            "0dcf41516e608bbcb6cdc5229feb7b86fe4a643b85e7df251133c93408fdac73",
            1067)],
    },
    ("webview2-com-macros", "0.8.1"): {
        "repository": "https://github.com/wravery/webview2-rs",
        "vcs": "dffa41a8a46d3f5565eefbff2de57d38d399f158",
        "path": "crates/callback-macros", "license": "MIT",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/wravery/webview2-rs/"
            "dffa41a8a46d3f5565eefbff2de57d38d399f158/LICENSE",
            "0dcf41516e608bbcb6cdc5229feb7b86fe4a643b85e7df251133c93408fdac73",
            1067)],
    },
    ("webview2-com-sys", "0.39.1"): {
        "repository": "https://github.com/wravery/webview2-rs",
        "vcs": "edc2caf886175ccaebe86078c9cfe1ae2a187328",
        "path": "crates/bindings", "license": "MIT",
        "files": [("LICENSE",
            "https://raw.githubusercontent.com/wravery/webview2-rs/"
            "edc2caf886175ccaebe86078c9cfe1ae2a187328/LICENSE",
            "0dcf41516e608bbcb6cdc5229feb7b86fe4a643b85e7df251133c93408fdac73",
            1067)],
    },
}
VENDORED_CARGO = {
    ("dlopen2", "0.8.2", "LICENSE"):
        "third_party/notice-fallbacks/dlopen2-rs-LICENSE.txt",
    ("dlopen2_derive", "0.4.3", "LICENSE"):
        "third_party/notice-fallbacks/dlopen2-rs-LICENSE.txt",
    ("cesu8", "1.1.0", "LICENSE-APACHE"):
        "third_party/notice-fallbacks/cesu8-1.1.0-LICENSE-APACHE.txt",
    ("cesu8", "1.1.0", "LICENSE-MIT"):
        "third_party/notice-fallbacks/cesu8-1.1.0-LICENSE-MIT.txt",
    ("block2", "0.6.2", "LICENSE-MIT.txt"):
        "third_party/notice-fallbacks/block2-0.6.2-LICENSE-MIT.txt",
    ("dispatch2", "0.3.1", "LICENSE-MIT.txt"):
        "third_party/notice-fallbacks/block2-0.6.2-LICENSE-MIT.txt",
    ("alloc-stdlib", "0.3.0", "LICENSE"):
        "third_party/notice-fallbacks/alloc-stdlib-0.3.0-LICENSE.txt",
    ("defmt-parser", "1.0.0", "LICENSE-MIT"):
        "third_party/notice-fallbacks/defmt-parser-1.0.0-LICENSE-MIT.txt",
    ("defmt-parser", "1.0.0", "LICENSE-APACHE"):
        "third_party/notice-fallbacks/defmt-parser-1.0.0-LICENSE-APACHE.txt",
    ("selectors", "0.38.0", "MPL-2.0.txt"):
        "third_party/notice-fallbacks/selectors-0.38.0-MPL-2.0.txt",
    ("webview2-com", "0.39.1", "LICENSE"):
        "third_party/notice-fallbacks/webview2-rs-LICENSE.txt",
    ("webview2-com-macros", "0.8.1", "LICENSE"):
        "third_party/notice-fallbacks/webview2-rs-LICENSE.txt",
    ("webview2-com-sys", "0.39.1", "LICENSE"):
        "third_party/notice-fallbacks/webview2-rs-LICENSE.txt",
}
EXTERNAL_OUTPUT_NAMES = {
    # The URL's MPL-2.0.txt is the full license text for this exact crate.
    # Give its bundled copy a license basename without changing its origin.
    ("selectors", "0.38.0", "MPL-2.0.txt"): "LICENSE-MPL-2.0.txt",
}
STYLO_DECLARATION = "third_party/notice-fallbacks/selectors-0.38.0-STYLO-README.md"
BLOCK2_DECLARATION = "third_party/notice-fallbacks/block2-0.6.2-UPSTREAM-LICENSE.md"
DISPATCH2_ARCHIVE_SHA256 = "1e0e367e4e7da84520dedcac1901e4da967309406d1e51017ae1abfb97adbd38"
DISPATCH2_ARCHIVE_BYTES = 55299
DLOPEN2_ARCHIVES = {
    ("dlopen2", "0.8.2"):
        ("5e2c5bd4158e66d1e215c49b837e11d62f3267b30c92f1d171c4d3105e3dc4d4", 27695),
    ("dlopen2_derive", "0.4.3"):
        ("0fbbb781877580993a8707ec48672673ec7b81eeba04cfd2310bd28c08e47c8f", 7032),
}
VENDORED_FILES = set(VENDORED_CARGO.values()) | {STYLO_DECLARATION, BLOCK2_DECLARATION}
require_vendor_count = 11
if len(VENDORED_FILES) != require_vendor_count:
    raise RuntimeError("vendored notice source map does not cover eleven files")
CESU8_ARCHIVE_SHA256 = "6d43a04d8753f35258c91f8ec639f792891f748a1edbd759cf1dcea3382ad83c"
CESU8_ARCHIVE_BYTES = 10555
CESU8_RELEASE = "f18962a9de5189f21600b7ff2898845157df4447"
CESU8_MEMBERS = {
    ".gitignore": "4fffb2f89cbd8f2169ce9914bd16bd43785bb368",
    ".travis.yml": "0ea001b616879a4339ed8ebe4c2f89d7472830d3",
    "COPYRIGHT-RUST.txt": "52039ea7e5734462f2989ef7f30ebbaf80fef949",
    "Cargo.toml": "0d8bd9a572fc68998df4709fce2db857e77239bc",
    "README.md": "161f8652a254442a595af73d1563bcfde2579193",
    "src/lib.rs": "7fac5ffbe9a60c9604e8e3d990cc86b8bfbee249",
    "src/unicode.rs": "a236d310d247939111a5df2f8a8e83f002a4b9bc",
}
MAX_TEXT = 16 * 1024 * 1024
MAX_TOTAL = 256 * 1024 * 1024
MAX_ARCHIVE = 64 * 1024 * 1024
MAX_METADATA = 2 * 1024 * 1024
LICENSE_NAME = re.compile(
    r"^(?:LICEN[CS]E|COPYING|NOTICE|PATENTS|COPYRIGHT|UNLICENSE|"
    r"THIRD[_ -]?PARTY[_ -]?(?:LICEN[CS]ES?|NOTICES?))"
    r"(?:[._ -].*)?$",
    re.IGNORECASE,
)
AUTHORIZATION_NAME = re.compile(
    r"^(?:LICEN[CS]E|COPYING|UNLICENSE)(?:[._ -].*)?$",
    re.IGNORECASE,
)
HEX64 = re.compile(r"^[0-9a-f]{64}$")
PNPM_ENTRY = re.compile(r"^  (?:'([^']+)'|([^':\s][^:]*)):$")
SNAPSHOT_ENTRY = re.compile(r"^  (?:'([^']+)'|([^':\s][^:]*)):(?: \{\})?$")
INTEGRITY = re.compile(r"\bintegrity:\s*(sha512-[A-Za-z0-9+/=]+)")


class NoticeError(Exception):
    pass


def require(ok: bool, message: str) -> None:
    if not ok:
        raise NoticeError(message)


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def no_reparse(path: Path) -> os.stat_result:
    """Inspect the directory entry itself, including Windows reparse attributes."""
    info = path.lstat()
    require(not stat.S_ISLNK(info.st_mode) and not path.is_symlink()
            and not path.is_junction()
            and not (getattr(info, "st_file_attributes", 0)
                     & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)),
            "reparse path refused")
    return info


def checked_descendant(root: Path, relative: Path | str,
                       directory: bool) -> tuple[Path, os.stat_result]:
    """Check each component below an allowed canonical root before use."""
    root = root.resolve(strict=True)
    relative = Path(relative)
    require(relative.parts and not relative.is_absolute() and not relative.drive
            and all(part not in ("", ".", "..") for part in relative.parts),
            "unsafe descendant path")
    path = root
    for index, part in enumerate(relative.parts):
        path = path / part
        info = no_reparse(path)
        if index + 1 < len(relative.parts):
            require(stat.S_ISDIR(info.st_mode), "non-directory path component")
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode),
            "installed path has wrong type")
    return path, info


def walk_error(exc: OSError) -> None:
    raise NoticeError("directory inventory traversal failed") from exc


def raw(path: Path) -> bytes:
    info = no_reparse(path)
    require(stat.S_ISREG(info.st_mode), f"not a regular file: {path}")
    require(0 < info.st_size <= MAX_TEXT, f"empty or oversized text: {path}")
    data = path.read_bytes()
    require(len(data) == info.st_size, f"file changed while reading: {path}")
    try:
        data.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise NoticeError(f"notice is not UTF-8: {path}") from exc
    require(b"\x00" not in data, f"notice contains NUL: {path}")
    return data


def source_file(path: Path, root: Path) -> bytes:
    try:
        relative = path.relative_to(root)
    except ValueError as exc:
        raise NoticeError("path escapes package") from exc
    checked, _ = checked_descendant(root, relative, directory=False)
    return raw(checked)


def json_bytes(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"),
                       ensure_ascii=False) + "\n").encode("utf-8")


def read_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def git_blob(workspace: Path, name: str) -> bytes:
    prefix_proc = subprocess.run(
        ["git", "-C", str(workspace), "rev-parse", "--show-prefix"],
        capture_output=True, text=True, check=False,
    )
    require(prefix_proc.returncode == 0, "source Git prefix unavailable")
    prefix = prefix_proc.stdout.strip().replace("\\", "/")
    result = subprocess.run(
        ["git", "-C", str(workspace), "show", f"HEAD:{prefix}{name}"],
        capture_output=True, check=False,
    )
    require(result.returncode == 0 and bool(result.stdout),
            f"committed source file unavailable: {name}")
    return result.stdout


def safe_part(value: str) -> str:
    require(value and value not in (".", ".."), f"invalid package name: {value}")
    encoded = quote(value, safe="-_.")
    require("/" not in encoded and "\\" not in encoded, "unsafe output component")
    return encoded


def valid_license(value: object, license_file: object = None) -> str:
    if isinstance(value, str):
        label = value.strip()
        if label and label.upper() not in ("UNKNOWN", "UNLICENSED", "NONE"):
            return label
    require(isinstance(license_file, str) and license_file.strip(),
            "license metadata and license_file both absent")
    return "LICENSE_FILE"


def has_authorization_text(texts: dict[str, bytes]) -> bool:
    """A supplemental notice cannot stand in for a full license file."""
    return any(AUTHORIZATION_NAME.fullmatch(PurePosixPath(name).name)
               and bool(content.strip()) for name, content in texts.items())


def report_nested_node_modules(installed_root: Path, current: str,
                               archived_paths: set[str],
                               archived_files: set[str]) -> None:
    """Emit fixed-size structural counts without naming installed children."""
    item_limit = 128
    depth_limit = 4
    count_limit = 65535
    nested = Path(current) / "node_modules"
    try:
        relative = nested.relative_to(installed_root).as_posix()
        prefix = relative + "/"
        members = sum(path == relative or path.startswith(prefix)
                      for path in archived_paths)
        files = sum(path == relative or path.startswith(prefix)
                    for path in archived_files)
        root_info = nested.lstat()
        mode = root_info.st_mode
        junction = nested.is_junction()
        symlink = stat.S_ISLNK(mode)
        ordinary_dir = stat.S_ISDIR(mode) and not junction and not symlink
        layers = [{"regular": 0, "directory": 0, "symlink": 0,
                   "junction": 0, "other": 0, "overflow": 0}
                  for _ in range(depth_limit)]
        scanned = 0
        budget_overflow = 0
        if ordinary_dir:
            pending = [(nested, 0, (root_info.st_dev, root_info.st_ino))]
            offset = 0
            while offset < len(pending) and not budget_overflow:
                directory, level, identity = pending[offset]
                offset += 1
                check = directory.lstat()
                if (not stat.S_ISDIR(check.st_mode)
                        or directory.is_junction()
                        or (check.st_dev, check.st_ino) != identity):
                    raise NoticeError("nested node_modules diagnostic changed")
                with os.scandir(directory) as entries:
                    for entry in entries:
                        if scanned == item_limit:
                            layers[level]["overflow"] = 1
                            budget_overflow = 1
                            break
                        scanned += 1
                        child = Path(entry.path)
                        if child.is_junction():
                            kind = "junction"
                        elif entry.is_symlink():
                            kind = "symlink"
                        else:
                            info = child.lstat()
                            if stat.S_ISREG(info.st_mode):
                                kind = "regular"
                            elif stat.S_ISDIR(info.st_mode):
                                kind = "directory"
                            else:
                                kind = "other"
                        layers[level][kind] += 1
                        if kind == "directory":
                            if level + 1 < depth_limit:
                                pending.append((child, level + 1,
                                                (info.st_dev, info.st_ino)))
                            else:
                                layers[level]["overflow"] = 1
    except (OSError, ValueError):
        raise NoticeError("nested node_modules diagnostic failed") from None
    fields = [f"archive_members={min(members, count_limit)}",
              f"archive_files={min(files, count_limit)}",
              f"archive_overflow={int(members > count_limit or files > count_limit)}",
              f"ordinary_dir={int(ordinary_dir)}",
              f"symlink={int(symlink)}", f"junction={int(junction)}",
              f"entry_limit={item_limit}", f"depth_limit={depth_limit}",
              f"scanned={scanned}", f"budget_overflow={budget_overflow}"]
    for depth, counts in enumerate(layers, 1):
        for kind in ("regular", "directory", "symlink", "junction",
                     "other", "overflow"):
            fields.append(f"d{depth}_{kind}={counts[kind]}")
    print("nested_node_modules_diagnostic " + " ".join(fields),
          file=sys.stderr)


def archive_files(data: bytes, prefix: str, explicit: set[str] | None = None,
                  installed_root: Path | None = None,
                  cargo_source: bool = False,
                  allow_no_notice: bool = False,
                  ) -> tuple[dict[str, bytes], bytes]:
    """Read notice members directly from already integrity-checked tar bytes."""
    require(0 < len(data) <= MAX_ARCHIVE, "archive exceeds size budget")
    found: dict[str, bytes] = {}
    package_json = b""
    explicit = explicit or set()
    archived_paths: set[str] = set()
    archived_files: set[str] = set()
    if installed_root is not None:
        installed_root = installed_root.resolve(strict=True)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        for member in archive:
            parts = PurePosixPath(member.name).parts
            require(parts and not member.name.startswith("/")
                    and ".." not in parts and "\\" not in member.name,
                    f"unsafe archive member: {member.name}")
            require(parts[0] == prefix,
                    f"archive member outside package root: {member.name}")
            if len(parts) < 2:
                require(member.isdir(),
                        f"archive root is not directory: {member.name}")
                continue
            relative = PurePosixPath(*parts[1:]).as_posix()
            require(relative not in archived_paths,
                    f"duplicate archive path: {member.name}")
            archived_paths.add(relative)
            basename = parts[-1]
            is_notice = (bool(LICENSE_NAME.fullmatch(basename))
                         or "licenses" in (p.lower() for p in parts[1:-1])
                         or relative in explicit)
            is_manifest = relative == "package.json"
            if not is_notice and not is_manifest and installed_root is None:
                continue
            if member.isdir():
                require(not is_notice, f"notice is a directory: {member.name}")
                if installed_root is not None:
                    checked_descendant(installed_root, relative, directory=True)
                continue
            require(member.isfile(),
                    f"unsupported archive member type: {member.name}")
            archived_files.add(relative)
            require(0 <= member.size <= MAX_ARCHIVE,
                    f"archive member oversized: {member.name}")
            if is_notice or is_manifest:
                require(0 < member.size <= MAX_TEXT,
                        f"notice empty or oversized: {member.name}")
            stream = archive.extractfile(member)
            require(stream is not None, f"archive member unavailable: {member.name}")
            content = stream.read(MAX_ARCHIVE + 1)
            require(len(content) == member.size,
                    f"archive member size mismatch: {member.name}")
            if installed_root is not None:
                installed_path, info = checked_descendant(
                    installed_root, relative, directory=False)
                require(info.st_size == member.size,
                        f"installed package file size differs: {relative}")
                require(sha(installed_path.read_bytes()) == sha(content),
                        f"installed package file differs from archive: {relative}")
            if is_manifest:
                require(not package_json, f"duplicate package.json member: {prefix}")
                package_json = content
            if is_notice:
                require(relative not in found,
                        f"duplicate notice member: {relative}")
                try:
                    content.decode("utf-8")
                except UnicodeDecodeError as exc:
                    raise NoticeError(f"notice is not UTF-8: {member.name}") from exc
                require(b"\x00" not in content,
                        f"notice contains NUL: {member.name}")
                found[relative] = content
    require(has_authorization_text(found) or allow_no_notice,
            f"no license text in archive: {prefix}")
    require(explicit.issubset(found), f"declared license_file absent: {prefix}")
    if installed_root is not None:
        actual_paths: set[str] = set()
        for current, dirs, files in os.walk(
                installed_root, topdown=True, onerror=walk_error,
                followlinks=False):
            current_relative = Path(current).relative_to(installed_root)
            if current_relative.parts:
                checked_descendant(installed_root, current_relative,
                                   directory=True)
            if not cargo_source:
                if "node_modules" in dirs:
                    report_nested_node_modules(installed_root, current,
                                               archived_paths, archived_files)
                require("node_modules" not in dirs,
                        "nested node_modules in installed package")
            for directory in dirs:
                relative = (Path(current) / directory).relative_to(installed_root)
                checked_descendant(installed_root, relative, directory=True)
            for file in files:
                relative = (Path(current) / file).relative_to(installed_root)
                checked_descendant(installed_root, relative, directory=False)
                actual_paths.add(relative.as_posix())
        extras = actual_paths - archived_files
        if cargo_source:
            extras -= {".cargo-ok", ".cargo-checksum.json"}
        require(not extras, f"extra installed package files: {sorted(extras)[:5]}")
    return found, package_json


def npm_archive_root(data: bytes, name: str) -> str:
    roots: set[str] = set()
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        for member in archive:
            parts = PurePosixPath(member.name).parts
            require(parts and not member.name.startswith("/")
                    and ".." not in parts and "\\" not in member.name,
                    f"unsafe npm archive member: {member.name}")
            roots.add(parts[0])
    require(len(roots) == 1, f"npm archive has multiple roots: {name}")
    root = next(iter(roots))
    require(root in ("package", name.rsplit("/", 1)[-1]),
            f"unexpected npm archive root: {name}: {root}")
    return root


def archive_member(data: bytes, member_name: str) -> bytes:
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        matches = [m for m in archive if m.name == member_name]
        require(len(matches) == 1 and matches[0].isfile()
                and 0 < matches[0].size <= MAX_TEXT,
                f"archive provenance member unavailable: {member_name}")
        stream = archive.extractfile(matches[0])
        require(stream is not None, f"archive member unreadable: {member_name}")
        content = stream.read(MAX_TEXT + 1)
        require(len(content) == matches[0].size,
                f"archive member size changed: {member_name}")
        return content


def exact_regular_git_members(data: bytes, root: str,
                              expected: dict[str, str]) -> dict[str, bytes]:
    """Bind every member of one legacy crate to reviewed Git blob identities."""
    found: dict[str, bytes] = {}
    try:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
            members = archive.getmembers()
            require(len(members) == len(expected),
                    "legacy Cargo archive member count differs")
            for member in members:
                prefix = root + "/"
                require(member.isfile() and member.name.startswith(prefix),
                        "legacy Cargo archive member type or root differs")
                relative = member.name[len(prefix):]
                require(relative in expected and relative not in found and
                        0 < member.size <= MAX_METADATA,
                        "legacy Cargo archive member identity differs")
                stream = archive.extractfile(member)
                require(stream is not None, "legacy Cargo archive member unreadable")
                content = stream.read(member.size + 1)
                require(len(content) == member.size,
                        "legacy Cargo archive member length differs")
                git_blob_sha = hashlib.sha1(
                    b"blob " + str(len(content)).encode("ascii") + bytes([0]) +
                    content, usedforsecurity=False,
                ).hexdigest()
                require(git_blob_sha == expected[relative],
                        "legacy Cargo archive member Git blob differs")
                found[relative] = content
    except (tarfile.TarError, OSError) as exc:
        raise NoticeError("legacy Cargo archive unreadable") from exc
    require(set(found) == set(expected), "legacy Cargo archive inventory differs")
    return found


def cesu8_legacy_origin(data: bytes) -> dict[str, bytes]:
    require(len(data) == CESU8_ARCHIVE_BYTES and
            sha(data) == CESU8_ARCHIVE_SHA256,
            "cesu8 legacy archive checksum or size differs")
    members = exact_regular_git_members(data, "cesu8-1.1.0", CESU8_MEMBERS)
    require(b'license = "Apache-2.0/MIT"' in members["Cargo.toml"] and
            b'repository = "https://github.com/emk/cesu8-rs"' in
            members["Cargo.toml"] and
            b"## License" in members["README.md"] and
            b"LICENSE-RUST.txt" in members["README.md"] and
            b"dual-licensed under Apache 2.0 and MIT" in
            members["COPYRIGHT-RUST.txt"],
            "cesu8 legacy source license declaration differs")
    return members


def vendored_text(workspace: Path, name: str, digest: str,
                  length: int) -> bytes:
    require(name in VENDORED_FILES, f"unapproved vendored notice path: {name}")
    committed = git_blob(workspace, name)
    require(len(committed) == length and sha(committed) == digest,
            f"vendored committed notice differs from pinned source: {name}")
    return committed


class RegistryRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        parsed = urlparse(newurl)
        require(parsed.scheme == "https" and parsed.hostname == "registry.npmjs.org"
                and parsed.username is None and parsed.password is None,
                f"npm registry attempted off-host redirect: {newurl}")
        return super().redirect_request(request, fp, code, msg, headers, newurl)


def https_registry(url: str, limit: int) -> bytes:
    parsed = urlparse(url)
    require(parsed.scheme == "https" and parsed.hostname == "registry.npmjs.org"
            and parsed.username is None and parsed.password is None,
            f"unapproved npm registry URL: {url}")
    request = urllib.request.Request(url, headers={
        "Accept": "application/json", "User-Agent": "CPA-Cloud-notice-collector/1"
    })
    with urllib.request.build_opener(RegistryRedirect).open(
            request, timeout=60) as response:
        final = urlparse(response.geturl())
        require(final.scheme == "https" and final.hostname == "registry.npmjs.org",
                "npm registry redirected off approved host")
        data = response.read(limit + 1)
    require(0 < len(data) <= limit, f"registry response exceeds budget: {url}")
    return data


def npm_tarball(name: str, version: str, integrity: str) -> bytes:
    metadata_url = ("https://registry.npmjs.org/" +
                    quote(name, safe="@") + "/" + quote(version, safe=""))
    metadata_data = https_registry(metadata_url, MAX_METADATA)
    metadata = json.loads(metadata_data)
    require(metadata.get("name") == name and metadata.get("version") == version,
            f"npm registry identity mismatch: {name}@{version}")
    dist = metadata.get("dist")
    require(isinstance(dist, dict) and dist.get("integrity") == integrity,
            f"npm registry integrity differs from lock: {name}@{version}")
    require(integrity.startswith("sha512-"),
            f"unsupported npm integrity: {name}@{version}")
    digest = base64.b64decode(integrity[7:], validate=True)
    require(len(digest) == 64, f"invalid npm sha512: {name}@{version}")
    tarball = https_registry(dist.get("tarball", ""), MAX_ARCHIVE)
    require(hashlib.sha512(tarball).digest() == digest,
            f"npm archive integrity mismatch: {name}@{version}")
    return tarball


def cargo_lock(path: Path) -> dict[tuple[str, str], str]:
    parsed = tomllib.loads(path.read_text(encoding="utf-8"))
    require(parsed.get("version") == 4, "unsupported Cargo.lock version")
    result: dict[tuple[str, str], str] = {}
    local: set[tuple[str, str]] = set()
    for pkg in parsed["package"]:
        source = pkg.get("source")
        if source is None:
            local.add((pkg["name"], pkg["version"]))
            continue
        require(source == CRATES_IO, f"unsupported Cargo source: {source}")
        name, version, checksum = pkg["name"], pkg["version"], pkg.get("checksum")
        require(isinstance(checksum, str) and bool(HEX64.fullmatch(checksum)),
                f"missing Cargo checksum: {name}@{version}")
        key = (name, version)
        require(key not in result, f"duplicate Cargo package: {key}")
        result[key] = checksum
    require(bool(result), "Cargo.lock has no registry packages")
    require(local == FIRST_PARTY_CARGO,
            f"unreviewed local Cargo package: {sorted(local ^ FIRST_PARTY_CARGO)}")
    return result


def pnpm_lock(path: Path) -> dict[tuple[str, str], str]:
    text = path.read_text(encoding="utf-8")
    require(text.startswith("lockfileVersion: '9.0'\n"),
            "unsupported pnpm lock version")
    lines = text.splitlines()
    try:
        start = lines.index("packages:") + 1
        end = lines.index("snapshots:")
    except ValueError as exc:
        raise NoticeError("pnpm lock packages/snapshots section absent") from exc
    result: dict[tuple[str, str], str] = {}
    current: str | None = None
    integrity: str | None = None

    def finish() -> None:
        if current is None:
            return
        require(integrity is not None, f"pnpm integrity absent: {current}")
        at = current.rfind("@")
        require(at > 0, f"invalid pnpm package key: {current}")
        key = (current[:at], current[at + 1:])
        require(key not in result, f"duplicate pnpm package: {key}")
        result[key] = integrity

    for line in lines[start:end]:
        if not line.strip():
            continue
        if line.startswith("  ") and not line.startswith("    "):
            finish()
            match = PNPM_ENTRY.fullmatch(line)
            require(match is not None, f"unsupported pnpm package key: {line}")
            current, integrity = match.group(1) or match.group(2), None
        elif current is not None:
            match = INTEGRITY.search(line)
            if match:
                require(integrity is None, f"duplicate pnpm integrity: {current}")
                integrity = match.group(1)
    finish()
    require(bool(result), "pnpm lock has no packages")
    return result


def cargo_packages(workspace: Path, locked: dict[tuple[str, str], str]) -> list[dict]:
    proc = subprocess.run(
        ["cargo", "metadata", "--locked", "--offline", "--format-version", "1",
         "--manifest-path", str(workspace / "Cargo.toml")],
        cwd=workspace, capture_output=True, text=True, check=False,
    )
    require(proc.returncode == 0, f"cargo metadata failed: {proc.stderr[-2000:]}")
    metadata = json.loads(proc.stdout)
    packages = {
        (p["name"], p["version"]): p for p in metadata["packages"]
        if p.get("source") == CRATES_IO
    }
    local = {
        (p["name"], p["version"]): p for p in metadata["packages"]
        if p.get("source") is None
    }
    require(set(local) == FIRST_PARTY_CARGO,
            "Cargo metadata has unreviewed local package")
    for package in local.values():
        require(Path(package["manifest_path"]).resolve(strict=True)
                .is_relative_to(workspace.resolve(strict=True)),
                "local Cargo manifest outside employee workspace")
    require(set(packages) == set(locked),
            f"Cargo metadata/lock mismatch: metadata={len(packages)}, "
            f"lock={len(locked)}, missing={sorted(set(locked)-set(packages))[:5]}")
    cargo_home = Path(os.environ.get("CARGO_HOME", Path.home() / ".cargo"))
    registry_src = (cargo_home / "registry" / "src").resolve(strict=True)
    registry_cache = (cargo_home / "registry" / "cache").resolve(strict=True)
    output = []
    for (name, version), checksum in sorted(locked.items()):
        meta = packages[(name, version)]
        root = Path(meta["manifest_path"]).parent.resolve(strict=True)
        require(root.is_relative_to(registry_src),
                f"Cargo package outside registry source: {name}@{version}")
        registry_id = root.parent.name
        archive = registry_cache / registry_id / f"{name}-{version}.crate"
        require(archive.resolve(strict=True).is_relative_to(registry_cache),
                f"Cargo archive escapes cache: {archive}")
        archive_data = archive.read_bytes()
        require(sha(archive_data) == checksum,
                f"Cargo archive checksum mismatch: {name}@{version}")
        license_file = meta.get("license_file")
        explicit: set[str] = set()
        if license_file:
            license_path = Path(license_file)
            if not license_path.is_absolute():
                license_path = root / license_path
            require(license_path.resolve(strict=True).is_relative_to(root),
                    f"Cargo license_file escapes package: {name}@{version}")
            explicit = {license_path.relative_to(root).as_posix()}
        label = valid_license(meta.get("license"), license_file)
        texts, _ = archive_files(archive_data, f"{name}-{version}", explicit,
                                 installed_root=root, cargo_source=True,
                                 allow_no_notice=True)
        external_sources = []
        status = "archive_text"
        if not has_authorization_text(texts):
            fallback = EXTERNAL_CARGO.get((name, version))
            require(fallback is not None,
                    f"Cargo package lacks full license text: {name}@{version}")
            require(label == fallback["license"] and
                    str(meta.get("repository", "")).rstrip("/") ==
                    fallback["repository"],
                    f"Cargo external license identity differs: {name}@{version}")
            if (name, version) in DLOPEN2_ARCHIVES:
                pinned_checksum, pinned_bytes = DLOPEN2_ARCHIVES[(name, version)]
                require(checksum == pinned_checksum and
                        len(archive_data) == pinned_bytes,
                        f"dlopen2 official archive identity differs: {name}@{version}")
            if (name, version) == ("dispatch2", "0.3.1"):
                require(len(archive_data) == DISPATCH2_ARCHIVE_BYTES and
                        checksum == DISPATCH2_ARCHIVE_SHA256,
                        "dispatch2 official archive identity differs")
                with tarfile.open(fileobj=io.BytesIO(archive_data), mode="r:gz") as official:
                    members = official.getmembers()
                require(len(members) == 26 and all(member.isfile() for member in members),
                        "dispatch2 official archive member shape differs")
            if (name, version) == ("cesu8", "1.1.0"):
                cesu8_legacy_origin(archive_data)
                external_sources.append({
                    "role": "legacy-crate-archive-origin",
                    "url": "https://static.crates.io/crates/cesu8/cesu8-1.1.0.crate",
                    "upstream_release": CESU8_RELEASE,
                    "sha256": CESU8_ARCHIVE_SHA256,
                    "bytes": CESU8_ARCHIVE_BYTES,
                    "member_git_blobs": CESU8_MEMBERS,
                })
            else:
                vcs = json.loads(archive_member(
                    archive_data, f"{name}-{version}/.cargo_vcs_info.json"
                ))
                require(vcs.get("git", {}).get("sha1") == fallback["vcs"]
                        and vcs.get("path_in_vcs") == fallback["path"],
                        f"Cargo archive VCS origin differs: {name}@{version}")
            if (name, version) in (("block2", "0.6.2"),
                                   ("dispatch2", "0.3.1")):
                is_dispatch2 = name == "dispatch2"
                declaration_url = (
                    "https://raw.githubusercontent.com/madsmtm/objc2/" +
                    ("8852b424193ca41602281b3d7540d7c8ed51e49a" if is_dispatch2
                     else "b4167b582b2f75f9a1be75495c41b765344fd03c")
                    + "/LICENSE.md"
                )
                declaration = vendored_text(
                    workspace, BLOCK2_DECLARATION,
                    "7f976f7e9cb2d87df7230606feb932c3f21ac0e664045a775b600046ff850c54",
                    1339,
                )
                require((b"All other crates are trio-licensed" in declaration
                         and b"[MIT]" in declaration) if is_dispatch2 else
                        (b"`block2`" in declaration and
                         b"licensed under [the MIT license][MIT]" in declaration),
                        "objc2 upstream MIT declaration absent")
                declaration_output = "EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md"
                texts[declaration_output] = declaration
                external_sources.append({
                    "role": "upstream-license-declaration",
                    "path": declaration_output,
                    "vendored_source": BLOCK2_DECLARATION,
                    "url": declaration_url, "sha256": sha(declaration),
                    "bytes": len(declaration),
                })
            if name == "selectors":
                readme_url = ("https://raw.githubusercontent.com/servo/stylo/"
                              "572ecba2d1600e7c3d490586692a209faf703baa/"
                              "README.md")
                readme = vendored_text(workspace, STYLO_DECLARATION,
                    "59b1db611079186865d636abe5311c434d748955a97ea6f1ba1ec55ebaf9a84a",
                    7396)
                require(b"Stylo is licensed under MPL 2.0" in readme,
                        "Stylo source license declaration absent")
                external_sources.append({
                    "role": "upstream-license-declaration",
                    "vendored_source": STYLO_DECLARATION,
                    "url": readme_url, "sha256": sha(readme),
                    "bytes": len(readme),
                })
            for filename, url, digest, length in fallback["files"]:
                vendored = VENDORED_CARGO[(name, version, filename)]
                content = vendored_text(workspace, vendored, digest, length)
                relative = ("EXTERNAL_ORIGIN/" +
                            EXTERNAL_OUTPUT_NAMES.get((name, version, filename),
                                                      filename))
                texts[relative] = content
                external_sources.append({
                    "role": "full-license-text", "path": relative,
                    "vendored_source": vendored,
                    "url": url, "sha256": digest, "bytes": length,
                })
            status = "vendored_external"
        require(has_authorization_text(texts),
                f"Cargo package lacks full license text: {name}@{version}")
        output.append({
            "ecosystem": "cargo", "name": name, "version": version,
            "source": CRATES_IO, "integrity": f"sha256-{checksum}",
            "license": label, "texts": texts, "notice_status": status,
            "external_sources": external_sources,
        })
    return output


def pnpm_graph(path: Path, locked: dict[tuple[str, str], str]
               ) -> tuple[list[str], dict[str, str]]:
    lines = path.read_text(encoding="utf-8").splitlines()
    start = lines.index("snapshots:") + 1
    snapshots: list[str] = []
    for line in lines[start:]:
        if line.startswith("  ") and not line.startswith("    "):
            match = SNAPSHOT_ENTRY.fullmatch(line)
            require(match is not None, f"invalid pnpm snapshot: {line}")
            key = match.group(1) or match.group(2)
            base = key.split("(", 1)[0]
            at = base.rfind("@")
            require(at > 0 and (base[:at], base[at + 1:]) in locked,
                    f"snapshot has no locked package: {key}")
            snapshots.append(key)
    require(len(snapshots) == len(set(snapshots)) and bool(snapshots),
            "duplicate or empty pnpm snapshots")
    begin = lines.index("importers:") + 1
    end = lines.index("packages:")
    importer = lines[begin:end]
    require("  .:" in importer, "root pnpm importer absent")
    category: str | None = None
    current: str | None = None
    direct: dict[str, str] = {}
    for line in importer[importer.index("  .:") + 1:]:
        if line.startswith("    ") and not line.startswith("      "):
            category = line.strip()[:-1]
            require(category in ("dependencies", "devDependencies",
                                 "optionalDependencies"),
                    f"unexpected importer section: {category}")
            current = None
        elif line.startswith("      ") and not line.startswith("        "):
            require(category is not None and line.endswith(":"),
                    f"invalid importer package: {line}")
            current = line.strip()[:-1].strip("'")
        elif current and line.startswith("        version: "):
            version = line.partition("version: ")[2].strip().strip("'")
            require(current not in direct, f"duplicate direct package: {current}")
            require(f"{current}@{version}" in snapshots,
                    f"direct version missing exact snapshot: {current}@{version}")
            base_version = version.split("(", 1)[0]
            require((current, base_version) in locked,
                    f"direct version missing locked archive: {current}@{version}")
            direct[current] = base_version
            current = None
    require(bool(direct), "pnpm direct dependency graph empty")
    return sorted(snapshots), direct


class BundleHTML(HTMLParser):
    def __init__(self, html_name: str, chunk_names: set[str]) -> None:
        super().__init__(convert_charrefs=True)
        self.html_name = html_name
        self.chunk_names = chunk_names
        self.scripts = 0
        self.in_script = False

    def handle_starttag(self, tag: str, pairs: list[tuple[str, str | None]]) -> None:
        names = [name for name, _ in pairs]
        require(len(names) == len(set(names)) and
                not any(name.startswith("on") for name in names),
                f"HTML duplicate or event attribute: {self.html_name}")
        attrs = dict(pairs)
        require(tag not in ("base", "iframe", "object", "embed"),
                f"HTML executable container unsupported: {self.html_name}")
        if tag != "script":
            return
        require(not self.in_script, f"nested HTML script: {self.html_name}")
        self.in_script = True
        self.scripts += 1
        src = attrs.get("src")
        require(attrs.get("type") == "module" and isinstance(src, str)
                and bool(src) and not src.startswith("//")
                and not any(ch in src for ch in "\\?#%")
                and urlparse(src).scheme == "",
                f"HTML inline or external script: {self.html_name}")
        if src.startswith("/"):
            target = src[1:]
        else:
            target = posixpath.normpath(posixpath.join(
                posixpath.dirname(self.html_name), src))
        require(target in self.chunk_names,
                f"HTML script is not a captured Vite chunk: {self.html_name}: {src}")

    def handle_startendtag(self, tag: str,
                           attrs: list[tuple[str, str | None]]) -> None:
        require(tag != "script", f"self-closing HTML script: {self.html_name}")
        self.handle_starttag(tag, attrs)

    def handle_data(self, data: str) -> None:
        require(not self.in_script or not data.strip(),
                f"inline HTML script body: {self.html_name}")

    def handle_endtag(self, tag: str) -> None:
        if tag == "script":
            require(self.in_script, f"unpaired HTML script: {self.html_name}")
            self.in_script = False


def check_dist_executable_closure(dist: Path, chunk_names: set[str],
                                  actual_files: dict[str, tuple[int, str]]) -> None:
    require(chunk_names.issubset(actual_files),
            "Vite chunk missing from captured dist files")
    scripts = 0
    html_count = 0
    for name in actual_files:
        lower = name.lower()
        if lower.endswith((".js", ".mjs", ".cjs", ".wasm")):
            require(name in chunk_names and not lower.endswith(".wasm"),
                    f"dist executable asset lacks Vite module graph: {name}")
        elif lower.endswith(".html"):
            html_count += 1
            data = source_file(dist / name, dist)
            parser = BundleHTML(name, chunk_names)
            parser.feed(data.decode("utf-8"))
            parser.close()
            require(not parser.in_script, f"unclosed HTML script: {name}")
            scripts += parser.scripts
        elif lower.endswith(".svg"):
            data = source_file(dist / name, dist).lower()
            require(b"<script" not in data and b"javascript:" not in data,
                    f"executable SVG asset unsupported: {name}")
        elif lower.endswith(".css"):
            data = source_file(dist / name, dist).lower()
            require(b"@import" not in data and b"url(" not in data,
                    f"unattributed CSS import or asset unsupported: {name}")
    require(html_count > 0 and scripts > 0,
            "Vite output has no attributed HTML module script")


def bundle_packages(workspace: Path, evidence_path: Path,
                    locked: dict[tuple[str, str], str]) -> set[tuple[str, str]]:
    evidence = read_json(evidence_path)
    require(isinstance(evidence, dict) and evidence.get("schema") == 2,
            "Vite module evidence schema invalid")
    head = subprocess.run(["git", "-C", str(workspace), "rev-parse", "HEAD"],
                          capture_output=True, text=True, check=False)
    require(head.returncode == 0 and
            evidence.get("sourceCommit") == head.stdout.strip(),
            "Vite module evidence source commit differs")
    chunks = evidence.get("chunks")
    require(isinstance(chunks, list) and bool(chunks),
            "Vite module evidence has no chunks")
    module_ids: list[str] = []
    chunk_names: set[str] = set()
    chunk_imports: list[str] = []
    for chunk in chunks:
        require(isinstance(chunk, dict) and isinstance(chunk.get("fileName"), str)
                and isinstance(chunk.get("moduleIds"), list)
                and bool(chunk["moduleIds"]), "invalid Vite chunk module evidence")
        require(type(chunk.get("codeBytes")) is int and chunk["codeBytes"] > 0
                and isinstance(chunk.get("codeSha256"), str)
                and bool(HEX64.fullmatch(chunk["codeSha256"])),
                "Vite chunk code evidence invalid")
        filename = chunk["fileName"]
        require(filename not in chunk_names and not Path(filename).is_absolute()
                and ".." not in Path(filename).parts and "\\" not in filename,
                "duplicate or unsafe Vite chunk name")
        chunk_names.add(filename)
        for field in ("imports", "dynamicImports"):
            imports = chunk.get(field)
            require(isinstance(imports, list) and all(isinstance(value, str)
                    for value in imports), f"Vite chunk {field} invalid")
            chunk_imports.extend(imports)
        module_ids.extend(chunk["moduleIds"])
    require(all(name in chunk_names for name in chunk_imports),
            "Vite output contains external import")
    installed: set[tuple[str, str]] = set()
    local_modules = 0
    workspace = workspace.resolve(strict=True)
    modules_root = (workspace / "node_modules").resolve(strict=True)
    for raw_id in module_ids:
        require(isinstance(raw_id, str) and bool(raw_id),
                "invalid Vite module ID")
        if raw_id == "\x00rolldown/runtime.js":
            rolldown_key = ("rolldown", "1.2.13")
            require(rolldown_key in locked,
                    "Vite bundled unlocked package: rolldown@1.2.13")
            installed.add(rolldown_key)
            continue
        module_id = raw_id.split("?", 1)[0]
        if module_id.startswith("\x00"):
            require(module_id in ("\x00vite/modulepreload-polyfill.js",
                                  "\x00commonjsHelpers.js"),
                    f"unattributed virtual Vite module: {raw_id}")
            continue
        require(Path(module_id).is_absolute(),
                f"non-absolute Vite module: {raw_id}")
        file = Path(module_id).resolve(strict=True)
        if file.is_relative_to(workspace) and not file.is_relative_to(modules_root):
            require(file.is_file() and not file.is_relative_to(workspace / "dist"),
                    f"unapproved local Vite module: {raw_id}")
            source_name = file.relative_to(workspace).as_posix()
            tracked = subprocess.run(
                ["git", "-C", str(workspace), "ls-files", "--error-unmatch",
                 "--", source_name], capture_output=True, check=False,
            )
            require(tracked.returncode == 0,
                    f"untracked local Vite module: {raw_id}")
            local_modules += 1
            continue
        require(file.is_relative_to(modules_root),
                f"Vite module outside approved source: {raw_id}")
        current = file.parent
        while current.is_relative_to(modules_root):
            manifest = current / "package.json"
            try:
                checked, _ = checked_descendant(
                    current, "package.json", directory=False)
            except FileNotFoundError:
                checked = None
            if checked is not None:
                pkg = json.loads(raw(checked).decode("utf-8"))
                key = (pkg.get("name"), pkg.get("version"))
                require(key in locked, f"Vite bundled unlocked package: {key}")
                installed.add(key)
                break
            current = current.parent
        else:
            raise NoticeError(f"Vite module package absent: {raw_id}")
    identities = sorted(f"{name}@{version}" for name, version in installed)
    require(local_modules > 0, "Vite graph has no tracked application module")
    require(evidence.get("packageIds") == identities,
            "Vite package identities differ from raw chunk modules")
    expected_files = evidence.get("distFiles")
    require(isinstance(expected_files, list) and bool(expected_files),
            "Vite dist evidence absent")
    dist = workspace / "dist"
    require(stat.S_ISDIR(no_reparse(dist).st_mode), "Vite dist directory absent")
    dist = dist.resolve(strict=True)
    actual_files: dict[str, tuple[int, str]] = {}
    for current, dirs, files in os.walk(
            dist, topdown=True, onerror=walk_error, followlinks=False):
        current_relative = Path(current).relative_to(dist)
        if current_relative.parts:
            checked_descendant(dist, current_relative, directory=True)
        for directory in dirs:
            relative = (Path(current) / directory).relative_to(dist)
            checked_descendant(dist, relative, directory=True)
        for name in files:
            relative = (Path(current) / name).relative_to(dist)
            path, _ = checked_descendant(dist, relative, directory=False)
            data = path.read_bytes()
            actual_files[relative.as_posix()] = (len(data), sha(data))
    provided: dict[str, tuple[int, str]] = {}
    for item in expected_files:
        require(isinstance(item, dict) and isinstance(item.get("path"), str),
                "Vite dist record invalid")
        path = item["path"]
        require(path not in provided and not Path(path).is_absolute()
                and ".." not in Path(path).parts and "\\" not in path,
                "Vite dist record path invalid")
        provided[path] = (item.get("bytes"), item.get("sha256"))
    require(actual_files == provided, "Vite dist bytes changed after module capture")
    for chunk in chunks:
        require(actual_files.get(chunk["fileName"]) ==
                (chunk["codeBytes"], chunk["codeSha256"]),
                f"Vite chunk code differs from built dist: {chunk['fileName']}")
    check_dist_executable_closure(dist, chunk_names, actual_files)
    return installed


def pnpm_packages(workspace: Path, locked: dict[tuple[str, str], str],
                  direct: dict[str, str],
                  bundled: set[tuple[str, str]]) -> tuple[list[dict], list[str]]:
    store = workspace / "node_modules" / ".pnpm"
    require(store.is_dir() and not store.is_symlink(), "pnpm virtual store absent")
    roots: dict[tuple[str, str], Path] = {}
    def generated_bins(path: Path) -> None:
        require(path.is_dir() and not path.is_symlink(),
                f"pnpm generated bin directory invalid: {path}")
        for item in path.iterdir():
            require(item.is_file() and item.resolve(strict=True).is_relative_to(store),
                    f"pnpm generated bin entry invalid: {item}")

    for unit in sorted(store.iterdir()):
        if unit.name == "node_modules":
            continue
        if not unit.is_dir():
            require(unit.name == "lock.yaml",
                    f"unexpected pnpm store file: {unit.name}")
            continue
        nm = unit / "node_modules"
        require(nm.is_dir() and not nm.is_symlink(),
                f"pnpm virtual package has no node_modules: {unit.name}")
        for entry in sorted(nm.iterdir()):
            if entry.name == ".bin":
                generated_bins(entry)
                continue
            candidates = sorted(entry.iterdir()) if entry.name.startswith("@") else [entry]
            for candidate in candidates:
                manifest, _ = checked_descendant(
                    candidate, "package.json", directory=False)
                pkg = json.loads(raw(manifest).decode("utf-8"))
                require(isinstance(pkg, dict), f"invalid package.json: {manifest}")
                name, version = pkg.get("name"), pkg.get("version")
                require(isinstance(name, str) and isinstance(version, str),
                        f"npm package identity absent: {manifest}")
                key = (name, version)
                require(key in locked, f"installed npm package absent from lock: {key}")
                real = candidate.resolve(strict=True)
                require(real.is_relative_to(store.resolve(strict=True)),
                        f"npm package escapes virtual store: {candidate}")
                require(key not in roots or roots[key] == real,
                        f"duplicate npm roots: {key}")
                roots[key] = real
    require(bool(roots), "no installed pnpm packages")
    hoist = store / "node_modules"
    if hoist.exists():
        require(hoist.is_dir() and not hoist.is_symlink(),
                "pnpm hoist is not a directory")
        for entry in sorted(hoist.iterdir()):
            if entry.name == ".bin":
                generated_bins(entry)
                continue
            candidates = sorted(entry.iterdir()) if entry.name.startswith("@") else [entry]
            for candidate in candidates:
                manifest, _ = checked_descendant(
                    candidate, "package.json", directory=False)
                pkg = json.loads(raw(manifest).decode("utf-8"))
                key = (pkg.get("name"), pkg.get("version"))
                require(key in roots and candidate.resolve(strict=True) == roots[key],
                        f"pnpm hoist package is not attested: {candidate}")
    package_json = read_json(workspace / "package.json")
    declared = set(package_json.get("dependencies", {})) | set(
        package_json.get("devDependencies", {}))
    require(declared == set(direct),
            "pnpm importer does not match package.json direct package names")
    for name, version in direct.items():
        top = workspace / "node_modules" / name
        require(top.exists(), f"top-level npm dependency absent: {name}")
        actual = top.resolve(strict=True)
        require(actual.is_relative_to(store.resolve(strict=True)),
                f"top-level npm dependency outside pnpm store: {name}")
        pkg = json.loads(source_file(actual / "package.json", actual).decode("utf-8"))
        require((pkg.get("name"), pkg.get("version")) == (name, version),
                f"top-level npm dependency version mismatch: {name}@{version}")
        require(roots.get((name, version)) == actual,
                f"top-level npm dependency root mismatch: {name}@{version}")
    output = []
    for (name, version), integrity in sorted(locked.items()):
        tarball = npm_tarball(name, version, integrity)
        archive_root = npm_archive_root(tarball, name)
        texts, manifest_data = archive_files(
            tarball, archive_root, installed_root=roots.get((name, version)),
            allow_no_notice=True)
        require(bool(manifest_data), f"npm archive package.json absent: {name}@{version}")
        archive_pkg = json.loads(manifest_data.decode("utf-8"))
        require((archive_pkg.get("name"), archive_pkg.get("version")) ==
                (name, version), f"npm archive identity mismatch: {name}@{version}")
        label = valid_license(archive_pkg.get("license"))
        declared = re.fullmatch(r"SEE LICENSE IN (.+)", label, re.IGNORECASE)
        if declared:
            declared_path = declared.group(1).strip().replace("\\", "/")
            require(declared_path and not declared_path.startswith("/")
                    and ".." not in PurePosixPath(declared_path).parts,
                    f"unsafe declared npm license file: {name}@{version}")
            texts, _ = archive_files(tarball, archive_root,
                                     explicit={declared_path})
        require(not texts or has_authorization_text(texts),
                f"npm package has notices but no full license text: {name}@{version}")
        if not texts:
            require((name, version) not in bundled,
                    f"bundled npm package has no license text: {name}@{version}")
        if (name, version) in roots:
            installed_json = source_file(roots[(name, version)] / "package.json",
                                         roots[(name, version)])
            require(installed_json == manifest_data,
                    f"installed npm package.json differs from archive: {name}@{version}")
        output.append({
            "ecosystem": "pnpm", "name": name, "version": version,
            "source": "https://registry.npmjs.org", "integrity": integrity,
            "license": label, "texts": texts,
            "notice_status": "archive_text" if texts else "excluded_build_only",
        })
    return output, [f"{name}@{version}" for name, version in sorted(roots)]


def collect(workspace: Path, output: Path, bundle_path: Path) -> None:
    workspace = workspace.resolve(strict=True)
    require(not output.exists(), f"output already exists: {output}")
    top_proc = subprocess.run(
        ["git", "-C", str(workspace), "rev-parse", "--show-toplevel"],
        capture_output=True, text=True, check=False,
    )
    require(top_proc.returncode == 0, "source repository root unavailable")
    tracked = subprocess.run(
        ["git", "-C", top_proc.stdout.strip(), "status", "--porcelain",
         "--untracked-files=no"], capture_output=True, text=True, check=False,
    )
    require(tracked.returncode == 0 and not tracked.stdout.strip(),
            "tracked source is not clean before collection")
    status = subprocess.run(
        ["git", "-C", str(workspace), "status", "--porcelain",
         "--untracked-files=all"], capture_output=True, text=True, check=False,
    )
    require(status.returncode == 0 and not status.stdout.strip(),
            "source worktree is not clean before collection")
    lock_cargo = workspace / "Cargo.lock"
    lock_pnpm = workspace / "pnpm-lock.yaml"
    cargo = cargo_lock(lock_cargo)
    pnpm = pnpm_lock(lock_pnpm)
    snapshots, direct = pnpm_graph(lock_pnpm, pnpm)
    bundled = bundle_packages(workspace, bundle_path, pnpm)
    pnpm_items, _ = pnpm_packages(workspace, pnpm, direct, bundled)
    packages = cargo_packages(workspace, cargo) + pnpm_items
    require(len(packages) == len({(p["ecosystem"], p["name"], p["version"])
                                   for p in packages}), "duplicate package identity")

    project_files = [
        workspace / "third_party" / "cc-switch" / "LICENSE",
        workspace / "PROVENANCE.md",
        workspace / "DEPENDENCY-LICENSES.md",
    ]
    source_files = [lock_cargo, lock_pnpm, workspace / "package.json",
                    workspace / "Cargo.toml"] + project_files + [
        workspace / name for name in sorted(VENDORED_FILES)]
    source_hashes = {
        path.relative_to(workspace).as_posix():
        sha(git_blob(workspace, path.relative_to(workspace).as_posix()))
        for path in source_files
    }
    copied: list[tuple[str, bytes]] = []
    package_records = []
    for package in sorted(packages, key=lambda p: (
            p["ecosystem"], p["name"], p["version"])):
        prefix = (package["ecosystem"] + "/" +
                  safe_part(package["name"]) + "@" +
                  safe_part(package["version"]))
        refs = []
        for relative, data in sorted(package["texts"].items()):
            destination = prefix + "/" + relative
            refs.append(destination)
            copied.append((destination, data))
        package_records.append({
            key: package[key] for key in
            ("ecosystem", "name", "version", "source", "integrity", "license")
        } | {"files": refs,
             "notice_status": package.get("notice_status", "archive_text"),
             "external_sources": package.get("external_sources", [])})
    for path in project_files:
        name = path.relative_to(workspace).as_posix()
        copied.append(("project/" + name, git_blob(workspace, name)))
    destinations = [name for name, _ in copied]
    require(len(destinations) == len(set(destinations)), "duplicate output path")
    require(sum(len(data) for _, data in copied) <= MAX_TOTAL,
            "notice text total exceeds limit")
    proc = subprocess.run(["git", "-C", str(workspace), "rev-parse", "HEAD"],
                          capture_output=True, text=True, check=False)
    require(proc.returncode == 0 and bool(re.fullmatch(r"[0-9a-f]{40}",
                                                       proc.stdout.strip())),
            "source commit unavailable")
    manifest = {
        "schema": SCHEMA,
        "source_commit": proc.stdout.strip(),
        "source_files": source_hashes,
        "locked": {"cargo": len(cargo), "pnpm": len(pnpm)},
        "pnpm_graph": {"snapshots": snapshots, "direct": direct},
        "packages": package_records,
        "files": [{"path": name, "bytes": len(data), "sha256": sha(data)}
                  for name, data in sorted(copied)],
    }
    output.mkdir(parents=True, exist_ok=False)
    for name, data in sorted(copied):
        dest = output / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(data)
    manifest_data = json_bytes(manifest)
    (output / "manifest.json").write_bytes(manifest_data)
    verify(output, workspace, sha(manifest_data), bundle_path)


def verify(root: Path, workspace: Path, expected_manifest_sha256: str,
           bundle_path: Path) -> None:
    require(bool(HEX64.fullmatch(expected_manifest_sha256)),
            "external manifest SHA-256 missing or malformed")
    require(stat.S_ISDIR(no_reparse(root).st_mode),
            "notice root is not an ordinary directory")
    root = root.resolve(strict=True)
    manifest_data = source_file(root / "manifest.json", root)
    require(sha(manifest_data) == expected_manifest_sha256,
            "notice manifest differs from external build-time hash")
    manifest = json.loads(manifest_data)
    require(isinstance(manifest, dict) and manifest.get("schema") == SCHEMA,
            "notice manifest schema invalid")
    workspace = workspace.resolve(strict=True)
    source_files = [
        "Cargo.lock", "pnpm-lock.yaml", "package.json", "Cargo.toml",
        "third_party/cc-switch/LICENSE", "PROVENANCE.md",
        "DEPENDENCY-LICENSES.md",
    ] + sorted(VENDORED_FILES)
    require(set(manifest.get("source_files", {})) == set(source_files),
            "source file inventory mismatch")
    for name in source_files:
        actual = sha(git_blob(workspace, name))
        require(manifest["source_files"][name] == actual,
                f"committed source file changed: {name}")
    proc = subprocess.run(["git", "-C", str(workspace), "rev-parse", "HEAD"],
                          capture_output=True, text=True, check=False)
    require(proc.returncode == 0 and
            proc.stdout.strip() == manifest.get("source_commit"),
            "source commit changed")
    top_proc = subprocess.run(
        ["git", "-C", str(workspace), "rev-parse", "--show-toplevel"],
        capture_output=True, text=True, check=False,
    )
    require(top_proc.returncode == 0, "source repository root unavailable")
    dirty_source = subprocess.run(
        ["git", "-C", top_proc.stdout.strip(), "status", "--porcelain",
         "--untracked-files=no"], capture_output=True, text=True, check=False,
    )
    require(dirty_source.returncode == 0 and not dirty_source.stdout.strip(),
            "tracked source changed after notice collection")
    cargo = cargo_lock(workspace / "Cargo.lock")
    pnpm = pnpm_lock(workspace / "pnpm-lock.yaml")
    snapshots, direct = pnpm_graph(workspace / "pnpm-lock.yaml", pnpm)
    bundled = bundle_packages(workspace, bundle_path, pnpm)
    graph = manifest.get("pnpm_graph", {})
    require(graph.get("snapshots") == snapshots and graph.get("direct") == direct,
            "pnpm graph differs from trusted lock")
    require(manifest.get("locked") == {"cargo": len(cargo), "pnpm": len(pnpm)},
            "locked package counts differ")
    identities = {
        (item.get("ecosystem"), item.get("name"), item.get("version"))
        for item in manifest.get("packages", [])
    }
    required = {("cargo", name, version) for name, version in cargo}
    required |= {("pnpm", name, version) for name, version in pnpm}
    require(identities == required and
            len(manifest["packages"]) == len(required),
            "manifest does not cover every locked package exactly once")
    expected: dict[str, dict] = {}
    for item in manifest.get("files", []):
        require(isinstance(item, dict), "invalid notice file record")
        name = item.get("path")
        require(isinstance(name, str) and name not in expected,
                "duplicate or invalid notice path")
        parts = Path(name).parts
        require(parts and all(part not in ("", ".", "..") for part in parts)
                and not Path(name).is_absolute() and "\\" not in name,
                f"unsafe notice path: {name}")
        expected[name] = item
    require(bool(expected), "notice manifest has no texts")
    actual: set[str] = set()
    for current, dirs, files in os.walk(
            root, topdown=True, onerror=walk_error, followlinks=False):
        current_relative = Path(current).relative_to(root)
        if current_relative.parts:
            checked_descendant(root, current_relative, directory=True)
        for directory in dirs:
            relative = (Path(current) / directory).relative_to(root)
            checked_descendant(root, relative, directory=True)
        for file in files:
            relative = (Path(current) / file).relative_to(root)
            checked_descendant(root, relative, directory=False)
            actual.add(relative.as_posix())
    require(actual == set(expected) | {"manifest.json"},
            f"notice file set mismatch: missing={sorted(set(expected)-actual)[:3]}, "
            f"extra={sorted(actual-set(expected)-{'manifest.json'})[:3]}")
    verified_texts: dict[str, bytes] = {}
    for name, item in expected.items():
        data = source_file(root / name, root)
        require(len(data) == item.get("bytes") and sha(data) == item.get("sha256"),
                f"notice hash/size mismatch: {name}")
        verified_texts[name] = data
    package_files: set[str] = set()
    for package in manifest["packages"]:
        refs = package.get("files")
        require(isinstance(refs, list), "package notice references invalid")
        name, version, ecosystem = (
            package["name"], package["version"], package["ecosystem"]
        )
        status = package.get("notice_status")
        if not refs:
            require(ecosystem == "pnpm" and status == "excluded_build_only"
                    and (name, version) not in bundled,
                    f"package without notice cannot be excluded: {name}@{version}")
        else:
            require(status in ("archive_text", "vendored_external"),
                    f"package notice status invalid: {name}@{version}")
        prefix = ecosystem + "/" + safe_part(name) + "@" + safe_part(version) + "/"
        require(all(isinstance(ref, str) and ref.startswith(prefix)
                    and ref in expected for ref in refs),
                f"package notice path mismatch: {name}@{version}")
        if refs:
            require(has_authorization_text({
                ref[len(prefix):]: verified_texts[ref] for ref in refs
            }), f"package notice lacks full license text: {name}@{version}")
        package_files.update(refs)
        locked_integrity = (
            "sha256-" + cargo[(name, version)] if ecosystem == "cargo"
            else pnpm[(name, version)]
        )
        require(package.get("integrity") == locked_integrity,
                f"package integrity differs from lock: {name}@{version}")
    project = {
        "project/" + name for name in (
            "third_party/cc-switch/LICENSE", "PROVENANCE.md",
            "DEPENDENCY-LICENSES.md"
        )
    }
    require(set(expected) == package_files | project,
            "notice file records do not match package and project inventory")
    for name in ("third_party/cc-switch/LICENSE", "PROVENANCE.md",
                 "DEPENDENCY-LICENSES.md"):
        require(expected["project/" + name]["sha256"] ==
                manifest["source_files"][name],
                f"project notice differs from committed source: {name}")
    rechecked_cargo = cargo_packages(workspace, cargo)
    rechecked_pnpm, _ = pnpm_packages(workspace, pnpm, direct, bundled)
    stored_packages = {
        (p["ecosystem"], p["name"], p["version"]): p
        for p in manifest["packages"]
    }
    for package in rechecked_cargo + rechecked_pnpm:
        key = (package["ecosystem"], package["name"], package["version"])
        stored = stored_packages[key]
        require(stored["notice_status"] ==
                package.get("notice_status", "archive_text") and
                stored["license"] == package["license"] and
                stored["source"] == package["source"] and
                stored.get("external_sources", []) ==
                package.get("external_sources", []),
                f"dependency notice status or license changed: {key}")
        prefix = (package["ecosystem"] + "/" +
                  safe_part(package["name"]) + "@" +
                  safe_part(package["version"]) + "/")
        require(stored["files"] ==
                [prefix + relative for relative in sorted(package["texts"])],
                f"dependency full-text inventory changed: {key}")
        for relative, data in package["texts"].items():
            item = expected.get(prefix + relative)
            require(item is not None and item["sha256"] == sha(data),
                    f"built dependency notice differs: {prefix}{relative}")
        if key in (("cargo", "block2", "0.6.2"),
                   ("cargo", "dispatch2", "0.3.1")) and (
                package["notice_status"] == "vendored_external"):
            name, version = key[1], key[2]
            for relative, vendored in (
                ("EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md", BLOCK2_DECLARATION),
                ("EXTERNAL_ORIGIN/LICENSE-MIT.txt",
                 VENDORED_CARGO[(name, version, "LICENSE-MIT.txt")]),
            ):
                output_bytes = verified_texts.get(prefix + relative)
                source_bytes = git_blob(workspace, vendored)
                require(output_bytes == source_bytes and
                        manifest["source_files"].get(vendored) == sha(source_bytes),
                        f"objc2 external notice differs from committed source: {relative}")
        if key == ("cargo", "cesu8", "1.1.0") and (
                package["notice_status"] == "vendored_external"):
            for filename in ("LICENSE-APACHE", "LICENSE-MIT"):
                relative = "EXTERNAL_ORIGIN/" + filename
                vendored = VENDORED_CARGO[("cesu8", "1.1.0", filename)]
                output_bytes = verified_texts.get(prefix + relative)
                source_bytes = git_blob(workspace, vendored)
                require(output_bytes == source_bytes and
                        manifest["source_files"].get(vendored) == sha(source_bytes),
                        f"cesu8 external notice differs from committed source: {relative}")
        if key in (("cargo", "dlopen2", "0.8.2"),
                   ("cargo", "dlopen2_derive", "0.4.3")) and (
                package["notice_status"] == "vendored_external"):
            name, version = key[1], key[2]
            relative = "EXTERNAL_ORIGIN/LICENSE"
            vendored = VENDORED_CARGO[(name, version, "LICENSE")]
            output_bytes = verified_texts.get(prefix + relative)
            source_bytes = git_blob(workspace, vendored)
            require(output_bytes == source_bytes and
                    manifest["source_files"].get(vendored) == sha(source_bytes),
                    f"dlopen2 external notice differs from committed source: {name}@{version}")
    print(f"verified {len(manifest['packages'])} packages and "
          f"{len(expected)} full notice files")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    collect_parser = sub.add_parser("collect")
    collect_parser.add_argument("--workspace", type=Path, required=True)
    collect_parser.add_argument("--output", type=Path, required=True)
    collect_parser.add_argument("--bundle-modules", type=Path, required=True)
    verify_parser = sub.add_parser("verify")
    verify_parser.add_argument("--root", type=Path, required=True)
    verify_parser.add_argument("--workspace", type=Path, required=True)
    verify_parser.add_argument("--expected-manifest-sha256", required=True)
    verify_parser.add_argument("--bundle-modules", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "collect":
            collect(args.workspace, args.output, args.bundle_modules)
        else:
            verify(args.root, args.workspace, args.expected_manifest_sha256,
                   args.bundle_modules)
    except (NoticeError, OSError, KeyError, ValueError) as exc:
        print(f"notice collection failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
