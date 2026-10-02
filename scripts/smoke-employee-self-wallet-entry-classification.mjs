// Independently authored synthetic process acceptance for docs/employee-self-wallet-entry-classification-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-wallet-entry-classification.mjs <absolute service exe> [--hold-browser]
import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const holdBrowser = process.argv[3] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const scratch = path.join(root, `employee-entry-classification-${randomUUID()}`);
const selfPassword = `synthetic-self-${randomBytes(16).toString('hex')}`;
const flags = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled', '--employee-self-wallet-activity-enabled'];
let processHandle;

async function selfCall(route, { method = 'GET', body, cookie = '', expected = 200, origin = processHandle.origin } = {}) {
  const response = await fetch(processHandle.origin + '/self/api/v1' + route, {
    method, headers: { Origin: origin, 'X-Self-Request': '1', ...(cookie ? { Cookie: cookie } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(10000),
  });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}`);
  if (expected !== 404) assert.equal(response.headers.get('cache-control'), 'no-store');
  return { body: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '' };
}

try {
  await mkdir(root, { recursive: true });
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  await selfCall('/billing/entry-classifications?currency=USD', { expected: 404 });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Classification smoke employee' }, 201);
  const issued = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await selfCall('/enroll', { method: 'POST', body: { employee_id: employee.id,
    enrollment_secret: issued.enrollment_secret, password: selfPassword } });
  const cookie = enrolled.cookie;
  assert.ok(cookie);
  await selfCall('/billing/entry-classifications?currency=USD', { cookie, expected: 404 });
  assert.equal((await selfCall('/session', { cookie })).body.features.employee_self_wallet_entry_classification, false);
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...flags, '--employee-self-wallet-entry-classification-enabled']);
  const current = adminClient(processHandle.origin);
  await current.login();
  assert.equal((await selfCall('/session', { cookie })).body.features.employee_self_wallet_entry_classification, true);
  await selfCall('/billing/entry-classifications?currency=USD', { expected: 401 });
  await selfCall('/billing/entry-classifications?currency=USD', { cookie: current.cookie(), expected: 401 });
  await selfCall('/billing/entry-classifications?currency=USD', { cookie, origin: 'http://evil.invalid', expected: 403 });
  await selfCall('/billing/entry-classifications?currency=usd', { cookie, expected: 400 });
  await selfCall('/billing/entry-classifications?currency=USD&employee_id=other', { cookie, expected: 400 });
  const missing = (await selfCall('/billing/entry-classifications?currency=JPY', { cookie })).body;
  assert.equal(missing.has_account, false);
  assert.deepEqual(missing.items, []);
  const owner = { kind: 'employee', employee_id: employee.id };
  await current.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
  await current.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '-25' });
  const first = (await selfCall('/billing/entry-classifications?currency=USD&limit=1', { cookie })).body;
  assert.deepEqual(Object.keys(first).sort(), ['currency', 'has_account', 'items', 'next_cursor', 'window_end', 'window_start']);
  assert.deepEqual(Object.keys(first.items[0]).sort(), ['delta_micro', 'entry_kind', 'occurred_at']);
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = (await selfCall(`/billing/entry-classifications?currency=USD&limit=1&cursor=${encodeURIComponent(first.next_cursor)}`, { cookie })).body;
  assert.deepEqual([first.items[0].entry_kind, second.items[0].entry_kind].sort(), ['adjustment_credit', 'adjustment_debit']);
  assert.equal(second.next_cursor, null);
  assert.equal(second.window_start, first.window_start);
  assert.equal(second.window_end, first.window_end);
  const old = (await selfCall('/billing/entries?currency=USD&limit=1', { cookie })).body;
  assert.deepEqual(Object.keys(old).sort(), ['currency', 'has_account', 'items', 'next_cursor', 'window_end', 'window_start']);
  assert.deepEqual(Object.keys(old.items[0]).sort(), ['delta_micro', 'occurred_at']);
  assert.ok(old.next_cursor);
  assert.equal((await selfCall(`/billing/entries?currency=USD&limit=1&cursor=${encodeURIComponent(first.next_cursor)}`, { cookie, expected: 400 })).body.error.code, 'invalid_request');
  assert.equal((await selfCall(`/billing/entry-classifications?currency=USD&limit=1&cursor=${encodeURIComponent(old.next_cursor)}`, { cookie, expected: 400 })).body.error.code, 'invalid_request');
  assert.equal((await selfCall(`/billing/entry-classifications?currency=USD&limit=2&cursor=${encodeURIComponent(first.next_cursor)}`, { cookie, expected: 400 })).body.error.code, 'invalid_request');
  assert.ok(!processHandle.output().includes(selfPassword) && !processHandle.output().includes(adminPassword) &&
    !processHandle.output().includes(cookie), 'secret appeared in service logs');
  console.log('employee self wallet entry classification isolated process acceptance passed');
  if (holdBrowser) {
    const fixturePath = path.join(scratch, 'browser-fixture.json');
    await writeFile(fixturePath, JSON.stringify({ origin: processHandle.origin, employee_id: employee.id, password: selfPassword }), { mode: 0o600 });
    console.log(`browser fixture=${fixturePath}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('data', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-entry-classification-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
