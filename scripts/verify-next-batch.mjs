// Batch-level acceptance runner. It never supplies a default data directory or
// fixed port: every child smoke receives an exclusive OS-temporary root and is
// responsible for allocating loopback ports inside that root's isolated run.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { access, mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

const argumentsList = process.argv.slice(2);
const option = (name) => {
  const index = argumentsList.indexOf(name);
  return index === -1 ? undefined : argumentsList[index + 1];
};
const listing = argumentsList.includes('--list');
const server = option('--server');
const backup = option('--backup');
const web = option('--web-dir');
const repository = path.resolve(import.meta.dirname, '..');

const stages = [
  { name: 'account lifecycle', script: 'smoke-account-lifecycle.mjs', timeout: 180_000, args: () => [server, web] },
  { name: 'scheduled tests', script: 'smoke-scheduled-tests.mjs', timeout: 180_000, args: () => [server, web] },
  { name: 'encrypted backup restore', script: 'smoke-backup-restore.mjs', timeout: 240_000, args: () => [server, backup, web] },
];

if (listing) {
  for (const stage of stages) console.log(`${stage.name}: scripts/${stage.script}`);
  process.exit(0);
}

for (const [label, value] of [['--server', server], ['--backup', backup], ['--web-dir', web]]) {
  assert.ok(value && path.isAbsolute(value), `${label} must be an absolute path`);
  await access(value);
}

const tempParent = path.resolve(os.tmpdir());
const maxOutputBytes = 1 << 20;

async function runStage(stage) {
  const script = path.join(repository, 'scripts', stage.script);
  await access(script);
  const root = await mkdtemp(path.join(tempParent, 'cpac-next-batch-'));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), tempParent, 'Acceptance root escaped the OS temporary directory');
  assert.ok(path.basename(resolved).startsWith('cpac-next-batch-'), 'Unexpected acceptance root');
  let output = '';
  try {
    const child = spawn(process.execPath, [script, ...stage.args()], {
      cwd: repository,
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
      env: {
        ...process.env,
        CPA_CLOUD_ACCEPTANCE_TEMP_ROOT: resolved,
        CPA_CLOUD_ACCEPTANCE_FORBID_PORT: '8787',
      },
    });
    for (const stream of [child.stdout, child.stderr]) {
      stream.on('data', (chunk) => {
        output += chunk;
        if (Buffer.byteLength(output) > maxOutputBytes) child.kill('SIGKILL');
      });
    }
    const timer = setTimeout(() => child.kill('SIGKILL'), stage.timeout);
    let result;
    try { result = await once(child, 'exit'); } finally { clearTimeout(timer); }
    const [code, signal] = result;
    if (code !== 0) {
      const tail = output.slice(-8000);
      throw new Error(`${stage.name} failed (code=${code}, signal=${signal ?? 'none'})\n${tail}`);
    }
    process.stdout.write(output);
  } finally {
    await rm(resolved, { recursive: true, force: true });
  }
}

for (const stage of stages) await runStage(stage);
console.log('PASS: next-batch isolated account lifecycle, scheduled tests, and encrypted backup/restore acceptance');
