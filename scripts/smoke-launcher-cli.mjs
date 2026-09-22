// Process-level acceptance for the desktop launcher's CLI contract.
// Uses generated credentials and isolated temporary data only.
// Usage: node scripts/smoke-launcher-cli.mjs <absolute cpa-cloud executable>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdtemp, rm, stat } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'An absolute executable path is required');
const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-launcher-cli-'));
const data = path.join(root, 'data');
const password = randomBytes(24).toString('hex');
const children = new Set();
function launch(args, input, keepInputOpen = false) {
  const child = spawn(executable, args, { windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'] });
  children.add(child);
  child.done = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => { children.delete(child); resolve({ code, signal }); });
  });
  child.stdin.on('error', () => {});
  if (!keepInputOpen) child.stdin.end(input);
  return child;
}
async function finish(child, timeout = 15000) {
  let timer;
  try {
    return await Promise.race([child.done, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('Child process did not exit within deadline')), timeout);
    })]);
  } finally { clearTimeout(timer); }
}
async function reservePort() {
  const listener = net.createServer();
  await new Promise((resolve, reject) => { listener.once('error', reject); listener.listen(0, '127.0.0.1', resolve); });
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  return port;
}
try {
  assert.equal((await finish(launch(['--data-dir', data, '--check-initialized']))).code, 3);
  await assert.rejects(stat(data), { code: 'ENOENT' });
  assert.notEqual((await finish(launch(['--data-dir', data, '--init', '--check-initialized'], password))).code, 0);
  await assert.rejects(stat(data), { code: 'ENOENT' });
  assert.equal((await finish(launch(['--data-dir', data, '--init'], password))).code, 0);
  assert.equal((await finish(launch(['--data-dir', data, '--check-initialized']))).code, 0);
  for (let restart = 0; restart < 2; restart++) {
    const port = await reservePort();
    const child = launch(['--data-dir', data, '--listen', `127.0.0.1:${port}`, '--shutdown-on-stdin-eof'], undefined, true);
    let ready = false;
    for (let attempt = 0; attempt < 80; attempt++) {
      assert.equal(child.exitCode, null, 'Service exited before readiness');
      try {
        const response = await fetch(`http://127.0.0.1:${port}/healthz`, { signal: AbortSignal.timeout(500) });
        ready = response.ok && (await response.json()).status === 'ok';
      } catch {}
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'Service did not become ready');
    child.stdin.end();
    assert.equal((await finish(child)).code, 0, 'EOF must cause graceful service shutdown');
    assert.equal((await finish(launch(['--data-dir', data, '--check-initialized']))).code, 0);
  }
  console.log('PASS: read-only initialization check, conflicting flags, initialization, EOF shutdown, restart');
} finally {
  for (const child of children) child.kill();
  await Promise.allSettled([...children].map(child => child.done));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-launcher-cli-'));
  await rm(resolved, { recursive: true, force: true });
}
