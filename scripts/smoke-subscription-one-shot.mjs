// Independent synthetic process acceptance for docs/subscription-one-shot-renewal-contract.md.
// Usage: node scripts/smoke-subscription-one-shot.mjs <absolute service exe> [go executable]
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const executable = process.argv[2];
const go = process.argv[3] ?? 'go';
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-one-shot-'));
const password = randomBytes(24).toString('hex');
let child, origin, cookie = '', csrf = '', serverStderr = '';
function launch(args) { return spawn(executable, ['--data-dir', directory, ...args], { cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: ['pipe', 'ignore', 'pipe'] }); }
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exit = once(child, 'exit'); child.stdin.end();
    const deadline = setTimeout(() => child.kill(), 5000);
    try { await exit; } finally { clearTimeout(deadline); }
  }
}
async function start() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  origin = `http://127.0.0.1:${probe.address().port}`;
  await new Promise(resolve => probe.close(resolve));
  assert.notEqual(new URL(origin).port, '8787');
  serverStderr = '';
  child = launch(['--listen', new URL(origin).host, '--shutdown-on-stdin-eof']);
  child.stderr.on('data', chunk => { serverStderr = (serverStderr + chunk.toString()).slice(-4000); });
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) throw new Error(`service exited before ready: ${serverStderr}`);
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* startup */ }
    await delay(100);
  }
  throw new Error('service readiness timeout');
}
async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${route}: ${raw}`);
  return JSON.parse(raw);
}
async function login() { cookie = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token; assert.ok(csrf); }
try {
  const init = launch(['--init']); const initialized = once(init, 'exit'); init.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0, 'initialization failed');
  await start(); await login();
  const employee = await admin('/employees', 'POST', { name: 'Synthetic one-shot employee' }, 201);
  const owner = { kind: 'employee', employee_id: employee.id };
  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  await admin('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
  const plan = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Synthetic monthly', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'monthly', enabled: true })).plan;
  const purchased = (await admin('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  const route = '/billing/subscriptions/' + purchased.id + '/one-shot-renewal';
  assert.equal((await admin(route)).state, 'none');
  const operation_id = randomUUID();
  const armed = await admin(route, 'POST', { operation_id, expected_revision: purchased.revision });
  assert.equal(armed.one_shot_renewal.state, 'armed');
  assert.equal((await admin(route, 'POST', { operation_id, expected_revision: purchased.revision })).receipt.replay, true);
  await stop();
  const helper = spawn(go, ['run', './scripts/acceptance-set-one-shot-due', '--db', path.join(directory, 'cpa-cloud.db'), '--subscription', purchased.id], { cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: 'ignore' });
  assert.equal((await once(helper, 'exit'))[0], 0, 'synthetic due fixture failed');
  await start(); await login();
  let result;
  for (let attempt = 0; attempt < 60; attempt++) {
    result = await admin(route);
    if (result.state === 'succeeded') break;
    await delay(200);
  }
  assert.equal(result.state, 'succeeded');
  assert.ok(result.successor_id);
  const successor = await admin('/billing/subscriptions/' + result.successor_id);
  assert.equal(successor.predecessor_id, purchased.id);
  assert.equal(successor.status, 'active');
  assert.equal((await admin('/billing/subscriptions/' + result.successor_id + '/one-shot-renewal')).state, 'none');
  const balance = await admin('/billing/balances?owner_kind=employee&employee_id=' + employee.id + '&currency=USD');
  assert.equal(balance.balance_micro, '120');
  const entries = await admin('/billing/entries?resource_kind=subscription&resource_id=' + successor.id);
  assert.equal(entries.items.length, 2);
  await stop(); await start(); await login();
  assert.equal((await admin(route)).successor_id, successor.id);
  assert.equal((await admin('/billing/balances?owner_kind=employee&employee_id=' + employee.id + '&currency=USD')).balance_micro, '120');
  console.log('one-shot renewal process acceptance passed');
} finally {
  await stop();
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-one-shot-')) await rm(resolved, { recursive: true, force: true });
}
