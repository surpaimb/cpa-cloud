// Independently authored synthetic process acceptance for
// docs/employee-self-admin-adjustment-history-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-admin-adjustment-history.mjs <absolute service exe> [--hold-browser]
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const holdBrowser = process.argv[3] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable), 'absolute executable required');
const scratch = path.join(root, `self-admin-adjustment-${randomUUID()}`);
const prerequisites = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
  '--employee-self-wallet-activity-enabled', '--employee-self-wallet-entry-classification-enabled'];
const feature = '--employee-self-admin-adjustment-history-enabled';
const route = '/self/api/v1/billing/admin-adjustments';
const selfPassword = 'synthetic-admin-adjustment-password-123';
let processHandle;

async function selfCall(target, { method = 'GET', cookie = '', body, origin = processHandle.origin, expected = 200 } = {}) {
  const response = await fetch(processHandle.origin + target, { method, redirect: 'manual', signal: AbortSignal.timeout(10000),
    headers: { Origin: origin, ...(cookie ? { Cookie: cookie } : {}),
      ...(method === 'GET' ? {} : { 'X-Self-Request': '1' }),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body) });
  assert.equal(response.status, expected, `${method} ${target} status=${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const raw = await response.text();
  return { body: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '', location: response.headers.get('location'), allow: response.headers.get('allow') };
}

try {
  await mkdir(root, { recursive: true });
  for (const subset of [[], prerequisites.slice(0, 1), prerequisites.slice(0, 2), prerequisites.slice(0, 3)]) {
    const target = path.join(root, `admin-adjustment-rejected-${randomUUID()}`);
    for (const mode of [[], ['--init']]) {
      await run(executable, ['--data-dir', target, ...mode, ...subset, feature], adminPassword + '\n', 1);
      await assert.rejects(stat(target), { code: 'ENOENT' }, 'missing prerequisite wrote persistent data');
    }
  }
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  await selfCall(route + '?currency=USD', { expected: 404 });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...prerequisites, feature]);
  const admin = adminClient(processHandle.origin);
  const adminSession = await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic adjustment employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await selfCall('/self/api/v1/enroll', { method: 'POST',
    body: { employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword } });
  const cookie = enrolled.cookie;
  assert.ok(cookie);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).body.features.employee_self_admin_adjustment_history, true);
  for (const shaped of ['/self/api/v1/billing%5Cadmin-adjustments', '/self/api/v1/billing%255Cadmin-adjustments',
    '/self//api/v1/billing/admin-adjustments']) {
    const blocked = await selfCall(shaped + '?currency=USD', { cookie, expected: 400 });
    assert.equal(blocked.location, null);
  }
  await selfCall(route + '?currency=USD', { expected: 401 });
  await selfCall(route + '?currency=USD', { cookie: admin.cookie(), expected: 401 });
  await selfCall(route + '?currency=USD', { cookie, origin: 'http://evil.invalid', expected: 403 });
  for (const query of ['?currency=usd', '?currency=USD&employee_id=other', '?currency=US%44', '?currency=USD&limit=01']) {
    await selfCall(route + query, { cookie, expected: 400 });
  }
  const wrongMethod = await selfCall(route + '?currency=USD', { method: 'POST', cookie, expected: 405 });
  assert.equal(wrongMethod.allow, 'GET');
  const missing = (await selfCall(route + '?currency=USD', { cookie })).body;
  assert.deepEqual([missing.has_account, missing.items, missing.next_cursor], [false, [], null]);
  const owner = { kind: 'employee', employee_id: employee.id };
  for (const amount of ['42', '-7']) {
    await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: amount });
  }
  await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner: { kind: 'resource', employee_id: employee.id,
    resource_kind: 'response', resource_id: 'synthetic-resource' }, currency: 'USD', amount_micro: '300' });
  const first = (await selfCall(route + '?currency=USD&limit=1', { cookie })).body;
  assert.deepEqual(Object.keys(first).sort(), ['currency', 'has_account', 'items', 'next_cursor', 'window_end', 'window_start']);
  assert.deepEqual(Object.keys(first.items[0]).sort(), ['delta_micro', 'occurred_at']);
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = (await selfCall(route + `?currency=USD&limit=1&cursor=${first.next_cursor}`, { cookie })).body;
  assert.deepEqual([first.items[0].delta_micro, second.items[0].delta_micro].sort(), ['-7', '42']);
  assert.equal(second.next_cursor, null);
  assert.equal(first.window_start, second.window_start);
  assert.equal((await selfCall(route + `?currency=EUR&limit=1&cursor=${first.next_cursor}`, { cookie, expected: 400 })).body.error.code, 'invalid_request');
  assert.equal((await selfCall(`/self/api/v1/billing/entries?currency=USD&limit=1&cursor=${first.next_cursor}`, { cookie, expected: 400 })).body.error.code, 'invalid_request');
  const firstEnabledProcess = processHandle;
  const firstEnabledClosed = once(firstEnabledProcess.child, 'close');
  await stopServer(processHandle);
  await firstEnabledClosed;
  const firstEnabledOutput = firstEnabledProcess.output();
  for (const [name, value] of Object.entries({
    admin_password: adminPassword,
    admin_session: admin.cookie(),
    admin_csrf: adminSession.csrf_token,
    enrollment_secret: enrollment.enrollment_secret,
    employee_password: selfPassword,
    employee_session: cookie,
  })) {
    assert.ok(typeof value === 'string' && value.length > 0, `${name} fixture is missing`);
    assert.ok(!firstEnabledOutput.includes(value), `${name} appeared in first enabled process logs`);
  }

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...prerequisites, feature]);
  const retained = (await selfCall(route + '?currency=USD', { cookie })).body;
  assert.deepEqual(retained.items.map(item => item.delta_micro).sort(), ['-7', '42']);
  assert.ok(!processHandle.output().includes(cookie) && !processHandle.output().includes(selfPassword) &&
    !processHandle.output().includes(adminPassword), 'synthetic secret appeared in logs');
  console.log('employee self admin adjustment isolated process acceptance passed');
  if (holdBrowser) {
    const fixturePath = path.join(scratch, 'browser-fixture.json');
    await writeFile(fixturePath, JSON.stringify({ origin: processHandle.origin, employee_id: employee.id }), { mode: 0o600 });
    console.log(`browser fixture=${fixturePath}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('data', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('self-admin-adjustment-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
