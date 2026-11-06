#!/usr/bin/env python3
"""Independent workflow/verifier contract tests, authored from this batch's
bounded specification and Python/GitHub public APIs. All archives, keys and
clients below are public synthetic fixtures. Namespace tests deliberately stub
ELF/license-content pins; they do not attest compiled ELF or a real Web build.
No GitHub request, binary, service or database is executed.
"""
import contextlib
import copy
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("bounded_reader", ROOT / "scripts/verify-release-package.py")
v = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(v)
SOURCE = "a" * 40

def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()

def package_fixture():
    files = {n: b"public fixture document\n" for n in v.DOCUMENTS}
    files.update({n: b"ELF boundary stub, never executed" for n in v.BINS})
    files.update({"docs/contract.md": b"public fixture contract\n",
                  "GO-DEPENDENCIES.txt": b"public linked module fixture\n",
                  "VERSION": (v.VERSION + "\n").encode(),
                  "BUILD-INFO.txt": ("\n".join(("Version: " + v.VERSION, "Commit: " + SOURCE,
                       "Target: linux/amd64", "Go toolchain: go1.26.9",
                       "Package host: linux/amd64")) + "\n").encode(),
                  "web/index.html": b'<div id="root"></div><script type="module" src="/assets/app.js"></script>',
                  "web/assets/app.js": b"/* modulepreload MutationObserver react.transitional.element react.portal " + b"x" * 20000 + b" */"})
    deps = ("golang.org/x/crypto", "modernc.org/sqlite")
    for n in ("LICENSE", "PATENTS", "VERSION"):
        files["THIRD-PARTY-LICENSES/go-runtime/" + n] = b"public runtime notice\n"
    for dep in deps:
        version, notices = v.GO[dep]
        for n in notices:
            files["THIRD-PARTY-LICENSES/go-modules/" + v.escaped(dep + "@" + version) + "/" + n] = b"public module notice\n"
    for dep, (version, notices) in v.NPM.items():
        prefix = "THIRD-PARTY-LICENSES/npm/" + v.escaped(dep + "@" + version) + "/"
        files[prefix + "package.json"] = encoded({"name": dep, "version": version, "license": "MIT"})
        for n in notices:
            files[prefix + n] = b"public synthetic notice boundary\n"
    files["FRONTEND-DEPENDENCIES.txt"] = (v.LOCK + "\nTHIRD-PARTY-LICENSE\n" +
        "\n".join(n + "@" + row[0] for n, row in v.NPM.items())).encode()
    modules = [{"path": dep, "version": v.GO[dep][0], "binaries": sorted(v.BINS),
                "notices": list(v.GO[dep][1])} for dep in sorted(deps)]
    manifest = {"schema": 1, "version": v.VERSION, "source_sha": SOURCE, "target": "linux/amd64",
                "binaries": [{"name": n, "package": "./cmd/" + n, "version": v.VERSION,
                    "sha256": v.sha(files[n]), "help_check": "passed (help only, no service or data operation)"}
                    for n in v.BINS],
                "go_modules": modules, "files": [], "closure": "public fixture boundary"}
    reseal(files, manifest)
    return files, manifest

def reseal(files, manifest):
    modes = {n: 0o755 if n in v.BINS else 0o644 for n in files}
    selected = {n: b for n, b in files.items() if n.startswith(("web/", "THIRD-PARTY-LICENSES/npm/"))
                or n == "FRONTEND-DEPENDENCIES.txt"}
    files["WEB-ASSETS.json"] = encoded({"schema": 1, "source_lock_sha256": v.LOCK,
                                      "files": v.inventory(selected, modes)})
    modes["WEB-ASSETS.json"] = 0o644
    manifest["files"] = v.inventory(files, modes, ("MANIFEST.json", "SHA256SUMS.txt"))
    files["MANIFEST.json"] = encoded(manifest)
    files["SHA256SUMS.txt"] = "".join(v.sha(b) + "  " + n + "\n" for n, b in sorted(files.items())
                                    if n != "SHA256SUMS.txt").encode()
    return {n: 0o755 if n in v.BINS else 0o644 for n in files}

def archive_bytes(entries):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w:gz", format=tarfile.USTAR_FORMAT) as archive:
        for name, body, mode in entries:
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(body), mode
            archive.addfile(info, io.BytesIO(body))
    return out.getvalue()

def fixture_core(files):
    modes = {n: 0o755 if n in v.BINS else 0o644 for n in files}
    hashes = {n: v.sha(b"public synthetic notice boundary\n") for n in v.ROLLDOWN_HASHES}
    with patch.object(v, "elf_info", return_value={n: v.GO[n][0] for n in ("golang.org/x/crypto", "modernc.org/sqlite")}), \
         patch.object(v, "ROLLDOWN_HASHES", hashes):
        return v.core(files, modes, SOURCE)

def publisher():
    raw = (ROOT / ".github/workflows/release-server.yml").read_text(encoding="utf-8")
    try:
        workflow = json.loads(raw)
    except json.JSONDecodeError:
        # Read only the known old heredoc. This is not a YAML parser or validation.
        body = raw.rsplit("          python3 - <<'PY'\n", 1)[1].split("          PY\n", 1)[0]
        source = textwrap.dedent(body)
    else:
        script = workflow["jobs"]["publish"]["steps"][-1]["run"]
        source = script.split("python3 - <<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
    namespace = {"__name__": "synthetic_publisher_test"}
    exec(compile(source, "inline-release-publisher", "exec"), namespace)
    return namespace

class FakeClient:
    """In-memory only; every attempted mutation/readback is observable."""
    def __init__(self, lost_patch=False, present=False, bad_download=False):
        self.calls, self.uploaded, self.downloads = [], {}, []
        self.tag, self.draft = False, True
        self.lost_patch, self.present, self.bad_download = lost_patch, present, bad_download
    def release(self):
        result = {"id": 41, "tag_name": v.VERSION, "name": "CPA Cloud " + v.VERSION,
                  "target_commitish": SOURCE, "body": "v0.1.0 public fixture notes",
                  "draft": self.draft, "prerelease": False}
        if not self.draft:
            result["published_at"] = "2026-11-06T00:00:00Z"
        return result
    def api(self, method, path, payload=None, allowed=(200,)):
        self.calls.append((method, path, payload))
        if method == "GET":
            if path == "":
                return 200, {"full_name": "surpaimb/cpa-cloud", "default_branch": "main"}
            if path == "/git/ref/heads/main":
                return 200, {"ref": "refs/heads/main", "object": {"type": "commit", "sha": SOURCE}}
            if path == "/git/ref/tags/" + v.VERSION:
                if self.tag or self.present:
                    return 200, {"ref": "refs/tags/" + v.VERSION, "object": {"type": "commit", "sha": SOURCE}}
                return 404, {"message": "Not Found"}
            if path.startswith("/git/matching-refs/"):
                return 200, []
            if path == "/releases/tags/" + v.VERSION:
                return 404, {"message": "Not Found"}
            if path == "/releases/41":
                return 200, self.release()
        if method == "POST" and path == "/git/refs":
            self.tag = True
            return 201, {"object": {"sha": SOURCE}}
        if method == "POST" and path == "/releases":
            return 201, self.release()
        if method == "PATCH" and path == "/releases/41":
            self.draft = False
            if self.lost_patch:
                raise OSError("public synthetic response loss")
            return 200, self.release()
        raise AssertionError("RVER_FIXTURE_UNEXPECTED_REQUEST")
    def pages(self, path):
        if path == "/releases":
            return []
        if path == "/releases/41/assets":
            return [{"id": row[0], "name": name, "state": "uploaded", "size": len(row[1])}
                    for name, row in self.uploaded.items()]
        raise AssertionError("RVER_FIXTURE_UNEXPECTED_PAGE")
    def upload(self, release_id, name, data):
        self.calls.append(("UPLOAD", name, None))
        identity = 81 + len(self.uploaded)
        self.uploaded[name] = (identity, data)
        return {"id": identity, "name": name, "state": "uploaded", "size": len(data)}
    def download(self, identity, size):
        self.downloads.append(identity)
        body = next(body for asset_id, body in self.uploaded.values() if asset_id == identity)
        return b"changed" if self.bad_download else body

def publish_fixture(client):
    namespace = publisher()
    with tempfile.TemporaryDirectory(prefix="release-publisher-fixture-") as tmp:
        payload = Path(tmp)
        for name in (v.ARCHIVE, v.SIDECAR):
            (payload / name).write_bytes(("public fixture " + name).encode())
        hashes = {n: v.sha((payload / n).read_bytes()) for n in (v.ARCHIVE, v.SIDECAR)}
        sizes = {n: (payload / n).stat().st_size for n in (v.ARCHIVE, v.SIDECAR)}
        with patch.object(namespace["v"], "verify", return_value={}), contextlib.redirect_stdout(io.StringIO()):
            namespace["publish"](client, SOURCE, "v0.1.0 public fixture notes", payload, {}, hashes, sizes)

def git_fixture(root):
    env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
               GIT_CONFIG_COUNT="0", GIT_TERMINAL_PROMPT="0")
    def git(*args):
        result = subprocess.run(["git", *args], cwd=root, env=env, capture_output=True, check=True)
        return result.stdout.decode().strip()
    git("init", "-q")
    git("config", "core.autocrlf", "false")
    git("config", "core.hooksPath", str(root / "no-hooks"))
    for name, body in {".gitignore": "bin/\n", "web/src/main.ts": "export const value = 1\n",
                       "web/public/logo.svg": "<svg/>\n", "web/package.json": "{}\n",
                       "web/bun.lock": "public lock fixture\n", "web/index.html": "<div id=\"root\"></div>\n",
                       "web/vite.config.ts": "export default {}\n"}.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body.encode())
    git("add", "--all")
    git("-c", "user.name=Public fixture", "-c", "user.email=fixture@example.invalid",
        "-c", "commit.gpgSign=false", "commit", "-qm", "Public Web fixture")
    return git("rev-parse", "HEAD")

class TestOldOracle(unittest.TestCase):
    def test_canonical_tar_control(self):
        files, _ = v.read_archive(archive_bytes([(v.BASE + "/web/assets/app.js", b"first", 0o644)]))
        self.assertEqual(files, {"web/assets/app.js": b"first"})
    def test_tar_separator_alias(self):
        raw = archive_bytes([(v.BASE + "/web/assets/app.js", b"first", 0o644),
                             (v.BASE + "//web/assets/app.js", b"second", 0o644)])
        try:
            v.read_archive(raw)
        except v.Reject:
            return
        self.fail("RVER_OLD_TAR_ALIAS_ACCEPTED")
    def test_closed_namespace_control(self):
        files, _ = package_fixture()
        self.assertEqual(fixture_core(files)["ordinary_files"], len(files))
    def test_unknown_ordinary(self):
        files, manifest = package_fixture()
        files["unexpected-tool.txt"] = b"public extra"
        reseal(files, manifest)
        try:
            fixture_core(files)
        except v.Reject:
            return
        self.fail("RVER_OLD_UNKNOWN_ORDINARY_ACCEPTED")
    def test_unknown_npm_metadata(self):
        files, manifest = package_fixture()
        files["THIRD-PARTY-LICENSES/npm/unknown_1.0.0/package.json"] = b'{"name":"unknown"}'
        reseal(files, manifest)
        try:
            fixture_core(files)
        except v.Reject:
            return
        self.fail("RVER_OLD_UNKNOWN_NPM_METADATA_ACCEPTED")
    def test_comment_markers_are_not_build_evidence(self):
        files, _ = package_fixture()
        raw = archive_bytes([(v.BASE + "/" + n, b, 0o755 if n in v.BINS else 0o644)
                             for n, b in files.items()])
        with tempfile.TemporaryDirectory(prefix="release-web-evidence-fixture-") as tmp:
            root = Path(tmp)
            (root / v.ARCHIVE).write_bytes(raw)
            (root / v.SIDECAR).write_bytes((v.sha(raw) + "  " + v.ARCHIVE + "\n").encode())
            with patch.object(v, "core", side_effect=lambda f, m, s: fixture_core_original(f, m, s)), \
                 patch.object(v, "verify_source", return_value={}), \
                 patch.object(v, "physical_sources", return_value=dict(v.HELPERS)):
                report = v.verify(root / v.ARCHIVE, root / v.SIDECAR, SOURCE,
                                  source_root=root, go_root=root, mod_cache=root)
        self.assertIs(report.get("web_build_receipt_checked"), False, "RWF_COMMENT_WEB_BUILD_FLAG")
        self.assertIs(report.get("web_structure_checked"), True)
    def test_ignored_public_input(self):
        with tempfile.TemporaryDirectory(prefix="release-source-fixture-") as tmp:
            root = Path(tmp)
            source = git_fixture(root)
            v.verify_source(root, source)
            extra = root / "web/public/bin/injected.txt"
            extra.parent.mkdir(parents=True)
            extra.write_bytes(b"public ignored input copied by Vite")
            try:
                v.verify_source(root, source)
            except v.Reject:
                return
            self.fail("RVER_OLD_IGNORED_PUBLIC_ACCEPTED")
    def test_publication_response_loss(self):
        client = FakeClient(lost_patch=True)
        try:
            publish_fixture(client)
        except Exception as error:
            if str(error) == "RREL_PUBLICATION_UNKNOWN":
                self.assertEqual(sum(method == "PATCH" for method, _, _ in client.calls), 1)
                self.assertFalse(client.draft)
                return
            self.fail("RVER_OLD_PUBLICATION_LOSS_UNCLASSIFIED")
        self.fail("RVER_OLD_PUBLICATION_LOSS_ACCEPTED")
    def test_publisher_control(self):
        client = FakeClient()
        publish_fixture(client)
        self.assertFalse(client.draft)
        self.assertEqual(client.downloads, [81, 82])
        self.assertEqual(sum(method == "PATCH" for method, _, _ in client.calls), 1)

# Capture the original function so a bounded ELF/content stub cannot recurse.
CORE = v.core
def fixture_core_original(files, modes, source):
    hashes = {n: v.sha(b"public synthetic notice boundary\n") for n in v.ROLLDOWN_HASHES}
    with patch.object(v, "elf_info", return_value={n: v.GO[n][0] for n in ("golang.org/x/crypto", "modernc.org/sqlite")}), \
         patch.object(v, "ROLLDOWN_HASHES", hashes):
        return CORE(files, modes, source)

def exercise_producer(case, failed_stage=None, mutate=None):
    """Controlled executor only: no real Bun, Git request, install or Web build.
    Independent fixture bytes act as HEAD; successful execution is simulated.
    """
    with tempfile.TemporaryDirectory(prefix="release-producer-unit-") as tmp:
        base, root = Path(tmp), Path(tmp) / "source"
        payload = {"web/bun.lock": b"public frozen lock fixture\n",
                   "web/package.json": b'{"name":"public-fixture"}\n',
                   "web/src/main.ts": b"export const publicFixture = 1\n"}
        for name, body in payload.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        inputs = {name: v.sha(body) for name, body in payload.items()}
        cache, receipt_path, calls = base / "cache", base / "receipt.json", []
        cache.mkdir()
        def source_check(source_root, source_sha):
            case.assertEqual(Path(source_root), root)
            case.assertEqual(source_sha, SOURCE)
            for name, body in payload.items():
                v.need((root / name).read_bytes() == body, "RWF_CONTROLLED_HEAD_MISMATCH")
            return {"web_inputs": dict(inputs)}
        def blob(source_root, *args):
            case.assertEqual(args[:2], ("cat-file", "blob"))
            case.assertTrue(args[2].startswith(SOURCE + ":"))
            return payload[args[2].split(":", 1)[1]]
        def version(command, **kwargs):
            case.assertEqual(command, ["bun", "--version"])
            return b"1.3.14\n"
        def execute(command, **kwargs):
            stage = len(calls)
            case.assertEqual(command, v.WEB_COMMANDS[stage])
            calls.append(list(command))
            private = Path(kwargs["cwd"]).parent
            case.assertNotEqual(private, root)
            case.assertNotIn("GH_TOKEN", kwargs["env"])
            for name, body in payload.items():
                case.assertEqual((private / name).read_bytes(), body)
            if failed_stage == stage:
                raise subprocess.CalledProcessError(27, command)
            if stage == 3:
                if mutate is not None:
                    (private / mutate).write_bytes(b"public controlled changed input")
                dist = private / "web/dist"
                (dist / "assets").mkdir(parents=True)
                (dist / "index.html").write_bytes(
                    b'<div id="root"></div><script type="module" src="/assets/app.js"></script>')
                (dist / "assets/app.js").write_bytes(b"/* public inert fixture */")
        with patch.object(v, "verify_source", side_effect=source_check), \
             patch.object(v, "run_git", side_effect=blob), \
             patch.object(v, "LOCK", v.sha(payload["web/bun.lock"])), \
             patch.object(v.subprocess, "check_output", side_effect=version), \
             patch.object(v.subprocess, "run", side_effect=execute), \
             patch.dict(os.environ, {"BUN_INSTALL_CACHE_DIR": str(cache)}):
            if failed_stage is not None:
                with case.assertRaises(subprocess.CalledProcessError):
                    v.build_web(root, SOURCE, receipt_path)
                case.assertEqual(len(calls), failed_stage + 1)
            elif mutate is not None:
                with case.assertRaisesRegex(v.Reject, "RVER_WEB_INPUT_CHANGED"):
                    v.build_web(root, SOURCE, receipt_path)
            else:
                receipt = v.build_web(root, SOURCE, receipt_path)
                case.assertEqual(v.decode_json(receipt_path.read_bytes()), receipt)
                case.assertEqual(receipt["inputs"], inputs)
                case.assertEqual(receipt["commands"], calls)
                case.assertEqual(receipt["outputs"], {n: v.sha(b) for n, b in v.disk_tree(root / "web/dist").items()})
        if failed_stage is not None or mutate is not None:
            case.assertFalse(receipt_path.exists())
            case.assertFalse((root / "web/dist").exists())
        for name, body in payload.items():
            case.assertEqual((root / name).read_bytes(), body)

class TestProducerContract(unittest.TestCase):
    def test_bun_1314_command_contract(self):
        self.assertEqual(v.WEB_COMMANDS[0], ["bun", "install", "--frozen-lockfile"],
                         "RWF_OLD_UNSUPPORTED_BUN_OFFLINE")
        self.assertEqual(v.WEB_COMMANDS[1:],
                         [["bun", "run", name] for name in ("typecheck", "test", "build")])

    def test_receipt_rejects_missing_and_wrong_fields(self):
        inputs = {"web/package.json": v.sha(b"public input")}
        outputs = {"index.html": v.sha(b"public output")}
        receipt = {"schema": 1, "kind": "producer-observed-private-head-web-build",
                   "source_sha": SOURCE, "bun_version": "1.3.14",
                   "commands": copy.deepcopy(v.WEB_COMMANDS), "inputs": inputs, "outputs": outputs}
        v.validate_web_receipt(receipt, SOURCE, outputs, inputs)
        for absent in (None, {}):
            with self.assertRaises(v.Reject):
                v.validate_web_receipt(absent, SOURCE, outputs, inputs)
        wrong = {"source_sha": "b" * 40, "inputs": {"web/package.json": v.sha(b"changed")},
                 "outputs": {"index.html": v.sha(b"changed")}, "commands": [["bun", "run", "build"]]}
        for field in wrong:
            with self.subTest(field=field, variant="missing"):
                changed = copy.deepcopy(receipt)
                del changed[field]
                with self.assertRaises(v.Reject):
                    v.validate_web_receipt(changed, SOURCE, outputs, inputs)
            with self.subTest(field=field, variant="wrong"):
                changed = copy.deepcopy(receipt)
                changed[field] = wrong[field]
                with self.assertRaises(v.Reject):
                    v.validate_web_receipt(changed, SOURCE, outputs, inputs)
        for source in ("", None, "b" * 40):
            with self.subTest(source=source):
                with self.assertRaises(v.Reject):
                    v.validate_web_receipt(receipt, source, outputs, inputs)

    def test_receipt_field_types_are_rejected(self):
        inputs = {"web/package.json": v.sha(b"public input")}
        outputs = {"index.html": v.sha(b"public output")}
        receipt = {"schema": 1, "kind": "producer-observed-private-head-web-build",
                   "source_sha": SOURCE, "bun_version": "1.3.14",
                   "commands": copy.deepcopy(v.WEB_COMMANDS), "inputs": inputs, "outputs": outputs}
        wrong = {"schema": (True, "1", None), "kind": (None, 1, []),
                 "source_sha": (None, 1, True, []), "bun_version": (None, 1),
                 "inputs": (None, [], "public", {"web/package.json": False}, {123: v.sha(b"x")}),
                 "outputs": (None, [], True, {"index.html": None}),
                 "commands": (None, {}, "bun install", ["bun", "install"])}
        for field, values in wrong.items():
            for index, value in enumerate(values):
                with self.subTest(field=field, variant=index):
                    changed = copy.deepcopy(receipt)
                    changed[field] = value
                    with self.assertRaises(v.Reject):
                        v.validate_web_receipt(changed, SOURCE, outputs, inputs)

    def test_controlled_producer_success_records_input_and_output_hashes(self):
        exercise_producer(self)

    def test_producer_command_failure_emits_no_success_receipt(self):
        for stage in range(4):
            with self.subTest(stage=stage):
                exercise_producer(self, failed_stage=stage)

    def test_producer_changed_lock_or_source_emits_no_success_receipt(self):
        for name in ("web/bun.lock", "web/src/main.ts"):
            with self.subTest(input=name):
                exercise_producer(self, mutate=name)

class TestCLIReceipt(unittest.TestCase):
    def run_main_receipt(self, receipt):
        # Real CLI parsing and verification over synthetic package bytes only.
        files, _ = package_fixture()
        raw = archive_bytes([(v.BASE + "/" + name, body, 0o755 if name in v.BINS else 0o644)
                             for name, body in files.items()])
        inputs = {"web/package.json": v.sha(b"public input")}
        if callable(receipt):
            receipt = receipt({name[4:]: v.sha(body) for name, body in files.items()
                               if name.startswith("web/")}, inputs)
        with tempfile.TemporaryDirectory(prefix="release-cli-receipt-fixture-") as tmp:
            root = Path(tmp)
            (root / v.ARCHIVE).write_bytes(raw)
            (root / v.SIDECAR).write_bytes((v.sha(raw) + "  " + v.ARCHIVE + "\n").encode())
            receipt_path = root / "web-receipt.json"
            receipt_path.write_bytes(encoded(receipt))
            output = io.StringIO()
            with patch.object(v, "core", side_effect=fixture_core_original), \
                 patch.object(v, "verify_source", return_value={"web_inputs": inputs}), \
                 patch.object(v, "physical_sources", return_value=dict(v.HELPERS)), \
                 patch.dict(os.environ, {"GITHUB_RUN_ID": "41", "GITHUB_RUN_ATTEMPT": "1",
                                         "GITHUB_EVENT_NAME": "pull_request"}), \
                 contextlib.redirect_stdout(output):
                v.main(["verify", "--archive", str(root / v.ARCHIVE),
                        "--sidecar", str(root / v.SIDECAR), "--source-sha", SOURCE,
                        "--source-root", str(root), "--go-root", str(root),
                        "--module-cache", str(root), "--web-build-receipt", str(receipt_path)])
            return json.loads(output.getvalue())

    def test_json_null_receipt_is_rejected(self):
        try:
            report = self.run_main_receipt(None)
        except v.Reject as error:
            self.assertEqual(str(error), "RVER_WEB_RECEIPT")
            return
        self.assertIs(report["web_build_receipt_checked"], False)
        self.fail("RVER_OLD_CLI_NULL_RECEIPT_ACCEPTED")

    def test_other_non_object_receipts_are_rejected(self):
        for value in ([], ["public"], True, False, 0, 1, "", "public"):
            with self.subTest(value=value):
                with self.assertRaisesRegex(v.Reject, r"^RVER_WEB_RECEIPT$"):
                    self.run_main_receipt(value)

    def test_valid_receipt_object_is_checked(self):
        def receipt(outputs, inputs):
            return {"schema": 1, "kind": "producer-observed-private-head-web-build",
                    "source_sha": SOURCE, "bun_version": "1.3.14",
                    "commands": copy.deepcopy(v.WEB_COMMANDS), "inputs": inputs,
                    "outputs": outputs}
        report = self.run_main_receipt(receipt)
        self.assertIs(report["web_build_receipt_checked"], True)
        self.assertEqual(report["web_build_receipt"]["source_sha"], SOURCE)

class TestPublisherFaults(unittest.TestCase):
    def test_pull_request_event_stops_before_client_creation(self):
        namespace = publisher()
        def forbidden_client(*args):
            raise AssertionError("RWF_PR_CLIENT_CREATED")
        event = {"GITHUB_EVENT_NAME": "pull_request", "GITHUB_REF": "refs/pull/136/merge",
                 "GITHUB_REPOSITORY": "surpaimb/cpa-cloud", "GITHUB_RUN_ATTEMPT": "1"}
        with patch.dict(namespace, {"Client": forbidden_client}), patch.dict(os.environ, event, clear=True):
            with self.assertRaisesRegex(RuntimeError, "RREL_EVENT_REJECTED"):
                namespace["main"]()

    def test_download_corruption_never_reaches_publish_patch(self):
        client = FakeClient(bad_download=True)
        with self.assertRaisesRegex(RuntimeError, "RREL_DOWNLOADED_HASH"):
            publish_fixture(client)
        self.assertEqual(client.downloads, [81])
        self.assertFalse(any(method == "PATCH" for method, _, _ in client.calls))

    def test_bad_publication_readback_is_unknown_without_retry(self):
        class WrongReadback(FakeClient):
            def api(self, method, path, payload=None, allowed=(200,)):
                result = super().api(method, path, payload, allowed)
                return (200, {"draft": True}) if method == "PATCH" else result
        client = WrongReadback()
        with self.assertRaisesRegex(RuntimeError, "RREL_PUBLICATION_UNKNOWN"):
            publish_fixture(client)
        self.assertEqual(sum(method == "PATCH" for method, _, _ in client.calls), 1)
        self.assertFalse(client.draft)

class TestWorkflowStaticContract(unittest.TestCase):
    def workflow(self):
        # Basic JSON-subset check; full YAML/GitHub schema parsing stays separate.
        return v.decode_json((ROOT / ".github/workflows/release-server.yml").read_bytes())

    def test_permissions_event_gate_and_exact_artifact(self):
        workflow = self.workflow()
        self.assertEqual(set(workflow["on"]), {"pull_request", "push"})
        self.assertEqual(workflow["on"]["push"]["branches"], ["main"])
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        self.assertEqual(workflow["jobs"]["build"]["permissions"], {"contents": "read"})
        publish = workflow["jobs"]["publish"]
        self.assertEqual(publish["permissions"], {"contents": "write", "actions": "read"})
        for text in ("github.event_name == 'push'", "github.ref == 'refs/heads/main'",
                     "github.run_attempt == 1", "github.repository == 'surpaimb/cpa-cloud'"):
            self.assertIn(text, publish["if"])
        download = publish["steps"][1]["with"]
        self.assertIn("needs.build.outputs.artifact_id", download["artifact-ids"])
        self.assertNotIn("name", download)
        for job in workflow["jobs"].values():
            for step in job["steps"]:
                if "uses" in step:
                    self.assertRegex(step["uses"], r"^[^@]+@[0-9a-f]{40}$")

    def test_fixed_head_run_asset_and_hash_checks_remain(self):
        workflow = self.workflow()
        ref = workflow["jobs"]["build"]["steps"][0]["with"]["ref"]
        self.assertIn("github.event.pull_request.head.sha", ref)
        text = workflow["jobs"]["publish"]["steps"][-1]["run"]
        for token in ("RREL_RUN_IDENTITY", "RREL_ARTIFACT_IDENTITY", "RREL_PRODUCER_RUN",
                      "RREL_DOWNLOADED_HASH", "RREL_PUBLICATION_UNKNOWN",
                      '"/releases/assets/" + str(asset_id)', 'run.get("head_sha") == source',
                      "expected_archive=hashes[v.ARCHIVE]"):
            self.assertIn(token, text)
        self.assertEqual(text.count('client.api("PATCH"'), 1)
        self.assertNotIn('client.api("DELETE"', text)

    def test_web_install_contract_matches_fixed_bun(self):
        steps = self.workflow()["jobs"]["build"]["steps"]
        installs = [s for s in steps if "\nbun install " in s.get("run", "")]
        self.assertEqual(len(installs), 1)
        self.assertIn("\nbun install --frozen-lockfile\n", installs[0]["run"])
        self.assertNotIn("--offline", installs[0]["run"])
        self.assertIn("downloads permitted", installs[0]["name"].lower())
        producers = [s for s in steps if " web-build " in s.get("run", "")]
        self.assertEqual(len(producers), 1)
        self.assertIn("downloads permitted", producers[0]["name"].lower())
        bun = [s for s in steps if s.get("uses", "").startswith("oven-sh/setup-bun@")]
        self.assertEqual(len(bun), 1)
        self.assertEqual(bun[0]["with"]["bun-version"], "1.3.14")

    def test_inline_bash_and_python_syntax_without_execution(self):
        import ast
        bash = os.environ.get("RELEASE_TEST_BASH", "/bin/bash")
        self.assertTrue(Path(bash).is_file(), "RWF_BASH_UNAVAILABLE")
        for job in self.workflow()["jobs"].values():
            for step in job["steps"]:
                if "run" not in step:
                    continue
                with self.subTest(step=step["name"]):
                    script = step["run"]
                    result = subprocess.run([bash, "-n"], input=script.encode(),
                                            capture_output=True, timeout=30, check=False)
                    self.assertEqual(result.returncode, 0, "RWF_BASH_SYNTAX")
                    if "python3 - <<'PY'\n" in script:
                        body = script.split("python3 - <<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
                        ast.parse(body, filename="workflow-inline-python")

class CountingResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.passed_methods, self.subtests, self.passed_subtests = 0, 0, 0
    def addSuccess(self, test):
        self.passed_methods += 1
        super().addSuccess(test)
    def addSubTest(self, test, subtest, error):
        self.subtests += 1
        self.passed_subtests += error is None
        super().addSubTest(test, subtest, error)

class CountingRunner(unittest.TextTestRunner):
    resultclass = CountingResult
    def run(self, test):
        result = super().run(test)
        print("RWF_TEST_COUNTS " + json.dumps({"methods": result.testsRun,
            "passed_methods": result.passed_methods, "subtests": result.subtests,
            "passed_subtests": result.passed_subtests, "failures": len(result.failures),
            "errors": len(result.errors), "skipped": len(result.skipped)}, sort_keys=True))
        return result

if __name__ == "__main__":
    unittest.main(testRunner=CountingRunner(verbosity=2))
