// Independently authored isolated-process acceptance for
// docs/employee-self-topup-credit-history-contract.md. Synthetic local facts only.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-topup-credit-history.mjs <absolute service exe> [--hold-browser]
import assert from 'node:assert/strict';
import { createHmac, randomUUID } from 'node:crypto';
import { mkdir, rm, stat, writeFile } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const holdBrowser = process.argv[3] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const scratch = path.join(root, `self-topup-credit-history-${randomUUID()}`);
const selfPassword = 'synthetic-topup-self-password-123';
const webhookSecret = 'synthetic-topup-webhook-secret-at-least-32-bytes';
const prerequisites = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
  '--employee-self-wallet-activity-enabled', '--employee-self-wallet-entry-classification-enabled'];
const historyFlag = '--employee-self-topup-credit-history-enabled';
const route = '/self/api/v1/billing/topup-credits';
let processHandle;

async function selfCall(target, { method = 'GET', body, cookie = '', origin = processHandle.origin, expected = 200 } = {}) {
  const response = await fetch(processHandle.origin + target, { method,
    headers: { Origin: origin, ...(cookie ? { Cookie: cookie } : {}),
      ...(method === 'GET' ? {} : { 'X-Self-Request': '1' }),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), redirect: 'manual', signal: AbortSignal.timeout(10000) });
  assert.equal(response.status, expected, `${method} ${target} returned ${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const raw = await response.text();
  return { value: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '', allow: response.headers.get('allow'), location: response.headers.get('location') };
}

async function rawPath(target, cookie = '') {
  const base = new URL(processHandle.origin);
  return new Promise((resolve, reject) => {
    const request = http.request({ hostname: base.hostname, port: base.port, path: target, method: 'GET',
      headers: cookie ? { Cookie: cookie } : {} }, response => {
      response.resume();
      response.on('end', () => resolve({ status: response.statusCode, cache: response.headers['cache-control'],
        location: response.headers.location, allow: response.headers.allow }));
    });
    request.on('error', reject);
    request.end();
  });
}

try {
  await mkdir(root, { recursive: true });
  for (const missing of [[], prerequisites.slice(0, 1), prerequisites.slice(0, 2), prerequisites.slice(0, 3)]) {
    const unwritten = path.join(root, `topup-history-rejected-${randomUUID()}`);
    await run(executable, ['--data-dir', unwritten, '--init', ...missing, historyFlag], adminPassword + '\n', 1);
    await assert.rejects(stat(unwritten), { code: 'ENOENT' }, 'invalid --init wrote persistent data');
  }
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  assert.equal((await selfCall(route + '?currency=USD', { expected: 404 })).location, null);
  const rawAliases = ['/self/api/v1/billing/./topup-credits?currency=USD',
    '/self/api/v1/billing/topup-credits/../other?currency=USD'];
  for (const alias of rawAliases) {
    const off = await rawPath(alias);
    assert.deepEqual(off, { status: 404, cache: 'no-store', location: undefined, allow: undefined });
  }
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [...prerequisites, historyFlag]);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic topup history employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await selfCall('/self/api/v1/enroll', { method: 'POST', expected: 200,
    body: { employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword } });
  const cookie = enrolled.cookie;
  assert.ok(cookie);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).value.features.employee_self_topup_credit_history, true);
  for (const alias of rawAliases) {
    const on = await rawPath(alias, cookie);
    assert.deepEqual(on, { status: 400, cache: 'no-store', location: undefined, allow: undefined });
  }
  await selfCall(route + '?currency=USD', { expected: 401 });
  await selfCall(route + '?currency=USD', { cookie: admin.cookie(), expected: 401 });
  await selfCall(route + '?currency=USD', { cookie, origin: 'http://invalid.example', expected: 403 });
  await selfCall(route + '?currency=usd', { cookie, expected: 400 });
  const wrong = await selfCall(route + '?currency=USD', { method: 'POST', cookie, expected: 405 });
  assert.equal(wrong.allow, 'GET');
  const empty = (await selfCall(route + '?currency=USD', { cookie })).value;
  assert.deepEqual([empty.has_account, empty.items, empty.next_cursor], [false, [], null]);

  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  const connector = await admin.request('/billing/payment-connectors', 'POST',
    { operation_id: randomUUID(), name: 'Synthetic local callback', webhook_secret: webhookSecret, enabled: true });
  const connectorID = connector.connector.id;
  const payments = [];
  for (const amount of ['41', '43']) {
    const created = await admin.request('/billing/topups', 'POST', { operation_id: randomUUID(),
      owner: { kind: 'employee', employee_id: employee.id }, connector_id: connectorID, currency: 'USD', amount_micro: amount });
    const paymentID = created.topup.payment_id;
    payments.push(paymentID);
    const body = JSON.stringify({ status: 'paid', payment_id: paymentID, external_reference: created.topup.external_reference,
      amount_micro: amount, currency: 'USD' });
    const timestamp = String(Math.floor(Date.now() / 1000));
    const eventID = `synthetic-topup-${randomUUID()}`;
    const signature = createHmac('sha256', webhookSecret).update(`${timestamp}\n${eventID}\n${body}`).digest('hex');
    const response = await fetch(processHandle.origin + '/admin/api/v1/billing/payment-callbacks/' + connectorID,
      { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Billing-Event-ID': eventID,
        'X-Billing-Timestamp': timestamp, 'X-Billing-Signature': signature }, body, signal: AbortSignal.timeout(10000) });
    assert.equal(response.status, 200, `signed callback returned ${response.status}`);
    await response.arrayBuffer();
  }
  const first = (await selfCall(route + '?currency=USD&limit=1', { cookie })).value;
  assert.deepEqual(Object.keys(first).sort(), ['currency', 'has_account', 'items', 'next_cursor', 'window_end', 'window_start']);
  assert.deepEqual(Object.keys(first.items[0]).sort(), ['amount_micro', 'credited_at']);
  assert.ok(first.next_cursor && !first.next_cursor.includes(employee.id));
  const second = (await selfCall(route + `?currency=USD&limit=1&cursor=${first.next_cursor}`, { cookie })).value;
  assert.deepEqual([first.items[0].amount_micro, second.items[0].amount_micro].sort(), ['41', '43']);
  assert.equal(second.next_cursor, null);
  assert.equal(first.window_start, second.window_start);
  await admin.request('/billing/refunds', 'POST', { operation_id: randomUUID(), payment_id: payments[0], amount_micro: '41' });
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  const retained = (await selfCall(route + '?currency=USD', { cookie })).value;
  assert.deepEqual(retained.items.map(item => item.amount_micro).sort(), ['41', '43']);
  assert.ok(!processHandle.output().includes(cookie) && !processHandle.output().includes(selfPassword) &&
    !processHandle.output().includes(webhookSecret), 'synthetic secret appeared in service logs');
  console.log('employee self topup credit history isolated process acceptance passed');
  if (holdBrowser) {
    const fixturePath = path.join(scratch, 'browser-fixture.json');
    await writeFile(fixturePath, JSON.stringify({ origin: processHandle.origin, employee_id: employee.id }), { mode: 0o600 });
    console.log(`browser fixture=${fixturePath}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('data', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('self-topup-credit-history-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
