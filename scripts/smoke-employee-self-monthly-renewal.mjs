// Independently authored isolated process acceptance for
// docs/employee-self-monthly-renewal-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-monthly-renewal.mjs <absolute service exe> <go exe> [--hold-browser]
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, rm } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const go = process.argv[3];
const holdBrowser = process.argv[4] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable) && go && path.isAbsolute(go), 'absolute service and Go executables required');
const scratch = path.join(root, `employee-self-monthly-renewal-${randomUUID()}`);
const selfPassword = 'synthetic-self-password-123';
const flags = ['--employee-self-service-enabled', '--employee-self-subscription-status-enabled',
  '--employee-self-wallet-balance-enabled', '--employee-self-subscription-renewal-enabled'];
let processHandle;
let admin;

async function call(route, { method = 'GET', body, cookie = '', csrf = '', expected = 200, requestOrigin } = {}) {
  const origin = processHandle.origin;
  const response = await fetch(origin + route, {
    method, headers: { Origin: requestOrigin ?? origin, 'X-Self-Request': '1',
      ...(cookie ? { Cookie: cookie } : {}), ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route} status ${response.status}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return { value: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '' };
}

async function makeDue(subscriptionID) {
  const helper = spawn(go, ['run', './scripts/acceptance-set-subscription-due', '--db', path.join(scratch, 'cpa-cloud.db'), '--subscription', subscriptionID], {
    cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: 'ignore',
  });
  assert.equal((await once(helper, 'exit'))[0], 0, 'isolated due fixture failed');
}

try {
  await mkdir(scratch, { recursive: true });
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), []);
  await call('/self/api/v1/billing/subscriptions/absent/renewal-quotes', { method: 'POST', expected: 404 });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic monthly renewal employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const self = await call('/self/api/v1/enroll', { method: 'POST', body: {
    employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword,
  } });
  assert.ok(self.cookie && self.value.csrf_token);
  const selfCookie = self.cookie;
  const csrf = self.value.csrf_token;
  assert.equal((await call('/self/api/v1/session', { cookie: selfCookie })).value.features.employee_self_subscription_renewal, true);
  const owner = { kind: 'employee', employee_id: employee.id };
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '200' });
  const plan = (await admin.request('/billing/plans', 'POST', {
    operation_id: randomUUID(), name: 'Synthetic monthly renewal', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'monthly', enabled: true,
  })).plan;
  const original = (await admin.request('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  const browser = (await admin.request('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  await stopServer(processHandle);
  await makeDue(original.id);
  await makeDue(browser.id);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  admin = adminClient(processHandle.origin);
  await admin.login();
  const quoteRoute = `/self/api/v1/billing/subscriptions/${encodeURIComponent(original.id)}/renewal-quotes`;
  const renewRoute = `/self/api/v1/billing/subscriptions/${encodeURIComponent(original.id)}/renew`;
  await call(quoteRoute, { method: 'POST', expected: 401 });
  await call(quoteRoute, { method: 'POST', cookie: selfCookie, csrf: 'bad', expected: 403 });
  const quote = (await call(quoteRoute, { method: 'POST', cookie: selfCookie, csrf, expected: 201 })).value;
  assert.deepEqual(Object.keys(quote).sort(), ['credit_micro', 'currency', 'expires_at', 'interval', 'plan_id',
    'predecessor_id', 'predecessor_period_end_at', 'price_micro', 'quote_token', 'revision']);
  assert.equal(quote.predecessor_id, original.id);
  assert.equal(quote.currency, 'USD');
  assert.equal(quote.price_micro, '10');
  const operationID = randomUUID();
  const request = { operation_id: operationID, quote_token: quote.quote_token, current_password: selfPassword };
  await call(renewRoute, { method: 'POST', cookie: selfCookie, csrf, body: { ...request, current_password: 'wrong-password' }, expected: 401 });
  const first = (await call(renewRoute, { method: 'POST', cookie: selfCookie, csrf, body: request, expected: 201 })).value;
  assert.deepEqual(Object.keys(first).sort(), ['credit_micro', 'currency', 'interval', 'operation_id', 'period_end_at',
    'plan_id', 'predecessor_id', 'price_micro', 'replay', 'revision', 'started_at', 'subscription_id']);
  assert.equal(first.replay, false);
  assert.equal(first.predecessor_id, original.id);
  assert.ok(Date.parse(first.started_at) > Date.parse(quote.predecessor_period_end_at));
  const second = (await call(renewRoute, { method: 'POST', cookie: selfCookie, csrf, body: request })).value;
  assert.equal(second.replay, true);
  assert.equal(second.subscription_id, first.subscription_id);
  await call(renewRoute, { method: 'POST', cookie: selfCookie, csrf, body: { ...request, operation_id: randomUUID() }, expected: 409 });
  const linked = (await admin.request(`/billing/subscriptions/${original.id}`)).successor_id;
  assert.equal(linked, first.subscription_id);
  const balance = await admin.request(`/billing/balances?owner_kind=employee&employee_id=${employee.id}&currency=USD`);
  assert.equal(balance.balance_micro, '230');
  const entries = await admin.request(`/billing/entries?resource_kind=subscription&resource_id=${first.subscription_id}`);
  assert.equal(entries.items.length, 2);
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  await stopServer(processHandle);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  const replay = (await call(renewRoute, { method: 'POST', cookie: selfCookie, csrf, body: request })).value;
  assert.equal(replay.replay, true);
  assert.equal(replay.subscription_id, first.subscription_id);
  assert.ok(!processHandle.output().includes(adminPassword) && !processHandle.output().includes(selfPassword), 'secret appeared in service logs');
  console.log('employee self monthly renewal isolated process acceptance passed');
  if (holdBrowser) {
    admin = adminClient(processHandle.origin);
    await admin.login();
    await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 3, enabled: true });
    console.log(`browser origin=${processHandle.origin} employee=${employee.id} target=${browser.id}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('end', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-self-monthly-renewal-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
