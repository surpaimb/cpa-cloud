// Independently authored isolated-process acceptance for
// docs/employee-self-redemption-credit-history-contract.md. Synthetic facts only.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-redemption-credit-history.mjs <absolute service exe> [--hold-browser]
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const holdBrowser = process.argv[3] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const scratch = path.join(root, `self-redemption-credit-history-${randomUUID()}`);
const selfPassword = 'synthetic-history-self-password-123';
const prerequisites = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
  '--employee-self-wallet-activity-enabled', '--employee-self-wallet-entry-classification-enabled'];
const historyFlag = '--employee-self-redemption-credit-history-enabled';
const route = '/self/api/v1/billing/redemption-credits';
let processHandle;

async function selfCall(target, { method = 'GET', body, cookie = '', csrf = '', origin = processHandle.origin, expected = 200 } = {}) {
  const response = await fetch(processHandle.origin + target, {
    method, headers: { Origin: origin, ...(cookie ? { Cookie: cookie } : {}),
      ...(method === 'GET' ? {} : { 'X-Self-Request': '1', 'X-CSRF-Token': csrf }),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(10000),
  });
  assert.equal(response.status, expected, `${method} ${target} returned ${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const raw = await response.text();
  return { value: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '', allow: response.headers.get('allow'), location: response.headers.get('location') };
}

try {
  await mkdir(root, { recursive: true });
  for (const missing of [[], prerequisites.slice(0, 1), prerequisites.slice(0, 2), prerequisites.slice(0, 3)]) {
    const unwritten = path.join(root, `redemption-history-rejected-${randomUUID()}`);
    await run(executable, ['--data-dir', unwritten, '--init', ...missing, historyFlag], adminPassword + '\n', 1);
    await assert.rejects(stat(unwritten), { code: 'ENOENT' }, 'invalid --init wrote persistent data');
  }
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  await selfCall(route + '?currency=USD', { expected: 404 });
  const shapedPaths = ['/self/api/v1/billing%5Credemption-credits', '/self/api/v1/billing%255Credemption-credits',
    '/self/api/v1/billing/redemption-credits%5C', '/self//api/v1/billing/redemption-credits',
    '/self/api/v1/billing%2Fredemption-credits'];
  for (const shaped of shapedPaths) {
    const blocked = await selfCall(shaped + '?currency=USD', { expected: 404 });
    assert.equal(blocked.location, null, 'disabled shaped path redirected');
  }
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...prerequisites, historyFlag, '--employee-self-redemption-enabled']);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic redemption history employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await selfCall('/self/api/v1/enroll', { method: 'POST', body: { employee_id: employee.id,
    enrollment_secret: enrollment.enrollment_secret, password: selfPassword } });
  const cookie = enrolled.cookie;
  const csrf = enrolled.value.csrf_token;
  assert.ok(cookie && csrf);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).value.features.employee_self_redemption_credit_history, true);
  for (const shaped of shapedPaths) {
    const blocked = await selfCall(shaped + '?currency=USD', { cookie, expected: 400 });
    assert.equal(blocked.location, null, 'enabled shaped path redirected');
  }
  await selfCall(route + '?currency=USD', { expected: 401 });
  await selfCall(route + '?currency=USD', { cookie: admin.cookie(), expected: 401 });
  await selfCall(route + '?currency=USD', { cookie, origin: 'http://evil.invalid', expected: 403 });
  await selfCall(route + '?currency=usd', { cookie, expected: 400 });
  await selfCall(route + '?currency=USD&employee_id=other', { cookie, expected: 400 });
  const wrongMethod = await selfCall(route + '?currency=USD', { method: 'POST', cookie, csrf, expected: 405 });
  assert.equal(wrongMethod.allow, 'GET');
  const missing = (await selfCall(route + '?currency=JPY', { cookie })).value;
  assert.deepEqual([missing.has_account, missing.items, missing.next_cursor], [false, [], null]);

  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  for (const amount of ['41', '43']) {
    const issued = await admin.request('/billing/redemption-codes', 'POST', { operation_id: randomUUID(), currency: 'EUR',
      amount_micro: amount, max_uses: 1, expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString() });
    assert.ok(typeof issued.code === 'string' && issued.code.startsWith('cpa_'));
    const credited = (await selfCall('/self/api/v1/billing/redemptions', { method: 'POST', cookie, csrf, expected: 201,
      body: { operation_id: randomUUID(), code: issued.code, current_password: selfPassword } })).value;
    assert.equal(credited.amount_micro, amount);
  }
  const first = (await selfCall(route + '?currency=EUR&limit=1', { cookie })).value;
  assert.deepEqual(Object.keys(first).sort(), ['currency', 'has_account', 'items', 'next_cursor', 'window_end', 'window_start']);
  assert.deepEqual(Object.keys(first.items[0]).sort(), ['amount_micro', 'credited_at']);
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = (await selfCall(route + `?currency=EUR&limit=1&cursor=${encodeURIComponent(first.next_cursor)}`, { cookie })).value;
  assert.deepEqual([first.items[0].amount_micro, second.items[0].amount_micro].sort(), ['41', '43']);
  assert.equal(second.next_cursor, null);
  assert.equal(second.window_start, first.window_start);
  assert.equal(second.window_end, first.window_end);
  assert.equal((await selfCall('/self/api/v1/billing/entry-classifications?currency=EUR&limit=1&cursor=' + encodeURIComponent(first.next_cursor),
    { cookie, expected: 400 })).value.error.code, 'invalid_request');
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...prerequisites, historyFlag]);
  const retained = (await selfCall(route + '?currency=EUR', { cookie })).value;
  assert.deepEqual(retained.items.map(item => item.amount_micro).sort(), ['41', '43']);
  await selfCall('/self/api/v1/billing/redemptions', { method: 'POST', cookie, csrf, expected: 404 });
  assert.ok(!processHandle.output().includes(cookie) && !processHandle.output().includes(selfPassword) &&
    !processHandle.output().includes(adminPassword), 'synthetic secret appeared in service logs');
  console.log('employee self redemption credit history isolated process acceptance passed');
  if (holdBrowser) {
    const fixturePath = path.join(scratch, 'browser-fixture.json');
    await writeFile(fixturePath, JSON.stringify({ origin: processHandle.origin, employee_id: employee.id }), { mode: 0o600 });
    console.log(`browser fixture=${fixturePath}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('data', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('self-redemption-credit-history-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
