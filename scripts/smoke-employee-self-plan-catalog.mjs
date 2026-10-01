// Independently authored synthetic process acceptance for docs/employee-self-plan-catalog-contract.md.
// Usage: node scripts/smoke-employee-self-plan-catalog.mjs <absolute service executable>
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
const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-self-plan-catalog-'));
const adminPassword = randomBytes(24).toString('hex');
const secrets = [adminPassword];
const logs = [];
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
  child.stderr.on('data', (data) => logs.push(data.toString()));
  for (let i = 0; i < 120; i++) {
    if (child.exitCode !== null) throw new Error(`service exited with ${child.exitCode}`);
    try {
      const response = await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) });
      if (response.ok) return;
    } catch { /* Wait for the isolated listener. */ }
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
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}: ${raw.slice(0, 160)}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return {
    body: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw || null,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '',
  };
}

async function admin(route, method = 'GET', body, expected = 200) {
  return (await call('/admin/api/v1' + route, { method, body, cookie: adminCookie, csrf: adminCSRF, expected })).body;
}

async function loginAdmin() {
  const result = await call('/admin/api/v1/sessions', { method: 'POST', body: { username: 'admin', password: adminPassword } });
  adminCookie = result.cookie;
  adminCSRF = result.body.csrf_token;
  assert.ok(adminCookie && adminCSRF);
  secrets.push(adminCookie, adminCSRF);
}

async function enroll(employeeID) {
  const issued = await admin(`/employees/${employeeID}/self-enrollment`, 'POST', {}, 201);
  secrets.push(issued.enrollment_secret);
  const result = await call('/self/api/v1/enroll', { method: 'POST', self: true,
    body: { employee_id: employeeID, enrollment_secret: issued.enrollment_secret, password: 'synthetic-self-password-123' } });
  secrets.push(result.cookie);
  return result.cookie;
}

async function catalog(cookie, currency = 'USD', limit = 1, cursor = '', expected = 200) {
  const query = `?currency=${currency}&limit=${limit}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
  return (await call('/self/api/v1/billing/plans' + query, { cookie, self: true, expected })).body;
}

try {
  const invalid = spawn(executable, ['--data-dir', root, '--employee-self-plan-catalog-enabled'], { windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'] });
  const invalidExit = once(invalid, 'exit');
  invalid.stderr.on('data', (data) => logs.push(data.toString()));
  assert.notEqual((await invalidExit)[0], 0, 'catalog flag accepted without self service');

  const initialized = spawn(executable, ['--data-dir', root, '--init'], { windowsHide: true, stdio: ['pipe', 'ignore', 'pipe'] });
  const initialExit = once(initialized, 'exit');
  initialized.stdin.end(adminPassword + '\n');
  assert.equal((await initialExit)[0], 0, 'initialization failed');

  await start([]);
  await call('/self/api/v1/billing/plans?currency=USD', { expected: 404 });
  await stop();

  await start(['--employee-self-service-enabled']);
  await loginAdmin();
  const employee = await admin('/employees', 'POST', { name: 'Catalog smoke employee' }, 201);
  const selfCookie = await enroll(employee.id);
  await call('/self/api/v1/billing/plans?currency=USD', { cookie: selfCookie, self: true, expected: 404 });
  await stop();

  const flags = ['--employee-self-service-enabled', '--employee-self-plan-catalog-enabled'];
  await start(flags);
  await loginAdmin();
  assert.equal((await call('/self/api/v1/session', { cookie: selfCookie, self: true })).body.features.employee_self_plan_catalog, true);
  await call('/self/api/v1/billing/plans?currency=USD', { expected: 401 });
  await call('/self/api/v1/billing/plans?currency=USD', { cookie: adminCookie, expected: 401 });
  await call('/self/api/v1/billing/plans?currency=USD', { bearer: 'not-a-self-session', expected: 401 });
  await call('/self/api/v1/billing/plans?currency=USD', { cookie: selfCookie, requestOrigin: 'http://evil.invalid', expected: 403 });
  await call('/self/api/v1/billing/plans?currency=usd', { cookie: selfCookie, expected: 400 });
  await call('/self/api/v1/billing/plans?currency=USD&employee_id=x', { cookie: selfCookie, expected: 400 });
  assert.deepEqual(await catalog(selfCookie), { currency: 'USD', available: false, items: [], next_cursor: null });

  const monthly = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Current monthly', currency: 'USD', price_micro: '13', credit_micro: '29', interval: 'monthly', enabled: true })).plan;
  const oneTime = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Current once', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'one_time', enabled: true })).plan;
  const disabled = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Disabled USD', currency: 'USD', price_micro: '30', credit_micro: '40', interval: 'monthly', enabled: false })).plan;
  const euro = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Enabled EUR', currency: 'EUR', price_micro: '50', credit_micro: '60', interval: 'monthly', enabled: true })).plan;
  assert.deepEqual(await catalog(selfCookie), { currency: 'USD', available: false, items: [], next_cursor: null });

  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  const first = await catalog(selfCookie);
  assert.equal(first.currency, 'USD');
  assert.equal(first.available, true);
  assert.equal(first.items.length, 1);
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = await catalog(selfCookie, 'USD', 1, first.next_cursor);
  assert.equal(second.items.length, 1);
  assert.equal(second.next_cursor, null);
  const ids = [first.items[0].plan_id, second.items[0].plan_id].sort();
  assert.deepEqual(ids, [monthly.id, oneTime.id].sort());
  assert.ok(!ids.includes(disabled.id) && !ids.includes(euro.id));
  for (const item of [...first.items, ...second.items]) {
    assert.deepEqual(Object.keys(item).sort(), ['credit_micro', 'interval', 'name', 'plan_id', 'price_micro', 'revision']);
    assert.match(item.price_micro, /^[1-9][0-9]*$/);
    assert.match(item.credit_micro, /^[1-9][0-9]*$/);
  }
  assert.equal((await catalog(selfCookie, 'EUR')).items[0].plan_id, euro.id);
  assert.equal((await catalog(selfCookie, 'JPY')).items.length, 0);
  assert.equal((await catalog(selfCookie, 'USD', 2, first.next_cursor, 400)).error.code, 'invalid_request');
  assert.equal((await catalog(selfCookie, 'EUR', 1, first.next_cursor, 400)).error.code, 'invalid_request');
  const tampered = (first.next_cursor[0] === 'A' ? 'B' : 'A') + first.next_cursor.slice(1);
  assert.equal((await catalog(selfCookie, 'USD', 1, tampered, 400)).error.code, 'invalid_request');
  const other = await admin('/employees', 'POST', { name: 'Other catalog employee' }, 201);
  const otherCookie = await enroll(other.id);
  assert.equal((await catalog(otherCookie, 'USD', 1, first.next_cursor, 400)).error.code, 'invalid_request');

  const updated = (await admin(`/billing/plans/${monthly.id}`, 'PUT', { operation_id: randomUUID(), expected_revision: 1, name: 'Updated current monthly', currency: 'USD', price_micro: '17', credit_micro: '31', interval: 'monthly', enabled: true })).plan;
  assert.equal(updated.revision, 2);
  const current = await catalog(selfCookie, 'USD', 50);
  assert.equal(current.items.find((item) => item.plan_id === monthly.id)?.price_micro, '17');
  assert.equal(current.items.find((item) => item.plan_id === monthly.id)?.revision, 2);

  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  assert.deepEqual(await catalog(selfCookie, 'USD', 1, first.next_cursor), { currency: 'USD', available: false, items: [], next_cursor: null });
  await stop();
  await start(flags);
  assert.deepEqual(await catalog(selfCookie), { currency: 'USD', available: false, items: [], next_cursor: null });
  await loginAdmin();
  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 3, enabled: true });
  assert.equal((await catalog(selfCookie, 'USD', 1, first.next_cursor)).items.length, 1);
  assert.ok(secrets.every((secret) => secret && !logs.join('').includes(secret)), 'secret appeared in service logs');
  console.log('employee self plan catalog process acceptance passed');
} finally {
  await stop();
  const resolved = path.resolve(root);
  assert.ok(resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) && path.basename(resolved).startsWith('cpac-self-plan-catalog-'));
  await rm(resolved, { recursive: true, force: true });
}
