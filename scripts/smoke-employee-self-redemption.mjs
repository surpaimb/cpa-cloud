// Independently authored isolated-process acceptance for
// docs/employee-self-redemption-contract.md. Uses synthetic credentials only.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-redemption.mjs <absolute service exe>
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, rm, stat } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const scratch = path.join(root, `employee-self-redemption-${randomUUID()}`);
const flags = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled', '--employee-self-redemption-enabled'];
const selfPassword = 'synthetic-redemption-password-123';
let processHandle;

async function selfCall(route, { method = 'GET', body, cookie = '', csrf = '', expected = 200 } = {}) {
  const response = await fetch(processHandle.origin + route, {
    method, headers: { Origin: processHandle.origin, 'X-Self-Request': '1',
      ...(cookie ? { Cookie: cookie } : {}), ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return { value: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '', allow: response.headers.get('allow') };
}

try {
  await mkdir(scratch, { recursive: true });
  const rejected = path.join(scratch, 'rejected-init');
  await run(executable, ['--data-dir', rejected, '--init', '--employee-self-redemption-enabled'], adminPassword + '\n', 1);
  await assert.rejects(stat(rejected), { code: 'ENOENT' }, 'bad flag set created persistent data');
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  await selfCall('/self/api/v1/billing/redemptions', { method: 'POST', body: {}, expected: 404 });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic redemption employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const signedIn = await selfCall('/self/api/v1/enroll', { method: 'POST', body: {
    employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword,
  } });
  const cookie = signedIn.cookie;
  const csrf = signedIn.value.csrf_token;
  assert.ok(cookie && csrf);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).value.features.employee_self_redemption, true);
  const route = '/self/api/v1/billing/redemptions';
  const wrongMethod = await selfCall(route, { cookie, method: 'GET', expected: 405 });
  assert.equal(wrongMethod.allow, 'POST');
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  const issued = await admin.request('/billing/redemption-codes', 'POST', {
    operation_id: randomUUID(), currency: 'EUR', amount_micro: '41', max_uses: 1,
    expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
  });
  const code = issued.code;
  assert.ok(typeof code === 'string' && code.startsWith('cpa_'));
  const request = { operation_id: randomUUID(), code, current_password: selfPassword };
  const balance = () => selfCall('/self/api/v1/billing/balance?currency=EUR', { cookie });
  await selfCall(route, { cookie, csrf, method: 'POST', body: { ...request, code: 'synthetic-invalid-code' }, expected: 409 });
  assert.deepEqual((await balance()).value, { currency: 'EUR', has_account: false, amount_micro: null });
  const first = (await selfCall(route, { cookie, csrf, method: 'POST', body: request, expected: 201 })).value;
  assert.deepEqual(Object.keys(first).sort(), ['amount_micro', 'credited_at', 'currency', 'operation_id', 'replay']);
  assert.equal(first.replay, false);
  assert.equal(first.currency, 'EUR');
  assert.equal(first.amount_micro, '41');
  assert.equal((await balance()).value.amount_micro, '41');
  await selfCall(route, { cookie, csrf, method: 'POST', body: { ...request, operation_id: randomUUID() }, expected: 409 });
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  const replay = (await selfCall(route, { cookie, csrf, method: 'POST', body: request })).value;
  assert.deepEqual(replay, { ...first, replay: true });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const restarted = (await selfCall(route, { cookie, csrf, method: 'POST', body: request })).value;
  assert.deepEqual(restarted, { ...first, replay: true });
  assert.ok(!processHandle.output().includes(code) && !processHandle.output().includes(selfPassword) &&
    !processHandle.output().includes(adminPassword), 'secret appeared in service logs');
  console.log('employee self redemption isolated process acceptance passed');
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-self-redemption-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
