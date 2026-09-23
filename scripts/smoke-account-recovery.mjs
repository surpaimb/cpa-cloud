// Independent process acceptance. Synthetic loopback providers and disposable
// data only; no real credential, provider endpoint, or user service is read.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, webDirectory] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable));
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-auto-recovery-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = 'synthetic-' + randomUUID();
const sensitive = [password, upstreamSecret];
let child, origin, cookie = '', csrf = '', logs = '', calls = 0;
let behavior = 'ok', blocked = false;
const mockErrors = [];
const mock = http.createServer(async (req, res) => {
  try {
    calls++;
    assert.equal(req.url, '/v1/chat/completions');
    assert.equal(req.headers.authorization, 'Bearer ' + upstreamSecret);
    for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
    let text = ''; for await (const chunk of req) text += chunk;
    assert.equal(JSON.parse(text).model, 'actual');
    if (behavior === 'fail') { behavior = 'ok'; res.writeHead(502); res.end('{"error":"synthetic failure"}'); return; }
    if (behavior === 'block') { blocked = true; res.on('close', () => { blocked = false; }); return; }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{"id":"synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}');
  } catch (error) { mockErrors.push(error.message); res.writeHead(500); res.end(); }
});
function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const output of [proc.stdout, proc.stderr]) output.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const done = once(child, 'exit'); child.stdin.end();
    const timer = setTimeout(() => child.kill('SIGKILL'), 8000);
    try { assert.equal((await done)[0], 0, 'Service did not shut down cleanly'); } finally { clearTimeout(timer); }
  }
}
async function start(allow) {
  const port = http.createServer(); port.listen(0, '127.0.0.1'); await once(port, 'listening');
  origin = `http://127.0.0.1:${port.address().port}`; await new Promise(resolve => port.close(resolve));
  child = launch(['--listen', origin.slice(7), '--allow-loopback-upstream', '--shutdown-on-stdin-eof', ...(allow ? ['--allow-account-recovery'] : []), ...(webDirectory ? ['--web-dir', webDirectory] : [])]);
  cookie = ''; csrf = '';
  await until(async () => {
    if (child.exitCode !== null) throw Error('Synthetic service exited at startup');
    try { return (await fetch(origin + '/healthz')).ok; } catch { return false; }
  }, 'startup', 10000);
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
}
async function admin(route, method = 'GET', body, expected = 200) {
  const headers = { Origin: origin, 'Content-Type': 'application/json' };
  if (cookie) headers.Cookie = cookie;
  if (csrf) headers['X-CSRF-Token'] = csrf;
  const response = await fetch(origin + '/admin/api/v1' + route, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(25000) });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  const result = await response.json();
  assert.equal(response.status, expected, route + ' status=' + response.status + ' code=' + result.error?.code);
  return result;
}
async function until(condition, label, ms = 25000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { if (await condition()) return; await delay(100); }
  throw Error('Timed out: ' + label);
}
async function employee(key, expected) {
  const response = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'recovery-test', messages: [{ role: 'user', content: 'Synthetic employee input' }] }), signal: AbortSignal.timeout(12000) });
  assert.equal(response.status, expected); await response.text();
}
try {
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  child = launch(['--init']); const done = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await done)[0], 0);
  await start(false);
  let status = await admin('/account-recovery');
  assert.equal(status.cli_allowed, false); assert.equal(status.enabled, false); assert.equal(status.running, false);
  await admin('/account-recovery', 'PUT', { enabled: true, expected_revision: status.setting_revision }, 403);
  for (const revision of [undefined, 0, -1]) await admin('/account-recovery', 'PUT', { enabled: true, expected_revision: revision }, 400);
  await stop(); await start(true);
  status = await admin('/account-recovery');
  assert.equal(status.enabled, false); assert.equal(calls, 0, 'CLI allowance alone invoked a provider');
  const account = await admin('/upstreams', 'POST', { name: 'Synthetic recovery', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}/v1`, api_key: upstreamSecret }, 201);
  await admin('/models', 'POST', { id: 'recovery-test', upstream_id: account.id, upstream_model: 'actual' }, 201);
  await admin('/models/recovery-test/accounts', 'PUT', { expected_revision: 0, items: [{ upstream_id: account.id, upstream_model: 'actual', priority: 0, weight: 1, max_concurrency: 1 }] });
  const person = await admin('/employees', 'POST', { name: 'Synthetic employee' }, 201);
  const key = (await admin(`/employees/${person.id}/keys`, 'POST', { name: 'Synthetic key', operation_id: randomUUID() }, 201)).key;
  sensitive.push(key);
  const anonymous = await fetch(origin + '/admin/api/v1/account-recovery', { headers: { Authorization: 'Bearer ' + key } }); assert.equal(anonymous.status, 401);
  const noCSRF = await fetch(origin + '/admin/api/v1/account-recovery', { method: 'PUT', headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled: true, expected_revision: status.setting_revision }) }); assert.equal(noCSRF.status, 403);
  const enabled = await admin('/account-recovery', 'PUT', { enabled: true, expected_revision: status.setting_revision });
  await admin('/account-recovery', 'PUT', { enabled: false, expected_revision: status.setting_revision }, 409);
  assert.equal(enabled.enabled, true);
  behavior = 'fail'; await employee(key, 502);
  let records = (await admin('/account-recovery/accounts')).items;
  assert.equal(records.length, 1); assert.equal(records[0].public_model, 'recovery-test'); assert.equal(records[0].upstream_model, 'actual');
  const operation = records[0].operation_id;
  const beforeIsolation = calls; await employee(key, 503); assert.equal(calls, beforeIsolation, 'Employee bypassed isolation');
  await until(async () => (await admin('/account-recovery/accounts')).items.length === 0, 'automatic generation recovery');
  assert.equal(calls, 2, 'Recovery dispatched more than one synthetic request');
  let summary = await admin('/system-probes/summary'); assert.equal(summary.currencies[0].succeeded, '1');
  await employee(key, 200); assert.equal(calls, 3, 'Employee key stopped working after recovery');
  behavior = 'fail'; await employee(key, 502); behavior = 'block';
  await until(() => blocked, 'in-flight automatic probe');
  status = await admin('/account-recovery');
  const stopped = await admin('/account-recovery', 'PUT', { enabled: false, expected_revision: status.setting_revision });
  assert.equal(stopped.enabled, false); assert.equal(stopped.running, false);
  const afterDisable = calls;
  await until(() => !blocked, 'provider request cancellation', 3000);
  records = (await admin('/account-recovery/accounts')).items;
  assert.equal(records.length, 1); assert.notEqual(records[0].operation_id, operation);
  await stop(); await start(true);
  assert.equal((await admin('/account-recovery')).enabled, false);
  assert.equal((await admin('/account-recovery/accounts')).items.length, 1);
  await employee(key, 503); assert.equal(calls, afterDisable, 'Disabled/restarted worker replayed provider request');
  summary = await admin('/system-probes/summary'); assert.equal(summary.currencies[0].total, '2');
  await stop();
  const db = new DatabaseSync(path.join(directory, 'cpa-cloud.db'), { readOnly: true });
  try {
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM system_probe_attempts').get().n, 2);
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM accounting_requests').get().n, 3);
    assert.equal(db.prepare("SELECT COUNT(*) AS n FROM system_probe_attempts WHERE status='pending'").get().n, 0);
  } finally { db.close(); }
  for (const file of await readdir(directory)) for (const secret of sensitive) assert.ok(!(await readFile(path.join(directory, file))).includes(Buffer.from(secret)), 'Plaintext synthetic secret persisted');
  for (const secret of sensitive) assert.ok(!logs.includes(secret), 'Plaintext synthetic secret logged');
  assert.deepEqual(mockErrors, []);
  console.log('PASS: default-off dual gates, admin/CSRF/revision checks, real failure capture, isolation, one bounded recovery, separate ledger, cancellation, disable/restart without replay, employee key preserved');
} catch (error) {
  let diagnostics = logs.slice(-4000); for (const secret of sensitive) diagnostics = diagnostics.replaceAll(secret, '[redacted]');
  console.error(diagnostics); throw error;
} finally {
  await stop(); mock.closeAllConnections(); await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory), temp = path.resolve(os.tmpdir()) + path.sep;
  assert.ok(resolved.startsWith(temp) && path.basename(resolved).startsWith('cpac-auto-recovery-'));
  await rm(resolved, { recursive: true, force: true });
}
