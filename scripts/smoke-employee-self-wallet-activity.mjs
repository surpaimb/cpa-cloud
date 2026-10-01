// Independently authored synthetic process acceptance for docs/employee-self-wallet-activity-contract.md.
// Usage: node scripts/smoke-employee-self-wallet-activity.mjs <absolute service executable>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-self-wallet-activity-'));
const adminPassword = randomBytes(24).toString('hex');
const secretValues = [adminPassword];
const logChunks = [];
let child;
let origin = '';
let adminCookie = '';
let adminCSRF = '';

async function freePort() {
  const probe = net.createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise((resolve) => probe.close(resolve));
  assert.notEqual(port, 8787);
  return port;
}

async function start(flags) {
  const port = await freePort();
  origin = `http://127.0.0.1:${port}`;
  child = spawn(executable, ['--data-dir', root, '--listen', `127.0.0.1:${port}`, '--shutdown-on-stdin-eof', ...flags], {
    windowsHide: true, stdio: ['pipe', 'ignore', 'pipe'],
  });
  child.stderr.on('data', (data) => logChunks.push(data.toString()));
  for (let i = 0; i < 120; i++) {
    if (child.exitCode !== null) throw new Error(`service exited with ${child.exitCode}`);
    try {
      const response = await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) });
      if (response.ok) return;
    } catch { /* wait for local listener */ }
    await delay(100);
  }
  throw new Error('service readiness timeout');
}

async function stop() {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const exit = once(child, 'exit');
  child.stdin.end();
  const timer = setTimeout(() => child.kill(), 5000);
  try { await exit; } finally { clearTimeout(timer); }
}

async function call(route, { method = 'GET', body, cookie = '', csrf = '', self = false, bearer = '', expected = 200, requestOrigin = origin } = {}) {
  const headers = { Origin: requestOrigin };
  if (cookie) headers.Cookie = cookie;
  if (csrf) headers['X-CSRF-Token'] = csrf;
  if (self) headers['X-Self-Request'] = '1';
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(origin + route, {
    method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const contentType = response.headers.get('content-type') ?? '';
  return { body: raw && contentType.includes('application/json') ? JSON.parse(raw) : raw || null, cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '' };
}

async function admin(route, method = 'GET', body, expected = 200) {
  return (await call('/admin/api/v1' + route, { method, body, cookie: adminCookie, csrf: adminCSRF, expected })).body;
}

async function loginAdmin() {
  const result = await call('/admin/api/v1/sessions', { method: 'POST', body: { username: 'admin', password: adminPassword } });
  adminCookie = result.cookie;
  adminCSRF = result.body.csrf_token;
  assert.ok(adminCookie && adminCSRF);
  secretValues.push(adminCookie, adminCSRF);
}

async function enroll(employeeID) {
  const issued = await admin(`/employees/${employeeID}/self-enrollment`, 'POST', {}, 201);
  secretValues.push(issued.enrollment_secret);
  const result = await call('/self/api/v1/enroll', {
    method: 'POST', self: true, body: { employee_id: employeeID, enrollment_secret: issued.enrollment_secret, password: 'synthetic-self-password-123' },
  });
  assert.equal(result.body.profile.id, employeeID);
  secretValues.push(result.cookie);
  return result.cookie;
}

async function adjust(owner, currency, amount_micro) {
  return admin('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency, amount_micro });
}

async function activity(cookie, currency, limit = 1, cursor = '', expected = 200) {
  const query = `?currency=${currency}&limit=${limit}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
  return (await call('/self/api/v1/billing/entries' + query, { cookie, self: true, expected })).body;
}

try {
  const initialized = spawn(executable, ['--data-dir', root, '--init'], { windowsHide: true, stdio: ['pipe', 'ignore', 'pipe'] });
  const initialExit = once(initialized, 'exit');
  initialized.stdin.end(adminPassword + '\n');
  assert.equal((await initialExit)[0], 0, 'initialization failed');

  await start([]);
  await call('/self/api/v1/billing/entries?currency=USD', { expected: 404 });
  await stop();

  await start(['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled']);
  await loginAdmin();
  const employee = await admin('/employees', 'POST', { name: 'Activity smoke employee' }, 201);
  const selfCookie = await enroll(employee.id);
  await call('/self/api/v1/billing/entries?currency=USD', { cookie: selfCookie, self: true, expected: 404 });
  await stop();

  const flags = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled', '--employee-self-wallet-activity-enabled'];
  await start(flags);
  await loginAdmin();
  assert.equal((await call('/self/api/v1/session', { cookie: selfCookie, self: true })).body.features.employee_self_wallet_activity, true);
  await call('/self/api/v1/billing/entries?currency=USD', { expected: 401 });
  await call('/self/api/v1/billing/entries?currency=USD', { cookie: adminCookie, expected: 401 });
  await call('/self/api/v1/billing/entries?currency=USD', { bearer: 'not-a-self-session', expected: 401 });
  await call('/self/api/v1/billing/entries?currency=USD', { cookie: selfCookie, requestOrigin: 'http://evil.invalid', expected: 403 });
  await call('/self/api/v1/billing/entries?currency=usd', { cookie: selfCookie, expected: 400 });
  await call('/self/api/v1/billing/entries?currency=USD&employee_id=other', { cookie: selfCookie, expected: 400 });
  const missing = await activity(selfCookie, 'JPY');
  assert.equal(missing.has_account, false);
  assert.deepEqual(missing.items, []);
  assert.equal(missing.next_cursor, null);

  const own = { kind: 'employee', employee_id: employee.id };
  await adjust(own, 'USD', '100');
  await adjust(own, 'USD', '-100');
  await adjust(own, 'EUR', '7');
  const other = await admin('/employees', 'POST', { name: 'Other activity employee' }, 201);
  await adjust({ kind: 'employee', employee_id: other.id }, 'USD', '91');
  const key = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'Synthetic Key', operation_id: randomUUID() }, 201);
  secretValues.push(key.key);
  await adjust({ kind: 'key', employee_id: employee.id, key_id: key.id }, 'USD', '500');
  await adjust({ kind: 'resource', employee_id: employee.id, resource_kind: 'response', resource_id: 'synthetic-resource' }, 'USD', '600');
  const first = await activity(selfCookie, 'USD');
  assert.equal(first.has_account, true);
  assert.equal(first.items.length, 1);
  assert.ok(['100', '-100'].includes(first.items[0].delta_micro));
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = await activity(selfCookie, 'USD', 1, first.next_cursor);
  assert.equal(second.items.length, 1);
  assert.deepEqual([first.items[0].delta_micro, second.items[0].delta_micro].sort(), ['-100', '100']);
  assert.equal(second.next_cursor, null);
  assert.equal(second.window_start, first.window_start);
  assert.equal(second.window_end, first.window_end);
  assert.equal((await activity(selfCookie, 'USD', 2, first.next_cursor, 400)).error.code, 'invalid_request');
  const tampered = (first.next_cursor[0] === 'A' ? 'B' : 'A') + first.next_cursor.slice(1);
  assert.equal((await activity(selfCookie, 'USD', 1, tampered, 400)).error.code, 'invalid_request');
  assert.deepEqual((await call('/self/api/v1/billing/balance?currency=USD', { cookie: selfCookie, self: true })).body,
    { currency: 'USD', has_account: true, amount_micro: '0' });
  const otherCookie = await enroll(other.id);
  assert.equal((await activity(otherCookie, 'USD', 1, first.next_cursor, 400)).error.code, 'invalid_request');
  assert.equal((await activity(otherCookie, 'USD')).items[0].delta_micro, '91');
  await stop();

  await start(flags);
  assert.equal((await activity(selfCookie, 'USD', 1, first.next_cursor)).items[0].delta_micro, second.items[0].delta_micro);
  assert.ok(secretValues.every((secret) => secret && !logChunks.join('').includes(secret)), 'secret appeared in service logs');
  console.log('employee self wallet activity process acceptance passed');
} finally {
  await stop();
  const resolved = path.resolve(root);
  if (path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-self-wallet-activity-')) {
    await rm(resolved, { recursive: true, force: true });
  }
}
