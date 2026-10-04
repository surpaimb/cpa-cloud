// Independently authored synthetic wire acceptance for the wallet-balance route contract.
// Usage: node scripts/smoke-employee-self-wallet-balance-route-boundary.mjs <absolute server executable>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable));
const parent = path.resolve(process.env.CPA_CLOUD_ACCEPTANCE_TEMP_ROOT || os.tmpdir());
const root = await mkdtemp(path.join(parent, 'wallet-balance-route-'));
const dataDir = path.join(root, 'data');
const webDir = path.join(root, 'web');
const adminPassword = `A-${randomBytes(18).toString('hex')}`;
const selfPassword = `S-${randomBytes(18).toString('hex')}`;
const b = '/self/api/v1/billing/balance';
const record = { kind: 'wallet-balance-route-boundary-smoke', cases: [], services: [], cleanup: [], complete: false };
let running = null;
let selfCookie = '';

async function freePort() {
  const listener = net.createServer();
  listener.listen(0, '127.0.0.1');
  await once(listener, 'listening');
  const port = listener.address().port;
  await new Promise((resolve, reject) => listener.close(error => error ? reject(error) : resolve()));
  assert.notEqual(port, Number(process.env.CPA_CLOUD_ACCEPTANCE_FORBID_PORT || '8787'));
  return port;
}

async function portOpen(port) {
  return new Promise(resolve => {
    const socket = net.connect({ host: '127.0.0.1', port });
    socket.setTimeout(500);
    socket.once('connect', () => { socket.destroy(); resolve(true); });
    socket.once('error', () => resolve(false));
    socket.once('timeout', () => { socket.destroy(); resolve(false); });
  });
}

async function init() {
  const proc = spawn(executable, ['--data-dir', dataDir, '--init'], {
    windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'],
  });
  proc.stdin.end(adminPassword + '\n');
  assert.equal((await once(proc, 'exit'))[0], 0);
}

async function start(mode, flags) {
  const port = await freePort();
  const proc = spawn(executable, ['--data-dir', dataDir, '--listen', `127.0.0.1:${port}`,
    '--web-dir', webDir, '--shutdown-on-stdin-eof', ...flags],
  { windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'] });
  running = { proc, port, mode };
  record.services.push({ mode, pid: proc.pid, port });
  for (let i = 0; i < 100; i++) {
    if (proc.exitCode !== null) throw new Error(`${mode} exited before ready`);
    try {
      const response = await fetch(`http://127.0.0.1:${port}/healthz`, { signal: AbortSignal.timeout(700) });
      await response.arrayBuffer();
      if (response.status === 200) return port;
    } catch { /* readiness */ }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`${mode} readiness timeout`);
}

async function stop() {
  if (!running) return;
  const { proc, port, mode } = running;
  if (proc.exitCode === null && proc.signalCode === null) {
    if (!proc.stdin.destroyed) proc.stdin.end();
    if (!(await waitForExit(proc, 5000))) {
      proc.kill('SIGKILL');
      if (!(await waitForExit(proc, 5000))) {
        throw new Error(`${mode} PID ${proc.pid} did not exit after forced stop`);
      }
    }
  }
  let closed = false;
  for (let i = 0; i < 20; i++) {
    closed = !(await portOpen(port));
    if (closed) break;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  const exited = proc.exitCode !== null || proc.signalCode !== null;
  record.cleanup.push({ mode, pid: proc.pid, port, exited, port_closed: closed });
  assert.ok(exited && closed, `${mode} cleanup incomplete`);
  running = null;
}

async function waitForExit(proc, timeoutMs) {
  if (proc.exitCode !== null || proc.signalCode !== null) return true;
  return new Promise(resolve => {
    let timer;
    const onExit = () => {
      clearTimeout(timer);
      resolve(true);
    };
    timer = setTimeout(() => {
      proc.off('exit', onExit);
      resolve(false);
    }, timeoutMs);
    proc.once('exit', onExit);
    if (proc.exitCode !== null || proc.signalCode !== null) {
      proc.off('exit', onExit);
      clearTimeout(timer);
      resolve(true);
    }
  });
}

async function api(port, method, route, body, headers = {}, expected = 200) {
  const origin = `http://127.0.0.1:${port}`;
  const response = await fetch(origin + route, {
    method, redirect: 'manual', signal: AbortSignal.timeout(10000),
    headers: { Origin: origin, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  assert.equal(response.status, expected, `${method} ${route}`);
  return { json: await response.json(), cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '' };
}

async function enroll(port) {
  const admin = await api(port, 'POST', '/admin/api/v1/sessions', { username: 'admin', password: adminPassword });
  assert.ok(admin.cookie && admin.json.csrf_token);
  const auth = { Cookie: admin.cookie, 'X-CSRF-Token': admin.json.csrf_token };
  const employee = await api(port, 'POST', '/admin/api/v1/employees', { name: 'Synthetic balance boundary employee' }, auth, 201);
  const issued = await api(port, 'POST', `/admin/api/v1/employees/${encodeURIComponent(employee.json.id)}/self-enrollment`, {}, auth, 201);
  const self = await api(port, 'POST', '/self/api/v1/enroll', {
    employee_id: employee.json.id, enrollment_secret: issued.json.enrollment_secret, password: selfPassword,
  }, { 'X-Self-Request': '1' });
  assert.ok(self.cookie);
  return self.cookie;
}

async function raw(port, mode, method, target, identity, origin = '') {
  const host = `127.0.0.1:${port}`;
  const lines = [`${method} ${target} HTTP/1.1`, `Host: ${host}`, 'Connection: close'];
  if (identity === 'self') lines.push(`Cookie: ${selfCookie}`);
  if (origin) lines.push(`Origin: ${origin}`);
  const request = Buffer.from(`${lines.join('\r\n')}\r\n\r\n`, 'latin1');
  const response = await new Promise((resolve, reject) => {
    const socket = net.connect({ host: '127.0.0.1', port });
    const chunks = [];
    socket.setTimeout(10000);
    socket.once('connect', () => socket.write(request));
    socket.on('data', chunk => chunks.push(chunk));
    socket.once('end', () => resolve(Buffer.concat(chunks)));
    socket.once('error', reject);
    socket.once('timeout', () => { socket.destroy(); reject(new Error('raw timeout')); });
  });
  const cut = response.indexOf('\r\n\r\n');
  assert.ok(cut >= 0);
  const head = response.subarray(0, cut).toString('latin1');
  const status = Number(/^HTTP\/\d(?:\.\d)? (\d{3})/.exec(head)?.[1]);
  assert.ok(Number.isInteger(status));
  const headers = head.split('\r\n').slice(1).map(line => {
    const colon = line.indexOf(':');
    return [line.slice(0, colon).toLowerCase(), line.slice(colon + 1).trim()];
  });
  const value = name => headers.find(([key]) => key === name)?.[1] ?? '';
  const body = response.subarray(cut + 4);
  let code = '';
  if (value('content-type').includes('application/json') && method !== 'HEAD') {
    try { code = JSON.parse(body.toString('utf8')).error?.code ?? ''; } catch { /* non-JSON body */ }
  }
  const sample = { mode, method, target, identity, status, location: value('location'), allow: value('allow'),
    cache: value('cache-control'), content_type: value('content-type'), code, wire_body_bytes: body.length,
    body_sha256: createHash('sha256').update(body).digest('hex').toUpperCase(),
    web_marker: body.includes('wallet-boundary-web-marker') };
  record.cases.push(sample);
  assert.equal(sample.cache, 'no-store');
  assert.equal(sample.web_marker, false);
  if (method === 'HEAD') assert.equal(sample.wire_body_bytes, 0);
  return sample;
}

try {
  await mkdir(webDir, { recursive: true });
  await writeFile(path.join(webDir, 'index.html'), '<!doctype html><title>wallet-boundary-web-marker</title>');
  await init();
  for (const [mode, flags] of [
    ['base-only', ['--employee-self-service-enabled']],
    ['balance-on', ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled']],
  ]) {
    const port = await start(mode, flags);
    if (mode === 'base-only') selfCookie = await enroll(port);
    const forms = [
      [b + '?currency=USD', false],
      ['/self/api/v1/billing//balance?currency=USD', true],
      ['/self/api/v1/billing/./balance?currency=USD', true],
      [`http://authority.invalid${b}?currency=USD`, true],
      [`HTTP://authority.invalid${b}?currency=USD`, true],
      [`http://synthetic@authority.invalid${b}?currency=USD`, true],
      [`http://${b}?currency=USD`, true],
      [`http:${b}?currency=USD`, true],
      [`custom:${b}?currency=USD`, true],
    ];
    const before = mode === 'balance-on' ? await raw(port, mode, 'GET', b + '?currency=USD', 'self') : null;
    for (const [target, alias] of forms) {
      for (const method of ['GET', 'HEAD']) {
        for (const identity of ['anonymous', 'self']) {
          const sample = await raw(port, mode, method, target, identity);
          const expected = mode === 'base-only' ? 404 : identity === 'anonymous' ? 401 : alias ? 400 : 200;
          assert.equal(sample.status, expected, `${mode} ${method} ${target} ${identity}`);
          assert.equal(sample.location, '');
          assert.equal(sample.allow, '');
          if (mode === 'balance-on' && alias && identity === 'self' && method === 'GET') {
            assert.equal(sample.code, 'invalid_request');
          }
        }
      }
    }
    const near8192 = '/self/api/v1/billing/' + '/'.repeat(8192 - '/self/api/v1/billing/'.length - 'balance'.length) + 'balance';
    const near8191 = '/self/api/v1/billing/' + '/'.repeat(8191 - '/self/api/v1/billing/'.length - 'balance'.length) + 'balance';
    const prefix1024 = 'http://' + 'a'.repeat(1024 - 'http://'.length);
    for (const [label, target, owned] of [
      ['8191 path', near8191 + '?currency=USD', true],
      ['8192 path', near8192, true],
      ['question at byte 8193', near8192 + '?currency=USD', true],
      ['neighbor extension at byte 8193', near8192 + 'x?currency=USD', false],
      ['non-origin prefix 1024', prefix1024 + b + '?currency=USD', true],
      ['non-origin prefix 1025', prefix1024 + 'a' + b + '?currency=USD', false],
    ]) {
      for (const method of ['GET', 'HEAD']) {
        for (const identity of ['anonymous', 'self']) {
          const sample = await raw(port, mode, method, target, identity);
          let expected = mode === 'base-only' ? 404 : identity === 'anonymous' ? 401 : owned ? 400 : 200;
          let location = '';
          if (label === 'neighbor extension at byte 8193') {
            expected = 307;
            location = '/self/api/v1/billing/balancex?currency=USD';
          }
          assert.equal(sample.status, expected, `${mode} ${method} ${label} ${identity}`);
          assert.equal(sample.location, location);
          assert.equal(sample.allow, '');
        }
      }
    }
    if (mode === 'balance-on') {
      const origin = await raw(port, mode, 'GET', '/self/api/v1/billing//balance?currency=USD', 'self', 'https://different.invalid');
      assert.equal(origin.status, 403);
      assert.equal(origin.code, 'request_rejected');
      const after = await raw(port, mode, 'GET', b + '?currency=USD', 'self');
      assert.equal(after.body_sha256, before.body_sha256, 'alias changed wallet projection');
    }
    for (const [target, expected, location] of [
      [b + '-other?currency=USD', 404, ''],
      [b + '%2Dother?currency=USD', 404, ''],
      [b + '/../entries?currency=USD', 307, '/self/api/v1/billing/entries?currency=USD'],
      [b + '/../plans?currency=USD', 404, ''],
      [b + '/' + 'x'.repeat(8200) + '?currency=USD', 404, ''],
    ]) {
      const sample = await raw(port, mode, 'GET', target, 'self');
      assert.equal(sample.status, expected, `sibling or overbudget ${target.slice(0, 100)}`);
      assert.equal(sample.location, location);
    }
    for (const [target, redirect] of [
      [b + '?currency=USD', false],
      ['/self/api/v1/billing//balance?currency=USD', true],
      ['/self/api/v1/billing/./balance?currency=USD', true],
      [b + '-other?currency=USD', false],
      [b + '%2Dother?currency=USD', false],
    ]) {
      for (const method of ['POST', 'PUT', 'DELETE', 'OPTIONS']) {
        for (const identity of ['anonymous', 'self']) {
          const sample = await raw(port, mode, method, target, identity);
          assert.equal(sample.status, redirect ? 307 : 404);
          assert.equal(sample.location, redirect ? b + '?currency=USD' : '');
          assert.equal(sample.allow, '');
          assert.equal(sample.content_type, redirect ? '' : 'text/plain; charset=utf-8');
          assert.equal(sample.code, '');
          assert.equal(sample.wire_body_bytes, redirect ? 0 : 19);
          assert.equal(sample.body_sha256, redirect
            ? 'E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855'
            : 'B16E15764B8BC06C5C3F9F19BC8B99FA48E7894AA5A6CCDAD65DA49BBF564793');
        }
      }
    }
    await stop();
  }
  record.complete = true;
} catch (error) {
  record.error = String(error?.message ?? error);
  process.exitCode = 1;
} finally {
  try { await stop(); } catch (error) {
    record.cleanup_error = String(error?.message ?? error);
    record.complete = false;
    process.exitCode = 1;
  }
  if (record.complete) {
    try {
      const resolvedRoot = await realpath(root);
      assert.ok(resolvedRoot.startsWith(parent + path.sep), 'temporary cleanup escaped requested parent');
      await rm(resolvedRoot, { recursive: true, force: true });
    } catch (error) {
      record.cleanup_error = String(error?.message ?? error);
      record.complete = false;
      process.exitCode = 1;
    }
  }
  const externalEvidencePath = process.env.CPA_CLOUD_WALLET_BALANCE_BOUNDARY_EVIDENCE;
  const failureEvidencePath = path.join(root, 'failure-evidence.json');
  if (!record.complete) {
    record.retained_run_root = root;
    await writeFile(failureEvidencePath, JSON.stringify(record, null, 2) + '\n');
  }
  if (externalEvidencePath) await writeFile(externalEvidencePath, JSON.stringify(record, null, 2) + '\n');
  console.log(JSON.stringify({ complete: record.complete, cases: record.cases.length,
    services: record.services, cleanup: record.cleanup, error: record.error ?? null,
    evidence_path: externalEvidencePath || (record.complete ? null : failureEvidencePath) }));
}
