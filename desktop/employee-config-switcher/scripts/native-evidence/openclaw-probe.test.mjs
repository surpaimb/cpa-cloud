import {
  appendFileSync,
  linkSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { createHash } from "node:crypto";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";

import {
  EXPECTED_FIXTURE_CONFIG,
  buildChildEnvironment,
  captureProbeSnapshot,
  readManifestFromTrustedRoot,
  runEvidenceProcess,
  validateProbeManifest,
} from "./openclaw-probe.mjs";

const roots = [];

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

function createFixture() {
  const container = mkdtempSync(path.join(os.tmpdir(), "cpa-openclaw-probe-test-"));
  roots.push(container);
  const evidenceRoot = path.join(container, "evidence");
  const realProfile = path.join(container, "real-profile");
  const profileRoot = path.join(evidenceRoot, "profile");
  const stateDir = path.join(profileRoot, ".openclaw");
  const workingDirectory = path.join(evidenceRoot, "workspace");
  const temporaryDirectory = path.join(evidenceRoot, "tmp");
  const appData = path.join(profileRoot, "appdata");
  const localAppData = path.join(profileRoot, "localappdata");
  const xdgConfigHome = path.join(profileRoot, "xdg-config");
  const xdgDataHome = path.join(profileRoot, "xdg-data");
  const xdgCacheHome = path.join(profileRoot, "xdg-cache");
  const npmCache = path.join(profileRoot, "npm-cache");
  const npmPrefix = path.join(profileRoot, "npm-prefix");
  for (const directory of [
    evidenceRoot,
    realProfile,
    stateDir,
    workingDirectory,
    temporaryDirectory,
    appData,
    localAppData,
    xdgConfigHome,
    xdgDataHome,
    xdgCacheHome,
    npmCache,
    npmPrefix,
    path.join(evidenceRoot, "guarded-final"),
  ]) {
    mkdirSync(directory, { recursive: true });
  }
  const configPath = path.join(stateDir, "openclaw.json");
  writeFileSync(configPath, EXPECTED_FIXTURE_CONFIG);
  const npmUserConfig = path.join(profileRoot, "npmrc");
  writeFileSync(npmUserConfig, "");
  const nodeExecutable = path.join(container, "node-fixture.bin");
  const openclawEntry = path.join(container, "openclaw-fixture.mjs");
  const openclawPackageJson = path.join(container, "package-fixture.json");
  const dependencyTreeEvidence = path.join(evidenceRoot, "dependency-tree.json");
  writeFileSync(nodeExecutable, "node-fixture");
  writeFileSync(openclawEntry, "openclaw-fixture");
  writeFileSync(openclawPackageJson, "package-fixture");
  writeFileSync(dependencyTreeEvidence, "dependency-tree-fixture");
  const manifest = {
    evidenceRoot,
    profileRoot,
    stateDir,
    configPath,
    workingDirectory,
    temporaryDirectory,
    appData,
    localAppData,
    xdgConfigHome,
    xdgDataHome,
    xdgCacheHome,
    npmCache,
    npmPrefix,
    npmUserConfig,
    nodeExecutable,
    openclawEntry,
    openclawPackageJson,
    dependencyTreeEvidence,
    systemRoot: process.env.SystemRoot ?? path.parse(process.execPath).root,
  };
  const expectedArtifacts = {
    nodeExecutableSha256: digest("node-fixture"),
    openclawEntrySha256: digest("openclaw-fixture"),
    openclawPackageJsonSha256: digest("package-fixture"),
    dependencyTreeSha256: digest("dependency-tree-fixture"),
    fixtureConfigSha256: digest(EXPECTED_FIXTURE_CONFIG),
  };
  const manifestPath = path.join(evidenceRoot, "guarded-final", "manifest.json");
  writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return { container, evidenceRoot, realProfile, manifest, manifestPath, expectedArtifacts };
}

function options(fixture) {
  return {
    trustedRealProfile: fixture.realProfile,
    trustedProbeRoot: fixture.evidenceRoot,
    expectedArtifacts: fixture.expectedArtifacts,
  };
}

afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

describe("OpenClaw native evidence preflight", () => {
  it("keeps the committed acceptance record blocked and binds the exact fixture bytes", () => {
    const fixtureDirectory = path.resolve(
      process.cwd(),
      "crates/employee-config-core/tests/fixtures/openclaw-2026.9.8",
    );
    const record = JSON.parse(
      readFileSync(path.join(fixtureDirectory, "native-acceptance.json"), "utf8"),
    );
    const config = readFileSync(path.join(fixtureDirectory, "native-config.json"));
    expect(record.native_evidence_gate).toMatchObject({
      satisfied: false,
      execution: "blocked-before-spawn",
    });
    expect(record.artifacts.fixture_config.size).toBe(config.length);
    expect(record.artifacts.fixture_config.sha256).toBe(digest(config));
    expect(record.discarded_nonisolated_probe.excluded_from_acceptance).toBe(true);
    expect(record.discarded_nonisolated_probe.conclusion).toMatch(/indeterminate/u);
  });

  it("derives and validates the only allowed manifest below a trusted probe root", () => {
    const fixture = createFixture();
    const manifest = readManifestFromTrustedRoot(fixture.evidenceRoot, options(fixture));
    expect(manifest.configPath).toBe(fixture.manifest.configPath);
    expect(captureProbeSnapshot(manifest).configPath.links).toBe("1");
  });

  it("does not accept an arbitrary manifest path or a manifest-supplied real profile", () => {
    const fixture = createFixture();
    const alternate = path.join(fixture.container, "arbitrary.json");
    writeFileSync(alternate, JSON.stringify(fixture.manifest));
    expect(() => readManifestFromTrustedRoot(alternate, options(fixture))).toThrow(/directory/u);
    const poisoned = { ...fixture.manifest, realProfile: fixture.realProfile };
    expect(() => validateProbeManifest(poisoned, options(fixture))).toThrow(/trusted external source/u);

  });

  it("rejects evidence roots inside or containing the real OpenClaw state", () => {
    const inside = createFixture();
    const realState = path.join(inside.realProfile, ".openclaw");
    mkdirSync(realState);
    inside.manifest.evidenceRoot = realState;
    expect(() =>
      validateProbeManifest(inside.manifest, {
        ...options(inside),
        trustedProbeRoot: realState,
      }),
    ).toThrow(/overlaps/u);

    const containing = createFixture();
    containing.realProfile = path.join(containing.evidenceRoot, "real-profile");
    mkdirSync(path.join(containing.realProfile, ".openclaw"), { recursive: true });
    expect(() => validateProbeManifest(containing.manifest, options(containing))).toThrow(/overlaps/u);

    const beforeRead = createFixture();
    const beforeReadState = path.join(beforeRead.realProfile, ".openclaw");
    mkdirSync(path.join(beforeReadState, "guarded-final"), { recursive: true });
    writeFileSync(path.join(beforeReadState, "guarded-final", "manifest.json"), "not-json");
    expect(() =>
      readManifestFromTrustedRoot(beforeReadState, {
        trustedRealProfile: beforeRead.realProfile,
        expectedArtifacts: beforeRead.expectedArtifacts,
      }),
    ).toThrow(/overlaps/u);
  });

  it("rejects relative, missing, symbolic-link, and hard-linked inputs", () => {
    const relative = createFixture();
    relative.manifest.evidenceRoot = "relative-evidence";
    expect(() => validateProbeManifest(relative.manifest, options(relative))).toThrow(/must be absolute/u);

    const missing = createFixture();
    missing.manifest.configPath = path.join(missing.evidenceRoot, "missing.json");
    expect(() => validateProbeManifest(missing.manifest, options(missing))).toThrow();

    const linked = createFixture();
    const target = path.join(linked.evidenceRoot, "linked-target");
    mkdirSync(target);
    const alias = path.join(linked.evidenceRoot, "linked-state");
    symlinkSync(target, alias, process.platform === "win32" ? "junction" : "dir");
    linked.manifest.stateDir = alias;
    expect(() => validateProbeManifest(linked.manifest, options(linked))).toThrow(/link/u);

    const hardlinked = createFixture();
    const hardlink = path.join(hardlinked.evidenceRoot, "config-hardlink.json");
    try {
      linkSync(hardlinked.manifest.configPath, hardlink);
    } catch (error) {
      if (["EPERM", "EACCES", "ENOTSUP"].includes(error.code)) return;
      throw error;
    }
    expect(() => validateProbeManifest(hardlinked.manifest, options(hardlinked))).toThrow(/hard-linked/u);

    const hardlinkedManifest = createFixture();
    const manifestAlias = path.join(hardlinkedManifest.container, "manifest-hardlink.json");
    try {
      linkSync(hardlinkedManifest.manifestPath, manifestAlias);
    } catch (error) {
      if (["EPERM", "EACCES", "ENOTSUP"].includes(error.code)) return;
      throw error;
    }
    expect(() =>
      readManifestFromTrustedRoot(hardlinkedManifest.evidenceRoot, options(hardlinkedManifest)),
    ).toThrow(/hard-linked/u);
  });

  it("rejects extra config fields and wrong fixed-artifact hashes", () => {
    const extra = createFixture();
    const config = JSON.parse(EXPECTED_FIXTURE_CONFIG);
    config.telemetry = { enabled: false };
    writeFileSync(extra.manifest.configPath, `${JSON.stringify(config, null, 2)}\n`);
    expect(() => validateProbeManifest(extra.manifest, options(extra))).toThrow(/exact bounded fixture/u);

    for (const field of ["nodeExecutable", "openclawEntry", "openclawPackageJson", "dependencyTreeEvidence"]) {
      const fixture = createFixture();
      appendFileSync(fixture.manifest[field], "changed");
      expect(() => validateProbeManifest(fixture.manifest, options(fixture))).toThrow(/SHA-256 mismatch/u);
    }
  });

  it("builds a fresh environment mapping without inherited secrets", () => {
    const fixture = createFixture();
    const manifest = validateProbeManifest(fixture.manifest, options(fixture));
    const previousKey = process.env.OPENAI_API_KEY;
    const previousSentinel = process.env.CPA_PROBE_SHOULD_NOT_INHERIT;
    process.env.OPENAI_API_KEY = "real-parent-secret";
    process.env.CPA_PROBE_SHOULD_NOT_INHERIT = "real-parent-state";
    try {
      const child = buildChildEnvironment(manifest);
      expect(Object.getPrototypeOf(child)).toBeNull();
      expect(child.HOME).toBe(manifest.profileRoot);
      expect(child.USERPROFILE).toBe(manifest.profileRoot);
      expect(child.OPENCLAW_NO_AUTO_UPDATE).toBe("1");
      expect(child.OPENAI_API_KEY).toBeUndefined();
      expect(child.CPA_PROBE_SHOULD_NOT_INHERIT).toBeUndefined();
    } finally {
      if (previousKey === undefined) delete process.env.OPENAI_API_KEY;
      else process.env.OPENAI_API_KEY = previousKey;
      if (previousSentinel === undefined) delete process.env.CPA_PROBE_SHOULD_NOT_INHERIT;
      else process.env.CPA_PROBE_SHOULD_NOT_INHERIT = previousSentinel;
    }
  });

  it("fails closed before any OpenClaw process can be spawned", () => {
    const fixture = createFixture();
    expect(() => runEvidenceProcess(fixture.manifest, "version", options(fixture))).toThrow(
      /no OS-level egress isolation receipt issuer/u,
    );
  });

  it("has an exact non-launcher export surface and only a static CLI-disabled entry", async () => {
    const sourcePath = path.resolve(process.cwd(), "scripts/native-evidence/openclaw-probe.mjs");
    const source = readFileSync(sourcePath, "utf8");
    const module = await import("./openclaw-probe.mjs");
    expect(Object.keys(module).sort()).toEqual([
      "EXPECTED_FIXTURE_CONFIG",
      "FIXED_ARTIFACTS",
      "buildChildEnvironment",
      "captureProbeSnapshot",
      "readManifestFromTrustedRoot",
      "runEvidenceProcess",
      "validateProbeManifest",
    ]);
    const staticImports = [...source.matchAll(/\bfrom\s+["']([^"']+)["']/gu)]
      .map((match) => match[1])
      .sort();
    expect(staticImports).toEqual(["node:crypto", "node:fs", "node:path", "node:url"]);
    expect(source.match(/^\s*import\b/gmu)?.length ?? 0).toBe(staticImports.length);
    expect(source).not.toMatch(/^\s*import\s+["']/mu);
    expect(source).not.toMatch(
      /(?:node:)?child_process|\bspawn(?:Sync)?\s*\(|\bexec(?:File)?(?:Sync)?\s*\(|\bfork\s*\(|\bimport\s*\(|\brequire\s*\(|process\.(?:_linkedBinding|binding)\s*\(/u,
    );
    expect(source).toMatch(
      /if \(import\.meta\.url === pathToFileURL\(process\.argv\[1\] \?\? ""\)\.href\) \{\s*throw new Error\(\s*"OpenClaw native probe CLI disabled:/u,
    );
  });

  it("detects manifest corruption instead of accepting abnormal output", () => {
    const fixture = createFixture();
    const before = readFileSync(fixture.manifestPath, "utf8");
    appendFileSync(fixture.manifestPath, "not-json");
    expect(readFileSync(fixture.manifestPath, "utf8")).not.toBe(before);
    expect(() => readManifestFromTrustedRoot(fixture.evidenceRoot, options(fixture))).toThrow();
  });
});
