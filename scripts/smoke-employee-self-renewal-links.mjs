// Independently authored isolated-process acceptance for
// docs/employee-self-subscription-renewal-links-contract.md and
// docs/employee-self-subscription-renewal-links-route-boundary-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute scratch parent> node scripts/smoke-employee-self-renewal-links.mjs <absolute service exe> <go exe>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, rm, stat } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const go = process.argv[3];
const holdBrowser = process.argv[4] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable) && go && path.isAbsolute(go), 'absolute service and Go executables required');
const scratch = path.join(root, `employee-self-renewal-links-${randomUUID()}`);
const flags = ['--employee-self-service-enabled', '--employee-self-subscription-status-enabled',
  '--employee-self-subscription-renewal-links-enabled'];
const bothFlags = [...flags, '--employee-self-wallet-balance-enabled', '--employee-self-subscription-renewal-enabled'];
const selfPassword = 'synthetic-renewal-links-password-123';
let processHandle;

async function selfCall(route, { method = 'GET', cookie = '', body, expected = 200, origin } = {}) {
  const response = await fetch(processHandle.origin + route, {
    method,
    headers: { ...(origin === null ? {} : { Origin: origin ?? processHandle.origin }), 'X-Self-Request': '1',
      ...(cookie ? { Cookie: cookie } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route}: ${raw.slice(0, 250)}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return {
    value: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    cookie: response.headers.get('set-cookie')?.split(';')[0] ?? '',
    allow: response.headers.get('allow'),
  };
}

// Send the literal request-target and never follow a ServeMux cleaning redirect.
// WHATWG fetch URL construction would normalize dot segments before dispatch.
async function rawBoundaryCall(route, { method = 'GET', cookie = '', expected, code = '', allow = '' } = {}) {
  const origin = new URL(processHandle.origin);
  const result = await new Promise((resolve, reject) => {
    const request = http.request({ hostname: origin.hostname, port: origin.port, method, path: route,
      headers: cookie ? { Cookie: cookie } : {} }, response => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { body += chunk; });
      response.on('end', () => resolve({ status: response.statusCode, headers: response.headers, body }));
    });
    request.setTimeout(10000, () => request.destroy(new Error('raw route timeout')));
    request.on('error', reject);
    request.end();
  });
  assert.equal(result.status, expected, `${method} ${route}: ${result.body.slice(0, 250)}`);
  assert.equal(result.headers.location, undefined, `${method} ${route} redirected`);
  assert.equal(result.headers.allow ?? '', allow, `${method} ${route} Allow`);
  assert.equal(result.headers['cache-control'], 'no-store', `${method} ${route} Cache-Control`);
  if (code && method !== 'HEAD') assert.equal(JSON.parse(result.body).error.code, code, `${method} ${route} error code`);
}

const boundaryPaths = [
  '/self/api/v1/billing/subscriptions/absent/renewal-links',
  '/self/api/v1/billing/subscriptions/absent/../absent/renewal-links',
  '/self//api/v1/billing/subscriptions/absent/renewal-links',
  '/self/api/./v1/billing/subscriptions/absent/renewal-links',
];
const boundaryMethods = ['GET', 'HEAD', 'POST', 'DELETE'];

async function makeDue(subscriptionID) {
  const helper = spawn(go, ['run', './scripts/acceptance-set-renewal-links-due', '--db', path.join(scratch, 'cpa-cloud.db'), '--subscription', subscriptionID], {
    cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  for (const stream of [helper.stdout, helper.stderr]) stream.on('data', chunk => { output += chunk; });
  assert.equal((await once(helper, 'exit'))[0], 0, `isolated due fixture failed: ${output.slice(-500)}`);
}

try {
  await mkdir(scratch, { recursive: true });
  const rejectedDir = path.join(scratch, 'rejected-init');
  const rejected = spawn(executable, ['--data-dir', rejectedDir, '--init', '--employee-self-subscription-renewal-links-enabled'], {
    cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'],
  });
  rejected.stdin.end(adminPassword + '\n');
  assert.equal((await once(rejected, 'exit'))[0], 1, 'missing prerequisites must reject --init');
  await assert.rejects(stat(rejectedDir), { code: 'ENOENT' }, 'rejected --init must not create a data directory');
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  for (const route of boundaryPaths) for (const method of boundaryMethods) {
    await rawBoundaryCall(route, { method, expected: 404 });
  }
  await selfCall('/self/api/v1/billing/subscriptions/absent/renewal-links', { expected: 404 });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  let admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic renewal links employee' }, 201);
  const other = await admin.request('/employees', 'POST', { name: 'Other synthetic renewal links employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const signedIn = await selfCall('/self/api/v1/enroll', { method: 'POST', body: {
    employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword,
  } });
  const cookie = signedIn.cookie;
  assert.ok(cookie);
  for (const [index, route] of boundaryPaths.entries()) for (const method of boundaryMethods) {
    const canonical = index === 0;
    await rawBoundaryCall(route, { method, cookie,
      expected: canonical ? (method === 'GET' ? 404 : 405) : 400,
      code: canonical ? (method === 'GET' ? 'not_found' : 'method_not_allowed') : 'invalid_request',
      allow: canonical && method !== 'GET' ? 'GET' : '',
    });
  }
  const session = (await selfCall('/self/api/v1/session', { cookie })).value;
  assert.equal(session.features.employee_self_subscription_renewal_links, true);
  assert.equal(session.features.employee_self_wallet_balance, false);
  const owner = { kind: 'employee', employee_id: employee.id };
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
  await admin.request('/billing/adjustments', 'POST', { operation_id: randomUUID(),
    owner: { kind: 'employee', employee_id: other.id }, currency: 'USD', amount_micro: '100' });
  const plan = (await admin.request('/billing/plans', 'POST', {
    operation_id: randomUUID(), name: 'Synthetic renewal links monthly', currency: 'USD', price_micro: '10',
    credit_micro: '20', interval: 'monthly', enabled: true,
  })).plan;
  const oneTimePlan = (await admin.request('/billing/plans', 'POST', {
    operation_id: randomUUID(), name: 'Synthetic renewal links once', currency: 'USD', price_micro: '5',
    credit_micro: '7', interval: 'one_time', enabled: true,
  })).plan;
  const rootSubscription = (await admin.request('/billing/subscriptions', 'POST', {
    operation_id: randomUUID(), owner, plan_id: plan.id,
  })).subscription;
  const oneTime = (await admin.request('/billing/subscriptions', 'POST', {
    operation_id: randomUUID(), owner, plan_id: oneTimePlan.id,
  })).subscription;
  const foreign = (await admin.request('/billing/subscriptions', 'POST', {
    operation_id: randomUUID(), owner: { kind: 'employee', employee_id: other.id }, plan_id: plan.id,
  })).subscription;
  const route = id => `/self/api/v1/billing/subscriptions/${encodeURIComponent(id)}/renewal-links`;
  assert.deepEqual((await selfCall(route(rootSubscription.id), { cookie })).value, {
    subscription_id: rootSubscription.id, predecessor_id: null, successor_id: null,
  });
  assert.deepEqual((await selfCall(route(oneTime.id), { cookie })).value, {
    subscription_id: oneTime.id, predecessor_id: null, successor_id: null,
  });
  await selfCall(route(foreign.id), { cookie, expected: 404 });
  await selfCall(route(rootSubscription.id), { expected: 401 });
  await selfCall(route(rootSubscription.id), { cookie, origin: 'https://other.example', expected: 403 });
  await selfCall(route(rootSubscription.id) + '?x=1', { cookie, expected: 400 });
  await selfCall(route(rootSubscription.id), { cookie, method: 'POST', expected: 405 });
  await stopServer(processHandle);

  await makeDue(rootSubscription.id);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  admin = adminClient(processHandle.origin);
  await admin.login();
  const renewed = (await admin.request(`/billing/subscriptions/${rootSubscription.id}/renew`, 'POST', {
    operation_id: randomUUID(),
  })).subscription;
  const balanceBefore = (await admin.request(`/billing/balances?owner_kind=employee&employee_id=${employee.id}&currency=USD`)).balance_micro;
  await admin.request('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  await stopServer(processHandle);

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), flags);
  admin = adminClient(processHandle.origin);
  await admin.login();
  assert.deepEqual((await selfCall(route(rootSubscription.id), { cookie })).value, {
    subscription_id: rootSubscription.id, predecessor_id: null, successor_id: renewed.id,
  });
  assert.deepEqual((await selfCall(route(renewed.id), { cookie })).value, {
    subscription_id: renewed.id, predecessor_id: rootSubscription.id, successor_id: null,
  });
  assert.equal((await admin.request(`/billing/balances?owner_kind=employee&employee_id=${employee.id}&currency=USD`)).balance_micro,
    balanceBefore, 'read must not move money');
  assert.ok(!processHandle.output().includes(adminPassword) && !processHandle.output().includes(selfPassword), 'secret appeared in service logs');

  // The monthly write guard must not swallow the sibling renewal-links read.
  await stopServer(processHandle);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), bothFlags);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).value.features.employee_self_subscription_renewal, true);
  assert.deepEqual((await selfCall(route(rootSubscription.id), { cookie })).value, {
    subscription_id: rootSubscription.id, predecessor_id: null, successor_id: renewed.id,
  });
  assert.deepEqual((await selfCall(route(renewed.id), { cookie })).value, {
    subscription_id: renewed.id, predecessor_id: rootSubscription.id, successor_id: null,
  });
  const wrongMethod = await selfCall(route(rootSubscription.id), { cookie, method: 'POST', expected: 405 });
  assert.equal(wrongMethod.allow, 'GET');
  const monthlyMethod = await selfCall(`/self/api/v1/billing/subscriptions/${rootSubscription.id}/renew`, { cookie, expected: 405 });
  assert.equal(monthlyMethod.allow, 'POST');
  console.log('employee self renewal links isolated process acceptance passed');
  if (holdBrowser) {
    console.log(`browser origin=${processHandle.origin} employee=${employee.id} root=${rootSubscription.id} successor=${renewed.id}`);
    process.stdin.resume();
    await new Promise(resolve => process.stdin.once('end', resolve));
  }
} finally {
  await stopServer(processHandle);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-self-renewal-links-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
