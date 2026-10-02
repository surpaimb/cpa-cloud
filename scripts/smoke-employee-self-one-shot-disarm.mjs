// Independently authored synthetic process acceptance for
// docs/employee-self-one-shot-disarm-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-one-shot-disarm.mjs <absolute service exe> [--hold-browser]
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, rm } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const holdBrowser = process.argv[3] === '--hold-browser';
const scratch = path.join(root, `employee-self-one-shot-${randomUUID()}`);
const selfPassword = 'synthetic-self-password-123';
const flags = ['--employee-self-service-enabled', '--employee-self-subscription-status-enabled', '--employee-self-one-shot-renewal-disarm-enabled'];
let processHandle;

async function call(route, { method = 'GET', body, cookie = '', csrf = '', expected = 200, origin } = {}) {
  const target = processHandle.origin;
  const response = await fetch(target + route, {
    method,
    headers: { Origin: origin ?? target, 'X-Self-Request': '1', ...(cookie ? { Cookie: cookie } : {}), ...(csrf ? { 'X-CSRF-Token': csrf } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  const text = await response.text();
  assert.equal(response.status, expected, `${method} ${route} status ${response.status}: ${text.slice(0, 300)}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return { value: text ? JSON.parse(text) : {}, cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '' };
}

try {
  await mkdir(scratch, { recursive: true });
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic one-shot employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const self = await call('/self/api/v1/enroll', { method: 'POST', body: { employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword } });
  assert.ok(self.cookie && self.value.csrf_token);
  const selfCookie = self.cookie;
  const csrf = self.value.csrf_token;
  const owner = { kind: 'employee', employee_id: employee.id };
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
  const plan = (await admin.request('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Synthetic one-shot plan', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'monthly', enabled: true })).plan;
  const target = (await admin.request('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  const browserTarget = (await admin.request('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  const pathFor = id => `/billing/subscriptions/${encodeURIComponent(id)}/one-shot-renewal`;
  const none = (await call('/self/api/v1' + pathFor(target.id), { cookie: selfCookie })).value;
  assert.deepEqual(Object.keys(none).sort(), ['due_at', 'reason', 'revision', 'state', 'subscription_id', 'terminal_at']);
  assert.equal(none.state, 'none');
  assert.equal(none.due_at, null);
  for (const subscription of [target, browserTarget]) {
    const armed = await admin.request(pathFor(subscription.id), 'POST', { operation_id: randomUUID(), expected_revision: 1 });
    assert.equal(armed.one_shot_renewal.state, 'armed');
  }
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  const route = '/self/api/v1' + pathFor(target.id);
  const armed = (await call(route, { cookie: selfCookie })).value;
  assert.equal(armed.state, 'armed');
  assert.equal(armed.revision, 1);
  const disarmRoute = route + '/disarm';
  const operationID = randomUUID();
  await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: { operation_id: operationID, expected_revision: 1 }, expected: 400 });
  await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: { operation_id: operationID, expected_revision: 1, current_password: 'wrong-password' }, expected: 401 });
  const request = { operation_id: operationID, expected_revision: 1, current_password: selfPassword };
  const first = (await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: request })).value;
  assert.deepEqual(Object.keys(first).sort(), ['due_at', 'operation_id', 'reason', 'replay', 'revision', 'state', 'subscription_id', 'terminal_at']);
  assert.equal(first.replay, false);
  assert.equal(first.state, 'disarmed');
  assert.equal(first.revision, 2);
  const repeat = (await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: request })).value;
  assert.equal(repeat.replay, true);
  assert.equal(repeat.terminal_at, first.terminal_at);
  await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: { ...request, operation_id: randomUUID() }, expected: 409 });
  await stopServer(processHandle);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const restarted = (await call(disarmRoute, { method: 'POST', cookie: selfCookie, csrf, body: request })).value;
  assert.equal(restarted.replay, true);
  assert.equal(restarted.terminal_at, first.terminal_at);
  const browserArmed = (await call('/self/api/v1' + pathFor(browserTarget.id), { cookie: selfCookie })).value;
  assert.equal(browserArmed.state, 'armed');
  assert.ok(!processHandle.output().includes(adminPassword) && !processHandle.output().includes(selfPassword), 'secret appeared in logs');
  console.log('employee self one-shot disarm process acceptance passed');
  if (holdBrowser) {
    console.log(`browser origin=${processHandle.origin} employee=${employee.id} target=${browserTarget.id}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('end', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-self-one-shot-')) await rm(scratch, { recursive: true, force: true });
}
