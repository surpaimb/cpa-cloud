// CPA-authored OpenClaw evidence preflight. This file is never used by the app runtime.
import { createHash } from "node:crypto";
import { lstatSync, readFileSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const FIXTURE_PROVIDER = "cpa-cloud";
const FIXTURE_MODEL = "fixture-model";
const FIXTURE_KEY = "employee-fixture-secret";
const CLOSED_PROXY = "http://127.0.0.1:46262";
const MANIFEST_RELATIVE = path.join("guarded-final", "manifest.json");

export const EXPECTED_FIXTURE_CONFIG = `${JSON.stringify(
  {
    models: {
      mode: "merge",
      providers: {
        "cpa-cloud": {
          baseUrl: "http://127.0.0.1:46261/v1",
          apiKey: FIXTURE_KEY,
          api: "openai-responses",
          models: [{ id: FIXTURE_MODEL, name: FIXTURE_MODEL }],
        },
      },
    },
    agents: { defaults: { model: { primary: `${FIXTURE_PROVIDER}/${FIXTURE_MODEL}` } } },
  },
  null,
  2,
)}\n`;

export const FIXED_ARTIFACTS = Object.freeze({
  nodeExecutableSha256: "b3094d0b49f9ad602262a9921551737bb97637c05dd357a06ae98188d7290aa3",
  openclawEntrySha256: "aa8606ca0d62ff133ef5b7bd2323ec8ff3f8eb384404399cd5e742918d63b0a1",
  openclawPackageJsonSha256: "d2e734563bfb126d273beeb1e2cc5d4525b4d59efe00fdfec50a39b427524f56",
  dependencyTreeSha256: "658764cbd1b9744a1d53a6a060d5c5416c6d3ca72b6f586e46c9aa41cb1f8e2f",
  fixtureConfigSha256: "2a0ec1ced877f2d4dadd2597be5318b38771e3385562ab6c66a17c7af552f40f",
});

const MANIFEST_KEYS = Object.freeze([
  "evidenceRoot",
  "profileRoot",
  "stateDir",
  "configPath",
  "workingDirectory",
  "temporaryDirectory",
  "appData",
  "localAppData",
  "xdgConfigHome",
  "xdgDataHome",
  "xdgCacheHome",
  "npmCache",
  "npmPrefix",
  "npmUserConfig",
  "nodeExecutable",
  "openclawEntry",
  "openclawPackageJson",
  "dependencyTreeEvidence",
  "systemRoot",
]);

function normalized(value) {
  const resolved = path.normalize(value);
  return process.platform === "win32" ? resolved.toLowerCase() : resolved;
}

function assertAbsolute(name, value) {
  if (typeof value !== "string" || value.length === 0 || value !== value.trim()) {
    throw new Error(`${name} must be a non-empty, unpadded path`);
  }
  if (!path.isAbsolute(value)) throw new Error(`${name} must be absolute`);
  return path.resolve(value);
}

function isWithin(root, candidate) {
  const relative = path.relative(root, candidate);
  return (
    relative === "" ||
    (!relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative))
  );
}

function pathsOverlap(left, right) {
  return isWithin(left, right) || isWithin(right, left);
}

function assertNoReparseAncestors(name, value) {
  const absolute = path.resolve(value);
  const parsed = path.parse(absolute);
  const segments = absolute.slice(parsed.root.length).split(path.sep).filter(Boolean);
  let current = parsed.root;
  for (const segment of segments) {
    current = path.join(current, segment);
    const metadata = lstatSync(current);
    if (metadata.isSymbolicLink()) {
      throw new Error(`${name} contains a symbolic-link or junction ancestor`);
    }
  }
}

function exactIdentity(value) {
  const metadata = statSync(value, { bigint: true });
  return {
    realPath: realpathSync.native(value),
    device: metadata.dev.toString(),
    inode: metadata.ino.toString(),
    links: metadata.nlink.toString(),
    size: metadata.size.toString(),
    modifiedNanoseconds: metadata.mtimeNs.toString(),
  };
}

function assertSingleLinkRegularFile(name, value) {
  const metadata = lstatSync(value);
  if (metadata.isSymbolicLink() || !metadata.isFile()) {
    throw new Error(`${name} must be a regular non-link file`);
  }
  if (metadata.nlink !== 1) throw new Error(`${name} must not be hard-linked`);
}

function assertDirectory(name, value) {
  const metadata = lstatSync(value);
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    throw new Error(`${name} must be a directory and not a link`);
  }
}

function sha256Bytes(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function assertFileHash(name, value, expected) {
  if (sha256Bytes(readFileSync(value)) !== expected) {
    throw new Error(`${name} SHA-256 mismatch`);
  }
}

function assertExactFixtureConfig(configPath, expectedArtifacts) {
  const bytes = readFileSync(configPath);
  if (!bytes.equals(Buffer.from(EXPECTED_FIXTURE_CONFIG, "utf8"))) {
    throw new Error("fixture config bytes do not match the exact bounded fixture");
  }
  if (sha256Bytes(bytes) !== expectedArtifacts.fixtureConfigSha256) {
    throw new Error("fixture config SHA-256 mismatch");
  }
}

function assertExactKeys(input) {
  const actual = Object.keys(input).sort();
  const expected = [...MANIFEST_KEYS].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error("probe manifest fields do not match the fixed schema");
  }
}

function statPathIfPresent(value) {
  try {
    assertNoReparseAncestors("real OpenClaw state", value);
    return realpathSync.native(value);
  } catch (error) {
    if (error?.code === "ENOENT") return path.resolve(value);
    throw error;
  }
}

export function validateProbeManifest(input, options = {}) {
  if (!input || typeof input !== "object" || Array.isArray(input)) {
    throw new Error("probe manifest is required");
  }
  if (Object.hasOwn(input, "realProfile")) {
    throw new Error("realProfile must come from a trusted external source, not the manifest");
  }
  assertExactKeys(input);
  const trustedRealProfile = assertAbsolute("trustedRealProfile", options.trustedRealProfile);
  const expectedArtifacts = options.expectedArtifacts ?? FIXED_ARTIFACTS;
  const manifest = Object.fromEntries(
    MANIFEST_KEYS.map((name) => [name, assertAbsolute(name, input[name])]),
  );

  assertDirectory("trustedRealProfile", trustedRealProfile);
  assertDirectory("evidenceRoot", manifest.evidenceRoot);
  assertDirectory("systemRoot", manifest.systemRoot);
  const evidenceReal = realpathSync.native(manifest.evidenceRoot);
  const trustedProfileReal = realpathSync.native(trustedRealProfile);
  const trustedProbeRoot = options.trustedProbeRoot
    ? realpathSync.native(assertAbsolute("trustedProbeRoot", options.trustedProbeRoot))
    : evidenceReal;
  if (normalized(evidenceReal) !== normalized(trustedProbeRoot)) {
    throw new Error("manifest evidenceRoot does not match the trusted probe root");
  }

  const realStateBoundary = statPathIfPresent(path.join(trustedProfileReal, ".openclaw"));
  if (pathsOverlap(normalized(evidenceReal), normalized(realStateBoundary))) {
    throw new Error("evidence root overlaps the real OpenClaw state boundary");
  }

  const directoryNames = [
    "profileRoot",
    "stateDir",
    "workingDirectory",
    "temporaryDirectory",
    "appData",
    "localAppData",
    "xdgConfigHome",
    "xdgDataHome",
    "xdgCacheHome",
    "npmCache",
    "npmPrefix",
  ];
  const isolatedFileNames = ["configPath", "npmUserConfig", "dependencyTreeEvidence"];
  const artifactFileNames = ["nodeExecutable", "openclawEntry", "openclawPackageJson"];
  for (const name of directoryNames) assertDirectory(name, manifest[name]);
  for (const name of [...isolatedFileNames, ...artifactFileNames]) {
    assertSingleLinkRegularFile(name, manifest[name]);
  }

  const isolatedNames = ["evidenceRoot", ...directoryNames, ...isolatedFileNames];
  for (const name of [...isolatedNames, ...artifactFileNames, "systemRoot"]) {
    assertNoReparseAncestors(name, manifest[name]);
    const resolved = realpathSync.native(manifest[name]);
    if (isolatedNames.includes(name) && !isWithin(evidenceReal, resolved)) {
      throw new Error(`${name} escaped the evidence root`);
    }
    if (isolatedNames.includes(name) && pathsOverlap(normalized(resolved), normalized(realStateBoundary))) {
      throw new Error(`${name} overlaps the real OpenClaw state boundary`);
    }
  }

  if (normalized(realpathSync.native(manifest.profileRoot)) === normalized(trustedProfileReal)) {
    throw new Error("probe profile resolved to the real user profile");
  }
  assertExactFixtureConfig(manifest.configPath, expectedArtifacts);
  assertFileHash("nodeExecutable", manifest.nodeExecutable, expectedArtifacts.nodeExecutableSha256);
  assertFileHash("openclawEntry", manifest.openclawEntry, expectedArtifacts.openclawEntrySha256);
  assertFileHash(
    "openclawPackageJson",
    manifest.openclawPackageJson,
    expectedArtifacts.openclawPackageJsonSha256,
  );
  assertFileHash(
    "dependencyTreeEvidence",
    manifest.dependencyTreeEvidence,
    expectedArtifacts.dependencyTreeSha256,
  );
  return manifest;
}

export function readManifestFromTrustedRoot(probeRoot, options = {}) {
  const absoluteRoot = assertAbsolute("probeRoot", probeRoot);
  const trustedRealProfile = assertAbsolute("trustedRealProfile", options.trustedRealProfile);
  assertDirectory("probeRoot", absoluteRoot);
  assertDirectory("trustedRealProfile", trustedRealProfile);
  assertNoReparseAncestors("probeRoot", absoluteRoot);
  assertNoReparseAncestors("trustedRealProfile", trustedRealProfile);
  const realRoot = realpathSync.native(absoluteRoot);
  const trustedProfileReal = realpathSync.native(trustedRealProfile);
  const realStateBoundary = statPathIfPresent(path.join(trustedProfileReal, ".openclaw"));
  if (pathsOverlap(normalized(realRoot), normalized(realStateBoundary))) {
    throw new Error("trusted probe root overlaps the real OpenClaw state boundary");
  }
  const manifestPath = path.resolve(realRoot, MANIFEST_RELATIVE);
  if (!isWithin(realRoot, manifestPath)) {
    throw new Error("derived manifest path escaped the trusted probe root");
  }
  assertNoReparseAncestors("manifest", manifestPath);
  assertSingleLinkRegularFile("manifest", manifestPath);
  const realManifest = realpathSync.native(manifestPath);
  if (!isWithin(realRoot, realManifest)) {
    throw new Error("manifest resolved outside the trusted probe root");
  }
  const parsed = JSON.parse(readFileSync(realManifest, "utf8"));
  return validateProbeManifest(parsed, { ...options, trustedProbeRoot: realRoot });
}

export function captureProbeSnapshot(manifest) {
  return Object.fromEntries(
    [
      "evidenceRoot",
      "profileRoot",
      "stateDir",
      "configPath",
      "workingDirectory",
      "temporaryDirectory",
      "appData",
      "localAppData",
      "xdgConfigHome",
      "xdgDataHome",
      "xdgCacheHome",
      "npmCache",
      "npmPrefix",
      "npmUserConfig",
      "nodeExecutable",
      "openclawEntry",
      "openclawPackageJson",
      "dependencyTreeEvidence",
    ].map((name) => [name, exactIdentity(manifest[name])]),
  );
}

export function buildChildEnvironment(manifest) {
  const system32 = path.join(manifest.systemRoot, "System32");
  const nodeDirectory = path.dirname(manifest.nodeExecutable);
  const environment = Object.create(null);
  Object.assign(environment, {
    ALL_PROXY: CLOSED_PROXY,
    APPDATA: manifest.appData,
    CI: "1",
    DO_NOT_TRACK: "1",
    HOME: manifest.profileRoot,
    HTTP_PROXY: CLOSED_PROXY,
    HTTPS_PROXY: CLOSED_PROXY,
    LOCALAPPDATA: manifest.localAppData,
    NODE_DISABLE_COMPILE_CACHE: "1",
    NO_PROXY: "127.0.0.1,localhost",
    NPM_CONFIG_CACHE: manifest.npmCache,
    NPM_CONFIG_PREFIX: manifest.npmPrefix,
    NPM_CONFIG_UPDATE_NOTIFIER: "false",
    NPM_CONFIG_USERCONFIG: manifest.npmUserConfig,
    OPENCLAW_CONFIG_PATH: manifest.configPath,
    OPENCLAW_CONFIG_READONLY: "1",
    OPENCLAW_HOME: manifest.profileRoot,
    OPENCLAW_NO_AUTO_UPDATE: "1",
    OPENCLAW_STATE_DIR: manifest.stateDir,
    PATH: `${nodeDirectory}${path.delimiter}${system32}`,
    SYSTEMROOT: manifest.systemRoot,
    TEMP: manifest.temporaryDirectory,
    TMP: manifest.temporaryDirectory,
    USERPROFILE: manifest.profileRoot,
    WINDIR: manifest.systemRoot,
    XDG_CACHE_HOME: manifest.xdgCacheHome,
    XDG_CONFIG_HOME: manifest.xdgConfigHome,
    XDG_DATA_HOME: manifest.xdgDataHome,
  });
  const keys = Object.keys(environment);
  if (new Set(keys.map((key) => key.toLowerCase())).size !== keys.length) {
    throw new Error("child environment contains case-insensitive duplicate keys");
  }
  return environment;
}

export function runEvidenceProcess(input, _operation, options = {}) {
  validateProbeManifest(input, options);
  throw new Error(
    "OpenClaw native probe blocked: no OS-level egress isolation receipt issuer is available",
  );
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  throw new Error(
    "OpenClaw native probe CLI disabled: no trusted OS-profile or egress-isolation receipt issuer is available",
  );
}
