// Execute the README startup block with isolated data and a free test port.
import { readFile, mkdtemp, mkdir, copyFile, cp, writeFile, rm } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { randomBytes } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import assert from 'node:assert/strict';

assert.equal(process.platform, 'win32');
const executable = path.resolve(process.argv[2]);
const webSource = path.resolve(process.argv[3]);
const root = await mkdtemp(path.join(os.tmpdir(), 'cpa-readme-start-'));
const app = path.join(root, 'Programs', 'CPA Cloud');
const data = path.join(root, 'CPACloud', 'data');
let child;
try {
  await mkdir(app, { recursive: true });
  await copyFile(executable, path.join(app, 'cpa-cloud.exe'));
  await cp(webSource, path.join(app, 'web'), { recursive: true });
  const init = spawn(path.join(app, 'cpa-cloud.exe'), ['--data-dir', data, '--init'], { windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'] });
  init.stdin.end(randomBytes(24).toString('hex'));
  assert.equal((await once(init, 'exit'))[0], 0);
  for (const file of ['README.md', 'README.en.md']) {
    const doc = await readFile(file, 'utf8');
    const block = [...doc.matchAll(/```powershell\s*\n([\s\S]*?)```/g)].find(m => m[1].includes('--web-dir $web'))?.[1];
    assert.ok(block, `${file}: startup command missing`);
    const reservation = net.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const port = reservation.address().port;
    await new Promise(resolve => reservation.close(resolve));
    // Only the listen port and test-only shutdown transport change; path
    // resolution and all preflight checks run exactly as documented.
    const script = block.replace('127.0.0.1:8787', `127.0.0.1:${port}`)
      .replace('--web-dir $web', '--web-dir $web --shutdown-on-stdin-eof');
    const scriptPath = path.join(root, 'startup.ps1');
    await writeFile(scriptPath, script);
    child = spawn('powershell.exe', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', scriptPath], {
      cwd: os.tmpdir(), windowsHide: true,
      env: { ...process.env, LOCALAPPDATA: root }, stdio: ['pipe', 'ignore', 'pipe'],
    });
    let diagnostic = '';
    child.stderr.on('data', chunk => { diagnostic += chunk.toString().slice(0, 2000); });
    const exited = once(child, 'exit');
    let ready = false;
    for (let i = 0; i < 100; i++) {
      assert.equal(child.exitCode, null, diagnostic);
      try {
        const response = await fetch(`http://127.0.0.1:${port}/healthz`);
        ready = response.ok && (await response.json()).status === 'ok';
      } catch {}
      if (ready) break;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    assert.ok(ready, `${file}: startup timed out`);
    const page = await fetch(`http://127.0.0.1:${port}/`);
    assert.equal(page.status, 200);
    assert.match(await page.text(), /<html/i);
    child.stdin.end();
    assert.equal((await exited)[0], 0, diagnostic);
    child = undefined;
    console.log(`PASS ${file}: foreign working directory, spaced installation path, health and web entry`);
  }
} finally {
  if (child && child.exitCode === null) {
    child.stdin.end();
    await once(child, 'exit');
  }
  await rm(root, { recursive: true, force: true });
}
