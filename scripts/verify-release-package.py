#!/usr/bin/env python3
"""Independent bounded release reader; never imports the Go packager.
Provenance: handwritten from the CPA Cloud release contract, ELF64 ABI fields,
Go 1.26.9 build-information format documentation, and Python standard APIs.
https://go.dev/src/debug/buildinfo/buildinfo.go
https://raw.githubusercontent.com/golang/go/go1.26.9/src/cmd/go/internal/modload/build.go
No reference-product implementation/tests are used. No binary is executed.
"""
import argparse
import hashlib
import gzip
import html.parser
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile

VERSION = "v0.1.0"
BASE = "cpa-cloud_v0.1.0_linux_amd64"
ARCHIVE = BASE + ".tar.gz"
SIDECAR = ARCHIVE + ".sha256"
LOCK = "a4eb191ae92b5545e6f8deb8ad504ebecb44c929a5d4146acf225988a27c2ade"
BINS = ("cpa-cloud", "cpa-cloud-backup")
DOCUMENTS = ("README.md", "README.en.md", "CONTRIBUTING.md", "AGENTS.md", "THIRD_PARTY_NOTICES.md")
HEX40 = re.compile(r"[0-9a-f]{40}\Z")
HEX64 = re.compile(r"[0-9a-f]{64}\Z")
MAX_FILE = 256 << 20
MAX_TOTAL = 768 << 20
GO = {
    "github.com/dustin/go-humanize": ("v1.0.1", ("LICENSE",)),
    "github.com/google/uuid": ("v1.6.0", ("LICENSE",)),
    "github.com/mattn/go-isatty": ("v0.0.20", ("LICENSE",)),
    "github.com/ncruces/go-strftime": ("v1.0.0", ("LICENSE",)),
    "github.com/remyoudompheng/bigfft": ("v0.0.0-20230129092748-24d4a6f8daec", ("LICENSE",)),
    "golang.org/x/crypto": ("v0.42.0", ("LICENSE", "PATENTS")),
    "golang.org/x/exp": ("v0.0.0-20251023183803-a4bb9ffd2546", ("LICENSE", "PATENTS")),
    "golang.org/x/sys": ("v0.42.0", ("LICENSE", "PATENTS")),
    "modernc.org/libc": ("v1.70.0", ("LICENSE", "LICENSE-3RD-PARTY.md")),
    "modernc.org/mathutil": ("v1.7.1", ("LICENSE", "mersenne/LICENSE")),
    "modernc.org/memory": ("v1.11.0", ("LICENSE", "LICENSE-GO", "LICENSE-MMAP-GO")),
    "modernc.org/sqlite": ("v1.46.2", ("LICENSE", "SQLITE-LICENSE")),
}
NPM = {
    "react": ("19.3.0", ("LICENSE",)),
    "react-dom": ("19.3.0", ("LICENSE",)),
    "scheduler": ("0.28.0", ("LICENSE",)),
    "vite": ("8.3.0", ("LICENSE.md",)),
    "rolldown": ("1.2.9", ("LICENSE", "THIRD-PARTY-LICENSE")),
}
ROLLDOWN_HASHES = {
    "LICENSE": "23ecfff35a5a2e80d92142f75228912c3b1abc4b5a8337a821ff4397e2f9f734",
    "THIRD-PARTY-LICENSE": "a877291d800ed43692f3f9ae09d8e01cc6f7293ad39d43896059c188ffbb8b7c",
}
HELPERS = {
    "vite/dist/node/chunks/node.js": "2d7a6a7a48f06baada8f6ceec415505c2426aa308fd23b8ace5da443bc7a9673",
    "rolldown/dist/experimental-index.mjs": "d901af4bb1c47873e696b483143218a38c27e32c705f662165f4f12642e967b7",
}

class Reject(ValueError):
    pass

def need(value, code):
    if not value:
        raise Reject(code)

def sha(data):
    return hashlib.sha256(data).hexdigest()

def safe(name):
    need(isinstance(name, str) and name and not name.startswith("/") and
         not any(c in name for c in "\\:\0\r\n") and
         all(ord(c) >= 32 and ord(c) != 127 for c in name), "RVER_PATH")
    parts = name.split("/")
    need(all(p not in ("", ".", "..") for p in parts), "RVER_PATH")
    for part in parts:
        low = part.lower()
        need(low not in {".git", "node_modules", "data", "secrets", "keys", "key",
                         "backup", "backups", "credentials", "sessions", "session",
                         "config", "config.json", "master.key"} and
             not low.startswith(".env") and
             not low.endswith((".db", ".sqlite", ".key", ".pem")), "RVER_PRIVATE_MATERIAL")
    return name

def no_links(path):
    path = Path(path).absolute()
    for item in (path, *path.parents):
        need(not item.is_symlink(), "RVER_SOURCE_SYMLINK")

def read_file(path, limit=MAX_FILE):
    path = Path(path)
    no_links(path)
    info = path.stat()
    need(stat.S_ISREG(info.st_mode) and info.st_size <= limit, "RVER_REGULAR_FILE")
    data = path.read_bytes()
    need(len(data) == info.st_size, "RVER_FILE_CHANGED")
    return data

def unique_object(pairs):
    result = {}
    for key, value in pairs:
        need(key not in result, "RVER_DUPLICATE_JSON_KEY")
        result[key] = value
    return result

def decode_json(data):
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=unique_object)
    except (UnicodeError, json.JSONDecodeError) as exc:
        raise Reject("RVER_JSON") from exc

def run_git(root, *args):
    return subprocess.check_output(["git", "--no-optional-locks", *args], cwd=root, timeout=60)

def verify_source(root, source, runner=run_git):
    """Direct HEAD blob comparison, index-stage and hidden-flag rejection.
    Independent workflow defense; does not repair the frozen kernel's open P2.
    """
    need(HEX40.fullmatch(source), "RVER_SOURCE_SHA")
    root = Path(root).absolute()
    no_links(root)
    need(runner(root, "rev-parse", "HEAD").decode().strip() == source, "RVER_SOURCE_HEAD")
    need(Path(runner(root, "rev-parse", "--show-toplevel").decode().strip()).absolute() == root,
         "RVER_SOURCE_ROOT")
    tree = {}
    for record in runner(root, "ls-tree", "-rz", "--full-tree", source).split(b"\0"):
        if not record:
            continue
        meta, raw_name = record.split(b"\t", 1)
        mode, kind, blob = meta.decode().split()
        name = raw_name.decode("utf-8")
        # Repository source may contain public data fixtures, unlike a payload.
        need(name and not name.startswith("/") and "\\" not in name and
             all(x not in ("", ".", "..") for x in name.split("/")), "RVER_SOURCE_PATH")
        need(name not in tree and mode in ("100644", "100755") and kind == "blob"
             and HEX40.fullmatch(blob), "RVER_SOURCE_TREE")
        tree[name] = (mode, blob)
    need(tree, "RVER_SOURCE_TREE")
    index = {}
    for record in runner(root, "ls-files", "--stage", "-z").split(b"\0"):
        if not record:
            continue
        meta, raw_name = record.split(b"\t", 1)
        mode, blob, stage = meta.decode().split()
        name = raw_name.decode("utf-8")
        need(stage == "0" and name not in index, "RVER_SOURCE_INDEX")
        index[name] = (mode, blob)
    need(index == tree, "RVER_SOURCE_INDEX")
    flags = {}
    for record in runner(root, "ls-files", "-v", "-z").split(b"\0"):
        if record:
            need(record[:2] == b"H ", "RVER_HIDDEN_INDEX_FLAG")
            name = record[2:].decode("utf-8")
            need(name not in flags, "RVER_SOURCE_INDEX")
            flags[name] = True
    need(set(flags) == set(tree), "RVER_SOURCE_INDEX")
    need(not runner(root, "ls-files", "--others", "--exclude-standard", "-z"),
         "RVER_UNTRACKED_SOURCE")
    need(not runner(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z",
                    "--", "cmd", "internal", "scripts", "go.mod", "go.sum", "web/src"),
         "RVER_IGNORED_SOURCE")
    for name, (_, blob) in tree.items():
        path = root.joinpath(*name.split("/"))
        need(read_file(path) == runner(root, "cat-file", "blob", blob), "RVER_SOURCE_BYTES")
    web_inputs = verify_web_input_closure(root, tree)
    return {"source_sha": source, "tracked_files": len(tree), "hidden_index_flags": False,
            "web_inputs": web_inputs}


def verify_web_input_closure(root, tree):
    """All physical Web inputs except the two explicit generated directories
    must be raw HEAD files. This includes ignored public/bin and build configs.
    """
    root = Path(root)
    expected = {name for name in tree if name.startswith("web/")}
    need({"web/package.json", "web/bun.lock", "web/index.html", "web/vite.config.ts"} <= expected,
         "RVER_WEB_INPUTS")
    directories = {"web"}
    for name in expected:
        directories.update(p.as_posix() for p in Path(name).parents if str(p) != ".")
    actual = set()
    for parent, dirs, names in os.walk(root / "web", followlinks=False):
        rel = Path(parent).relative_to(root).as_posix()
        no_links(parent)
        need(rel in directories, "RVER_EXTRA_WEB_INPUT")
        for name in list(dirs):
            child = Path(parent) / name
            no_links(child)
            if rel == "web" and name in ("node_modules", "dist"):
                dirs.remove(name)
            else:
                need(child.relative_to(root).as_posix() in directories, "RVER_EXTRA_WEB_INPUT")
        for name in names:
            path = Path(parent) / name
            no_links(path)
            relative = path.relative_to(root).as_posix()
            need(relative in expected, "RVER_EXTRA_WEB_INPUT")
            actual.add(relative)
    need(actual == expected, "RVER_WEB_INPUTS")
    return {name: sha(read_file(root / name)) for name in sorted(expected)}

WEB_COMMANDS = [
    ["bun", "install", "--frozen-lockfile"],
    ["bun", "run", "typecheck"],
    ["bun", "run", "test"],
    ["bun", "run", "build"],
]

def validate_web_receipt(receipt, source, hashes, inputs=None):
    # Matching a producer receipt is not authentication of arbitrary JSON.
    # Workflow run/head/artifact/hash binding supplies the origin boundary.
    need(isinstance(receipt, dict) and set(receipt) ==
         {"schema", "kind", "source_sha", "bun_version", "commands", "inputs", "outputs"},
         "RVER_WEB_RECEIPT")
    need(type(receipt["schema"]) is int and receipt["schema"] == 1 and
         receipt["kind"] == "producer-observed-private-head-web-build" and
         receipt["source_sha"] == source and receipt["bun_version"] == "1.3.14" and
         receipt["commands"] == WEB_COMMANDS and receipt["outputs"] == hashes,
         "RVER_WEB_RECEIPT")
    need(isinstance(receipt["inputs"], dict) and receipt["inputs"] and all(
         isinstance(n, str) and n.startswith("web/") and isinstance(h, str) and HEX64.fullmatch(h)
         for n, h in receipt["inputs"].items()), "RVER_WEB_RECEIPT")
    if inputs is not None:
        need(receipt["inputs"] == inputs, "RVER_WEB_RECEIPT_INPUTS")

def build_web(root, source, receipt_path):
    """Observe fixed commands in a private raw-HEAD Web snapshot, not a marker.
    Dependency downloads are permitted with the fixed Bun 1.3.14 lockfile
    install. This is not an offline/cache-only claim. Raw input and lockfile
    SHA256 values must remain unchanged before and after all commands.
    No release API or product service is called here.
    """
    root = Path(root).absolute()
    evidence = verify_source(root, source)
    inputs = evidence["web_inputs"]
    need(sha(read_file(root / "web/bun.lock").replace(b"\r\n", b"\n")) == LOCK,
         "RVER_SOURCE_LOCK")
    no_links(receipt_path)
    need(not Path(receipt_path).exists() and not (root / "web/dist").exists(), "RVER_WEB_OUTPUT_EXISTS")
    allowed_env = {"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"}
    env = {k: val for k, val in os.environ.items() if k.upper() in allowed_env}
    cache = os.environ.get("BUN_INSTALL_CACHE_DIR")
    need(cache and Path(cache).is_absolute(), "RVER_BUN_CACHE_REQUIRED")
    no_links(cache)
    env["BUN_INSTALL_CACHE_DIR"] = cache
    need(subprocess.check_output(["bun", "--version"], env=env, timeout=30).strip() == b"1.3.14",
         "RVER_BUN_VERSION")
    with tempfile.TemporaryDirectory(prefix="cpa-head-web-build-") as tmp:
        private = Path(tmp)
        for name, digest in inputs.items():
            body = run_git(root, "cat-file", "blob", source + ":" + name)
            need(sha(body) == digest, "RVER_WEB_INPUT_CHANGED")
            target = private / name
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open("xb") as out:
                out.write(body)
        for command in WEB_COMMANDS:
            subprocess.run(command, cwd=private / "web", env=env, check=True, timeout=900)
        need(all(sha(read_file(private / name)) == digest for name, digest in inputs.items()),
             "RVER_WEB_INPUT_CHANGED")
        built = disk_tree(private / "web/dist")
        hashes = web_structure(built)
        need(verify_source(root, source)["web_inputs"] == inputs, "RVER_WEB_INPUT_CHANGED")
        # Every output byte is copied exclusively after the build has succeeded.
        target = root / "web/dist"
        target.mkdir()
        for name, body in built.items():
            destination = target.joinpath(*name.split("/"))
            destination.parent.mkdir(parents=True, exist_ok=True)
            with destination.open("xb") as output:
                output.write(body)
    receipt = {"schema": 1, "kind": "producer-observed-private-head-web-build",
               "source_sha": source, "bun_version": "1.3.14", "commands": WEB_COMMANDS,
               "inputs": inputs, "outputs": hashes}
    validate_web_receipt(receipt, source, hashes, inputs)
    with Path(receipt_path).open("xb") as output:
        output.write(json.dumps(receipt, sort_keys=True, separators=(",", ":")).encode())
    return receipt



def take(data, offset, size):
    need(isinstance(offset, int) and isinstance(size, int) and
         0 <= offset <= len(data) and 0 <= size <= len(data) - offset, "RVER_ELF_BOUNDS")
    return data[offset:offset + size]

def unpack(fmt, data, offset):
    return struct.unpack(fmt, take(data, offset, struct.calcsize(fmt)))

def cstring(data, offset):
    need(0 <= offset < len(data), "RVER_ELF_STRING")
    end = data.find(b"\0", offset)
    need(end >= offset and end - offset < 4096, "RVER_ELF_STRING")
    return data[offset:end].decode("utf-8")

def varstring(data, pos):
    size = 0
    for shift in range(0, 70, 7):
        byte = take(data, pos, 1)[0]
        pos += 1
        size |= (byte & 127) << shift
        if not byte & 128:
            need(size <= 2 << 20, "RVER_GO_INFO_SIZE")
            return take(data, pos, size), pos + size
    raise Reject("RVER_GO_INFO_VARINT")

def elf_info(data, binary_name):
    need(data[:7] == b"\x7fELF\x02\x01\x01", "RVER_ELF_TARGET")
    header = unpack("<HHIQQQIHHHHHH", data, 16)
    typ, machine, version, _, phoff, shoff, _, ehsize, phsize, phnum, shsize, shnum, shstr = header
    need(typ in (2, 3) and machine == 62 and version == 1 and ehsize == 64 and
         phsize == 56 and 0 < phnum < 256 and shsize == 64 and 0 < shnum < 4096
         and 0 < shstr < shnum, "RVER_ELF_TARGET")
    sections = [unpack("<IIQQQQIIQQ", data, shoff + i * 64) for i in range(shnum)]
    strings = take(data, sections[shstr][4], sections[shstr][5])
    names = [cstring(strings, s[0]) for s in sections]
    need(len(names) == len(set(names)), "RVER_ELF_DUPLICATE_SECTION")
    segments = []
    for i in range(phnum):
        kind, _, off, addr, _, fsize, msize, _ = unpack("<IIQQQQQQ", data, phoff + i * 56)
        if kind == 1:
            take(data, off, fsize)
            need(fsize <= msize, "RVER_ELF_SEGMENT")
            segments.append((addr, off, fsize))
    def virtual(addr, size):
        matches = [(off + addr - base, size) for base, off, length in segments
                   if base <= addr and size <= length and addr - base <= length - size]
        need(len(matches) == 1, "RVER_ELF_ADDRESS")
        return take(data, *matches[0])
    need(".go.buildinfo" in names, "RVER_GO_INFO_MISSING")
    section = sections[names.index(".go.buildinfo")]
    blob = take(data, section[4], section[5])
    need(blob[:14] == b"\xff Go buildinf:" and blob[14:16] == b"\x08\x02", "RVER_GO_INFO_FORMAT")
    go_version, pos = varstring(blob, 32)
    framed, _ = varstring(blob, pos)
    need(go_version == b"go1.26.9", "RVER_GO_VERSION")
    need(framed.startswith(bytes.fromhex("3077af0c9274080241e1c107e6d618e6")) and
         framed.endswith(bytes.fromhex("f932433186182072008242104116d8f2")), "RVER_GO_FRAMING")
    text = framed[16:-16].decode("utf-8")
    deps, settings, path, main = {}, {}, None, None
    for line in text.splitlines():
        fields = line.split("\t")
        if fields[0] == "path":
            need(path is None and len(fields) == 2, "RVER_GO_PATH")
            path = fields[1]
        elif fields[0] == "mod":
            need(main is None and len(fields) in (3, 4), "RVER_GO_MAIN")
            main = fields[1:3]
        elif fields[0] == "dep":
            need(len(fields) in (3, 4), "RVER_GO_DEP")
            name, ver = fields[1:3]
            need(name not in deps and name in GO and GO[name][0] == ver, "RVER_GO_DEP")
            deps[name] = ver
        elif fields[0] == "build":
            need(len(fields) == 2 and "=" in fields[1], "RVER_GO_SETTING")
            key, value = fields[1].split("=", 1)
            need(key not in settings, "RVER_GO_SETTING")
            settings[key] = value
        else:
            raise Reject("RVER_GO_REPLACE_OR_UNKNOWN")
    need(path == "cpacloud.local/server/cmd/" + binary_name and
         main == ["cpacloud.local/server", "(devel)"], "RVER_GO_PATH")
    need(all(settings.get(k) == v for k, v in
             {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"}.items()),
         "RVER_GO_TARGET")
    need({"modernc.org/sqlite", "golang.org/x/crypto"} <= set(deps), "RVER_GO_DEP")
    versions = []
    for section in sections:
        if section[1] != 2:
            continue
        need(section[9] == 24 and section[5] % 24 == 0 and
             section[6] < len(sections), "RVER_ELF_SYMBOLS")
        string_section = sections[section[6]]
        symbol_strings = take(data, string_section[4], string_section[5])
        for off in range(section[4], section[4] + section[5], 24):
            name, _, _, _, address, size = unpack("<IBBHQQ", data, off)
            if cstring(symbol_strings, name) == "main.version":
                need(size == 16, "RVER_BINARY_VERSION")
                pointer, length = struct.unpack("<QQ", virtual(address, 16))
                need(length == len(VERSION), "RVER_BINARY_VERSION")
                versions.append(virtual(pointer, length))
    need(versions == [VERSION.encode()], "RVER_BINARY_VERSION")
    return deps

def read_archive(data):
    need(len(data) <= 512 << 20 and data[:2] == b"\x1f\x8b", "RVER_ARCHIVE_FORMAT")
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(data)) as stream:
            plain = stream.read(MAX_TOTAL + (32 << 20) + 1)
    except (OSError, EOFError) as exc:
        raise Reject("RVER_ARCHIVE_FORMAT") from exc
    need(len(plain) <= MAX_TOTAL + (32 << 20), "RVER_ARCHIVE_SIZE")
    files, modes, seen, directories, total, end = {}, {}, set(), set(), 0, 0
    with tarfile.open(fileobj=io.BytesIO(plain), mode="r:") as archive:
        for member in archive:
            end = max(end, member.offset_data + ((member.size + 511) // 512) * 512)
            need(len(seen) < 10000, "RVER_ARCHIVE_COUNT")
            name = member.name
            if member.isdir():
                name = name[:-1] if name.endswith("/") else name
            need(name == BASE or name.startswith(BASE + "/"), "RVER_ARCHIVE_ROOT")
            rel = "" if name == BASE else name[len(BASE) + 1:]
            if rel:
                safe(rel)
            key = name.lower()
            need(key not in seen, "RVER_DUPLICATE_PATH")
            seen.add(key)
            need(set(member.pax_headers) <= {"path"} and
                 member.pax_headers.get("path", member.name) == member.name,
                 "RVER_ARCHIVE_PAX")
            if member.isdir():
                need(member.mode == 0o755, "RVER_ARCHIVE_MODE")
                directories.add(rel)
                continue
            need(member.type in (tarfile.REGTYPE, tarfile.AREGTYPE) and rel and
                 not member.issparse(), "RVER_NONREGULAR_ENTRY")
            need(0 <= member.size <= MAX_FILE, "RVER_ARCHIVE_SIZE")
            total += member.size
            need(total <= MAX_TOTAL, "RVER_ARCHIVE_SIZE")
            body = archive.extractfile(member).read(member.size + 1)
            need(len(body) == member.size, "RVER_ARCHIVE_TRUNCATED")
            need(member.mode == (0o755 if rel in BINS else 0o644), "RVER_ARCHIVE_MODE")
            files[rel], modes[rel] = body, member.mode
    need(len(plain) - end >= 1024 and len(plain) % 512 == 0 and not any(plain[end:]),
         "RVER_ARCHIVE_TRAILING")
    need(files, "RVER_ARCHIVE_EMPTY")
    parents = {""}
    for name in files:
        parts = name.split("/")
        parents.update("/".join(parts[:i]) for i in range(1, len(parts)))
    need(directories <= parents and not (set(files) & parents), "RVER_ARCHIVE_DIRECTORY_CLOSURE")
    return files, modes

def inventory(files, modes, exclude=()):
    return [{"path": name, "size": len(data), "sha256": sha(data), "mode": modes[name]}
            for name, data in sorted(files.items()) if name not in exclude]

def validate_file_list(rows):
    need(isinstance(rows, list) and len(rows) < 10000, "RVER_MANIFEST")
    seen = set()
    for row in rows:
        need(isinstance(row, dict) and set(row) == {"path", "size", "sha256", "mode"},
             "RVER_MANIFEST")
        name = safe(row["path"])
        need(name.lower() not in seen and HEX64.fullmatch(row["sha256"]) and
             type(row["size"]) is int and row["size"] >= 0 and
             type(row["mode"]) is int, "RVER_MANIFEST")
        seen.add(name.lower())

def escaped(name):
    return name.replace("/", "_").replace("@", "_")

class Page(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.root, self.scripts, self.base = False, [], False
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        self.root |= tag == "div" and attrs.get("id") == "root"
        self.base |= tag == "base"
        if tag == "script" and attrs.get("type") == "module":
            self.scripts.append(attrs.get("src", ""))

def web_structure(web):
    """SPA entry/reference structure only; no claim about framework execution,
    JavaScript semantics, real compilation or build origin is derived here.
    """
    need("index.html" in web, "RVER_WEB_STRUCTURE")
    page = Page()
    page.feed(web["index.html"].decode("utf-8"))
    need(page.root and page.scripts and not page.base, "RVER_WEB_STRUCTURE")
    scripts = []
    for path in page.scripts:
        need(re.fullmatch(r"/assets/[A-Za-z0-9_-]+\.js", path), "RVER_WEB_STRUCTURE")
        name = path[1:]
        need(name in web and web[name], "RVER_WEB_STRUCTURE")
        scripts.append(web[name])
    return {name: sha(body) for name, body in sorted(web.items())}

def core(files, modes, source):
    need(HEX40.fullmatch(source), "RVER_SOURCE_SHA")
    need(set(BINS + DOCUMENTS + ("GO-DEPENDENCIES.txt",)) <= set(files) and
         any(n.startswith("docs/") and n.endswith(".md") for n in files) and
         files.get("VERSION") == (VERSION + "\n").encode(),
         "RVER_REQUIRED_FILES")
    manifest = decode_json(files.get("MANIFEST.json", b""))
    need(set(manifest) == {"schema", "version", "source_sha", "target", "binaries",
                          "go_modules", "files", "closure"} and
         type(manifest["schema"]) is int and manifest["schema"] == 1 and manifest["version"] == VERSION and
         manifest["source_sha"] == source and manifest["target"] == "linux/amd64",
         "RVER_MANIFEST_IDENTITY")
    validate_file_list(manifest["files"])
    need(manifest["files"] == inventory(files, modes, ("MANIFEST.json", "SHA256SUMS.txt")),
         "RVER_FILE_CLOSURE")
    checksum = "".join(sha(body) + "  " + name + "\n" for name, body in sorted(files.items())
                       if name != "SHA256SUMS.txt").encode()
    need(files.get("SHA256SUMS.txt") == checksum, "RVER_TREE_CHECKSUM")
    info = files.get("BUILD-INFO.txt", b"").decode("utf-8")
    need(all(line in info.splitlines() for line in
             ("Version: " + VERSION, "Commit: " + source, "Target: linux/amd64",
              "Go toolchain: go1.26.9", "Package host: linux/amd64")), "RVER_BUILD_IDENTITY")
    union = {}
    binary_rows = {}
    for row in manifest["binaries"]:
        name = row.get("name")
        need(set(row) == {"name", "package", "version", "sha256", "help_check"} and
             row["help_check"] == "passed (help only, no service or data operation)" and
             name in BINS and name not in binary_rows and
             row.get("package") == "./cmd/" + name and row.get("version") == VERSION and
             row.get("sha256") == sha(files[name]), "RVER_BINARY_MANIFEST")
        binary_rows[name] = row
        for dep, version in elf_info(files[name], name).items():
            union.setdefault(dep, {"path": dep, "version": version,
                                  "binaries": [], "notices": list(GO[dep][1])})["binaries"].append(name)
    need(set(binary_rows) == set(BINS), "RVER_BINARY_MANIFEST")
    for row in union.values():
        row["binaries"].sort()
    need(manifest["go_modules"] == [union[k] for k in sorted(union)], "RVER_GO_UNION")
    license_paths = {"THIRD-PARTY-LICENSES/go-runtime/" + n for n in ("LICENSE", "PATENTS", "VERSION")}
    for dep, row in union.items():
        prefix = "THIRD-PARTY-LICENSES/go-modules/" + escaped(dep + "@" + row["version"]) + "/"
        license_paths.update(prefix + n for n in row["notices"])
    metadata_paths = set()
    for dep, (version, notices) in NPM.items():
        prefix = "THIRD-PARTY-LICENSES/npm/" + escaped(dep + "@" + version) + "/"
        metadata_paths.add(prefix + "package.json")
        metadata = decode_json(files.get(prefix + "package.json", b""))
        need(all(metadata.get(k) == v for k, v in
                 {"name": dep, "version": version, "license": "MIT"}.items()), "RVER_NPM_IDENTITY")
        license_paths.update(prefix + n for n in notices)
        for notice in notices:
            path = prefix + notice
            need(files.get(path), "RVER_NOTICE_MISSING")
            if dep == "rolldown":
                need(sha(files[path]) == ROLLDOWN_HASHES[notice], "RVER_NOTICE_CONTENT")
    actual_notices = {n for n in files if n.startswith("THIRD-PARTY-LICENSES/") and
                      not n.endswith("/package.json")}
    need(actual_notices == license_paths and all(files.get(n) for n in license_paths),
         "RVER_NOTICE_CLOSURE")
    fixed = set(BINS + DOCUMENTS + ("VERSION", "BUILD-INFO.txt", "GO-DEPENDENCIES.txt",
                "FRONTEND-DEPENDENCIES.txt", "WEB-ASSETS.json", "MANIFEST.json", "SHA256SUMS.txt"))
    allowed = fixed | license_paths | metadata_paths
    allowed.update(n for n in files if n.startswith("docs/") and n.endswith(".md"))
    allowed.update(n for n in files if n.startswith("web/"))
    need(set(files) == allowed, "RVER_PAYLOAD_NAMESPACE")
    npm_prefix = "THIRD-PARTY-LICENSES/npm/"
    web = {n[4:]: b for n, b in files.items() if n.startswith("web/")}
    web_hashes = web_structure(web)
    wm = decode_json(files.get("WEB-ASSETS.json", b""))
    need(wm.get("schema") == 1 and wm.get("source_lock_sha256") == LOCK, "RVER_WEB_LOCK")
    wf = {n: b for n, b in files.items() if n.startswith(("web/", npm_prefix)) or
          n == "FRONTEND-DEPENDENCIES.txt"}
    validate_file_list(wm.get("files"))
    need(wm["files"] == inventory(wf, modes), "RVER_WEB_CLOSURE")
    notice_index = files.get("FRONTEND-DEPENDENCIES.txt", b"").decode("utf-8")
    need(LOCK in notice_index and all(dep + "@" + version in notice_index
         for dep, (version, _) in NPM.items()) and
         "THIRD-PARTY-LICENSE" in notice_index, "RVER_NOTICE_INDEX")
    return {"binaries": {n: sha(files[n]) for n in BINS},
            "go_modules": [union[k] for k in sorted(union)],
            "license_files": {n: sha(files[n]) for n in sorted(license_paths)},
            "documentation_files": {n: sha(b) for n, b in sorted(files.items())
                                    if n in DOCUMENTS or n.startswith("docs/")},
            "web_files": web_hashes, "ordinary_files": len(files)}

def disk_tree(root):
    root = Path(root)
    no_links(root)
    need(root.is_dir(), "RVER_REAL_WEB")
    result = {}
    for path in sorted(root.rglob("*")):
        no_links(path)
        if path.is_dir():
            continue
        name = safe(path.relative_to(root).as_posix())
        result[name] = read_file(path)
    return result

def physical_sources(root, go_root, mod_cache, files, result):
    root, go_root, mod_cache = Path(root), Path(go_root), Path(mod_cache)
    need(sha(read_file(root / "web/bun.lock").replace(b"\r\n", b"\n")) == LOCK, "RVER_SOURCE_LOCK")
    real_web = disk_tree(root / "web/dist")
    need(web_structure(real_web) == result["web_files"] and all(
         files["web/" + n] == b for n, b in real_web.items()), "RVER_WEB_SOURCE")
    docs = set(DOCUMENTS)
    docs.update(n.decode("utf-8") for n in run_git(root, "ls-files", "-z", "--", "docs").split(b"\0") if n)
    need(docs == set(result["documentation_files"]) and all(
         files[n] == read_file(root / n) for n in docs), "RVER_DOCUMENT_SOURCE")
    expected = {}
    for n in ("LICENSE", "PATENTS", "VERSION"):
        expected["THIRD-PARTY-LICENSES/go-runtime/" + n] = read_file(go_root / n)
    for row in result["go_modules"]:
        dep, version = row["path"], row["version"]
        for notice in GO[dep][1]:
            expected["THIRD-PARTY-LICENSES/go-modules/" + escaped(dep + "@" + version) + "/" + notice] = read_file(mod_cache / (dep + "@" + version) / notice)
    for dep, (version, notices) in NPM.items():
        source = root / "web/node_modules" / dep
        metadata = decode_json(read_file(source / "package.json"))
        need(metadata.get("name") == dep and metadata.get("version") == version and
             metadata.get("license") == "MIT", "RVER_NPM_SOURCE")
        need(files["THIRD-PARTY-LICENSES/npm/" + escaped(dep + "@" + version) + "/package.json"] ==
             read_file(source / "package.json"), "RVER_NPM_SOURCE")
        for notice in notices:
            expected["THIRD-PARTY-LICENSES/npm/" + escaped(dep + "@" + version) + "/" + notice] = read_file(source / notice)
    need(set(expected) == set(result["license_files"]) and all(
         files[n] == body for n, body in expected.items()), "RVER_PHYSICAL_NOTICE")
    for path, digest in HELPERS.items():
        need(sha(read_file(root / "web/node_modules" / path)) == digest, "RVER_HELPER_SOURCE")
    return dict(HELPERS)

def verify(archive_path, sidecar_path, source, source_root=None, go_root=None, mod_cache=None,
           proof=None, expected_archive=None, expected_sidecar=None, web_receipt=None,
           producer_run=None):
    need(Path(archive_path).name == ARCHIVE and Path(sidecar_path).name == SIDECAR, "RVER_ASSET_NAME")
    raw, sidecar = read_file(archive_path, 512 << 20), read_file(sidecar_path, 256)
    archive_hash, sidecar_hash = sha(raw), sha(sidecar)
    need(sidecar == (archive_hash + "  " + ARCHIVE + "\n").encode(), "RVER_SIDECAR")
    if expected_archive is not None:
        need(HEX64.fullmatch(expected_archive) and archive_hash == expected_archive, "RVER_FROZEN_ARCHIVE")
        need(HEX64.fullmatch(expected_sidecar or "") and sidecar_hash == expected_sidecar, "RVER_FROZEN_SIDECAR")
    files, modes = read_archive(raw)
    result = core(files, modes, source)
    if source_root is None:
        need(isinstance(proof, dict), "RVER_FROZEN_PROOF")
        web_receipt = proof.get("web_build_receipt")
        producer_run = proof.get("producer_run")
    report = {"schema": 2, "version": VERSION, "source_sha": source, "target": "linux/amd64",
              "archive_sha256": archive_hash, "sidecar_sha256": sidecar_hash,
              "web_structure_checked": True, "web_build_receipt_checked": False,
              "web_build_receipt": web_receipt, "producer_run": producer_run,
              "source_lock_sha256": LOCK, **result}
    if source_root is not None:
        need(go_root is not None and mod_cache is not None, "RVER_SOURCE_ARGUMENTS")
        evidence = verify_source(source_root, source)
        report["helper_sources"] = physical_sources(source_root, go_root, mod_cache, files, result)
        if web_receipt is not None:
            validate_web_receipt(web_receipt, source, result["web_files"], evidence["web_inputs"])
            report["web_build_receipt_checked"] = True
    else:
        need(isinstance(proof, dict) and proof.get("helper_sources") == HELPERS, "RVER_FROZEN_PROOF")
        report["helper_sources"] = dict(HELPERS)
        if web_receipt is not None:
            validate_web_receipt(web_receipt, source, result["web_files"])
            report["web_build_receipt_checked"] = True
        need(report == proof, "RVER_FROZEN_PROOF")
    return report

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    source = commands.add_parser("source")
    source.add_argument("--root", required=True)
    source.add_argument("--source-sha", required=True)
    build = commands.add_parser("web-build")
    build.add_argument("--root", required=True)
    build.add_argument("--source-sha", required=True)
    build.add_argument("--receipt", required=True)
    package = commands.add_parser("verify")
    for name in ("archive", "sidecar", "source-sha"):
        package.add_argument("--" + name, required=True)
    package.add_argument("--source-root")
    package.add_argument("--go-root")
    package.add_argument("--module-cache")
    package.add_argument("--web-build-receipt")
    package.add_argument("--proof")
    package.add_argument("--expected-proof-sha")
    package.add_argument("--expected-archive-sha")
    package.add_argument("--expected-sidecar-sha")
    args = parser.parse_args(argv)
    if args.command == "source":
        result = verify_source(args.root, args.source_sha)
    elif args.command == "web-build":
        receipt = build_web(args.root, args.source_sha, args.receipt)
        result = {"kind": receipt["kind"], "source_sha": receipt["source_sha"],
                  "output_files": len(receipt["outputs"])}
    else:
        need(bool(args.source_root) != bool(args.proof), "RVER_MODE")
        proof = None
        if args.proof:
            body = read_file(args.proof, 8 << 20)
            need(HEX64.fullmatch(args.expected_proof_sha or "") and
                 sha(body) == args.expected_proof_sha and
                 args.expected_archive_sha and args.expected_sidecar_sha, "RVER_PROOF_DIGEST")
            proof = decode_json(body)
        receipt = None
        producer_run = None
        if args.source_root:
            need(args.web_build_receipt, "RVER_WEB_RECEIPT_REQUIRED")
            receipt = decode_json(read_file(args.web_build_receipt, 8 << 20))
            need(isinstance(receipt, dict), "RVER_WEB_RECEIPT")
            run_id, attempt = os.environ.get("GITHUB_RUN_ID", ""), os.environ.get("GITHUB_RUN_ATTEMPT", "")
            event = os.environ.get("GITHUB_EVENT_NAME", "")
            need(re.fullmatch(r"[1-9][0-9]*", run_id) and attempt == "1" and
                 event in ("pull_request", "push"), "RVER_PRODUCER_RUN")
            producer_run = {"run_id": run_id, "attempt": attempt, "event": event,
                            "source_sha": args.source_sha}
        result = verify(args.archive, args.sidecar, args.source_sha, args.source_root,
                        args.go_root, args.module_cache, proof,
                        args.expected_archive_sha, args.expected_sidecar_sha, receipt, producer_run)
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))

if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error) if isinstance(error, Reject) else "RVER_INVALID_INPUT", file=sys.stderr)
        sys.exit(1)
