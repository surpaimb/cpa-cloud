#!/usr/bin/env node
// Independently authored CPA Cloud release evidence. Run from the employee
// workspace after frozen pnpm install and the TypeScript build check.
import { build } from "vite";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  existsSync, lstatSync, readFileSync, realpathSync, readdirSync, writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const workspace = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const output = process.argv[2];
if (!output || process.argv.length !== 3) {
  throw new Error("usage: node scripts/employee-bundle-modules.mjs OUTPUT_JSON");
}
const target = resolve(output);
const dist = resolve(workspace, "dist");
if (existsSync(resolve(workspace, "public"))) {
  throw new Error("unattributed Vite public directory is not allowed");
}
if (target === dist || target.startsWith(dist + sep)) {
  throw new Error("module evidence must be outside dist");
}

function sha(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}
function sorted(values) {
  return values.sort((a, b) => a < b ? -1 : a > b ? 1 : 0);
}
function within(child, parent) {
  return child === parent || child.startsWith(parent + sep);
}
function packageForModule(rawId) {
  const id = rawId.split("?")[0];
  if (id.startsWith("\0")) {
    if (id === "\0vite/modulepreload-polyfill.js" ||
        id === "\0commonjsHelpers.js") return null;
    throw new Error("unattributed virtual bundle module: " + rawId);
  }
  if (!isAbsolute(id)) throw new Error("non-absolute bundle module: " + rawId);
  let real;
  try {
    real = realpathSync(id);
  } catch {
    throw new Error("bundle module file unavailable: " + rawId);
  }
  if (within(real, workspace) && !within(real, resolve(workspace, "node_modules"))) {
    if (within(real, dist) || !lstatSync(real).isFile()) {
      throw new Error("unapproved local bundle module: " + rawId);
    }
    const sourceName = relative(workspace, real).split(sep).join("/");
    execFileSync("git", ["ls-files", "--error-unmatch", "--", sourceName], {
      cwd: workspace, stdio: "ignore",
    });
    return null;
  }
  if (!within(real, resolve(workspace, "node_modules"))) {
    throw new Error("bundle module outside approved source: " + rawId);
  }
  let current = dirname(real);
  while (within(current, resolve(workspace, "node_modules"))) {
    const manifest = join(current, "package.json");
    try {
      if (lstatSync(manifest).isFile()) {
        const pkg = JSON.parse(readFileSync(manifest, "utf8"));
        if (typeof pkg.name !== "string" || typeof pkg.version !== "string") {
          throw new Error("package identity absent: " + manifest);
        }
        return pkg.name + "@" + pkg.version;
      }
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
    current = dirname(current);
  }
  throw new Error("bundle module package root unavailable: " + rawId);
}
function filesUnder(root) {
  const result = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isSymbolicLink()) throw new Error("dist symlink: " + path);
      if (entry.isDirectory()) walk(path);
      else if (entry.isFile()) {
        const name = relative(root, path).split(sep).join("/");
        const bytes = readFileSync(path);
        result.push({ path: name, bytes: bytes.length, sha256: sha(bytes) });
      } else throw new Error("dist non-file: " + path);
    }
  };
  walk(root);
  return result.sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
}

const chunks = [];
process.chdir(workspace);
const result = await build({
  root: workspace,
  configFile: resolve(workspace, "vite.config.ts"),
});
const outputs = Array.isArray(result) ? result : [result];
if (outputs.length < 1 || outputs.some((o) => !Array.isArray(o?.output))) {
  throw new Error("Vite did not return complete build outputs");
}
for (const output of outputs) {
  for (const item of output.output) {
    if (item.type !== "chunk") continue;
    if (!item.modules || typeof item.modules !== "object") {
      throw new Error("Vite chunk modules unavailable: " + item.fileName);
    }
    if (typeof item.code !== "string" || item.code.length === 0) {
      throw new Error("Vite chunk code unavailable: " + item.fileName);
    }
    const code = Buffer.from(item.code, "utf8");
    if (code.length === 0) throw new Error("empty Vite chunk code: " + item.fileName);
    const moduleIds = sorted(Object.keys(item.modules));
    if (moduleIds.length < 1) throw new Error("empty Vite chunk: " + item.fileName);
    chunks.push({ fileName: item.fileName, codeBytes: code.length,
      codeSha256: sha(code), moduleIds,
      imports: sorted([...item.imports]), dynamicImports: sorted([...item.dynamicImports]) });
  }
}
if (chunks.length < 1) {
  throw new Error("Vite did not expose a generated chunk module graph");
}
const names = new Set(chunks.map((chunk) => chunk.fileName));
if (names.size !== chunks.length || chunks.some((chunk) =>
  [...chunk.imports, ...chunk.dynamicImports].some((name) => !names.has(name)))) {
  throw new Error("Vite output contains duplicate chunks or external imports");
}
const files = filesUnder(dist);
if (files.length < 1) throw new Error("Vite dist is empty");
const distByName = new Map(files.map((file) => [file.path, file]));
for (const chunk of chunks) {
  const emitted = distByName.get(chunk.fileName);
  if (!emitted || emitted.bytes !== chunk.codeBytes ||
      emitted.sha256 !== chunk.codeSha256) {
    throw new Error("Vite chunk code differs from built dist: " + chunk.fileName);
  }
}
let htmlCount = 0;
let scriptCount = 0;
for (const file of files) {
  const lower = file.path.toLowerCase();
  if (/\.(?:js|mjs|cjs|wasm)$/.test(lower) &&
      (!names.has(file.path) || lower.endsWith(".wasm"))) {
    throw new Error("dist executable asset lacks Vite module graph: " + file.path);
  }
  if (lower.endsWith(".svg")) {
    const value = readFileSync(join(dist, file.path), "utf8").toLowerCase();
    if (value.includes("<script") || value.includes("javascript:")) {
      throw new Error("executable SVG asset unsupported: " + file.path);
    }
  }
  if (lower.endsWith(".css")) {
    const value = readFileSync(join(dist, file.path), "utf8").toLowerCase();
    if (value.includes("@import") || value.includes("url(")) {
      throw new Error("unattributed CSS import or asset unsupported: " + file.path);
    }
  }
  if (lower.endsWith(".html")) {
    htmlCount += 1;
    const html = readFileSync(join(dist, file.path), "utf8");
    if (/<(?:base|iframe|object|embed)\b|\bon[a-z]+\s*=/i.test(html)) {
      throw new Error("HTML executable container unsupported: " + file.path);
    }
    const scriptPattern = /<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi;
    const scripts = [...html.matchAll(scriptPattern)];
    if (/<script\b/i.test(html.replace(scriptPattern, ""))) {
      throw new Error("unclosed or malformed HTML script: " + file.path);
    }
    for (const match of scripts) {
      scriptCount += 1;
      const attrs = match[1];
      const src = attrs.match(/\bsrc\s*=\s*(["'])([^"']+)\1/i);
      if (!src || !/\btype\s*=\s*(["'])module\1/i.test(attrs) ||
          match[2].trim() || /[?#%\\]/.test(src[2])) {
        throw new Error("HTML inline or external script: " + file.path);
      }
      const target = new URL(src[2], "https://cpa.invalid/" + file.path);
      const targetName = target.pathname.slice(1);
      if (target.origin !== "https://cpa.invalid" || !names.has(targetName)) {
        throw new Error("HTML script is not a Vite chunk: " + file.path);
      }
    }
  }
}
if (htmlCount === 0 || scriptCount === 0) {
  throw new Error("Vite output has no attributed HTML module script");
}
const packageIds = sorted([...new Set(
  chunks.flatMap((chunk) => chunk.moduleIds.map(packageForModule))
    .filter((identity) => identity !== null),
)]);
const sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], {
  cwd: workspace, encoding: "utf8",
}).trim();
if (!/^[0-9a-f]{40}$/.test(sourceCommit)) {
  throw new Error("source commit unavailable");
}
const evidence = {
  schema: 2,
  sourceCommit,
  chunks: chunks.sort((a, b) => a.fileName < b.fileName ? -1 : a.fileName > b.fileName ? 1 : 0),
  packageIds,
  distFiles: files,
};
writeFileSync(target, JSON.stringify(evidence, null, 2) + "\n", {
  flag: "wx",
});
console.log(JSON.stringify({
  sourceCommit, chunkCount: chunks.length,
  moduleCount: chunks.reduce((n, chunk) => n + chunk.moduleIds.length, 0),
  packageCount: packageIds.length, distFileCount: files.length,
  evidenceSha256: sha(readFileSync(target)),
}));
