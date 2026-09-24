// Shared process harness for the independently authored next-batch acceptance smokes.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { mkdir } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

export const root = path.resolve(process.env.CPA_CLOUD_ACCEPTANCE_TEMP_ROOT ?? '');
assert.ok(process.env.CPA_CLOUD_ACCEPTANCE_TEMP_ROOT && path.isAbsolute(root), 'Acceptance temp root must be explicit and absolute');
export const forbiddenPort = Number(process.env.CPA_CLOUD_ACCEPTANCE_FORBID_PORT ?? '8787');
export const password = randomBytes(24).toString('hex');

export async function reserveOrigin() {
  const probe = http.createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  assert.notEqual(port, forbiddenPort, 'Acceptance selected the forbidden existing-service port');
  return `http://127.0.0.1:${port}`;
}

export async function run(executable, args, input = '', expect = 0) {
  assert.ok(path.isAbsolute(executable), 'Executable path must be absolute');
  const child = spawn(executable, args, { cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  let output = '';
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { output += chunk; if (output.length > (1 << 20)) child.kill('SIGKILL'); });
  child.stdin.end(input);
  const [code, signal] = await once(child, 'exit');
  assert.equal(code, expect, `Command failed code=${code} signal=${signal ?? 'none'} output=${output.slice(-2000)}`);
  return output;
}

export async function initialize(server, dataDir) {
  await mkdir(dataDir, { recursive: true });
  await run(server, ['--data-dir', dataDir, '--init'], password + '\n');
}

export async function startServer(server, dataDir, webDir, extra = []) {
  const origin = await reserveOrigin();
  const child = spawn(server, ['--data-dir', dataDir, '--listen', new URL(origin).host, '--allow-loopback-upstream', '--web-dir', webDir, '--shutdown-on-stdin-eof', ...extra], {
    windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '', launchError;
  child.on('error', error => { launchError = error; });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { output += chunk; if (output.length > (1 << 20)) child.kill('SIGKILL'); });
  for (let attempt = 0; attempt < 120; attempt++) {
    if (launchError || child.exitCode !== null) throw new Error(`Service failed before readiness: ${launchError ?? output.slice(-2000)}`);
    try {
      const response = await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) });
      if (response.ok) return { child, origin, output: () => output };
    } catch { /* own isolated process is not ready yet */ }
    await delay(100);
  }
  child.kill('SIGKILL');
  throw new Error(`Service readiness timeout: ${output.slice(-2000)}`);
}

export async function stopServer(process) {
  if (!process?.child || process.child.exitCode !== null || process.child.signalCode !== null) return;
  const exited = once(process.child, 'exit');
  process.child.stdin.end();
  const timer = setTimeout(() => process.child.kill('SIGKILL'), 8000);
  try {
    const [code] = await exited;
    assert.equal(code, 0, `Service stopped with code ${code}: ${process.output().slice(-2000)}`);
  } finally { clearTimeout(timer); }
}

export function adminClient(origin) {
  let cookie = '', csrf = '';
  return {
    cookie: () => cookie,
    async request(route, method = 'GET', body, expected = 200, headers = {}) {
      const response = await fetch(origin + '/admin/api/v1' + route, {
        method,
        headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json', ...headers },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(10000),
      });
      const setCookie = response.headers.get('set-cookie');
      if (setCookie) cookie = setCookie.split(';')[0];
      assert.equal(response.status, expected, `${method} ${route} returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`);
      if (response.status === 204) return {};
      return response.json();
    },
    async login() {
      const result = await this.request('/sessions', 'POST', { username: 'admin', password });
      csrf = result.csrf_token;
      assert.ok(csrf && cookie, 'Admin session did not return cookie and CSRF token');
      return result;
    },
  };
}

export async function waitFor(check, message, timeout = 10000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const value = await check();
    if (value) return value;
    if (Date.now() >= deadline) throw new Error(message);
    await delay(50);
  }
}
