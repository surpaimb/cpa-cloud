// Independent synthetic process acceptance for docs/subscription-manual-renewal-contract.md.
// Usage: node scripts/smoke-subscription-renewal.mjs <absolute service exe> [go executable]
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-renew-'));
const password = randomBytes(24).toString('hex');
let child, origin, cookie = '', csrf = '';
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
  child = launch(['--listen', new URL(origin).host, '--shutdown-on-stdin-eof']);
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) throw new Error('service exited before ready');
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
  assert.equal((await admin('/billing/settings')).enabled, false);
  const employee = await admin('/employees', 'POST', { name: 'Synthetic renewal employee' }, 201);
  const owner = { kind: 'employee', employee_id: employee.id };
  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true });
  await admin('/billing/adjustments', 'POST', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
  const plan = (await admin('/billing/plans', 'POST', { operation_id: randomUUID(), name: 'Synthetic monthly', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'monthly', enabled: true })).plan;
  const purchased = (await admin('/billing/subscriptions', 'POST', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
  assert.equal(purchased.status, 'active');
  await stop();
  const helper = spawn(go, ['run', './scripts/acceptance-set-subscription-due', '--db', path.join(directory, 'cpa-cloud.db'), '--subscription', purchased.id], { cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: 'ignore' });
  assert.equal((await once(helper, 'exit'))[0], 0, 'synthetic due fixture failed');
  await start(); await login();
  const old = await admin('/billing/subscriptions/' + purchased.id);
  assert.equal(old.status, 'expired'); assert.equal(old.successor_id, null);
  const before = await admin('/billing/balances?owner_kind=employee&employee_id=' + employee.id + '&currency=USD');
  assert.equal(before.balance_micro, '110', 'expiry must not move money');
  const operation_id = randomUUID();
  const result = await admin('/billing/subscriptions/' + purchased.id + '/renew', 'POST', { operation_id });
  assert.equal(result.receipt.replay, false);
  assert.equal(result.subscription.predecessor_id, purchased.id);
  assert.equal(result.subscription.status, 'active');
  assert.ok(Date.parse(result.subscription.started_at) > Date.parse(old.period_end_at), 'elapsed gap must not be backfilled');
  assert.equal((await admin('/billing/subscriptions/' + purchased.id)).successor_id, result.subscription.id);
  const after = await admin('/billing/balances?owner_kind=employee&employee_id=' + employee.id + '&currency=USD');
  assert.equal(after.balance_micro, '120');
  const entries = await admin('/billing/entries?resource_kind=subscription&resource_id=' + result.subscription.id);
  assert.equal(entries.items.length, 2);
  await admin('/billing/settings', 'PUT', { operation_id: randomUUID(), expected_revision: 2, enabled: false });
  const replay = await admin('/billing/subscriptions/' + purchased.id + '/renew', 'POST', { operation_id });
  assert.equal(replay.receipt.replay, true); assert.equal(replay.subscription.id, result.subscription.id);
  await admin('/billing/subscriptions/' + purchased.id + '/renew', 'POST', { operation_id: randomUUID() }, 409);
  console.log('manual subscription renewal process acceptance passed');
} finally {
  await stop();
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-renew-')) await rm(resolved, { recursive: true, force: true });
}
