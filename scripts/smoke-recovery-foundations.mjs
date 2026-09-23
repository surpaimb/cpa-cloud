// Independent acceptance: isolated Go processes, synthetic credentials only.
// No generation worker is enabled. SQLite fixtures are written only while the
// test-owned process is stopped; an optional old binary supplies the old DB.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, previousExecutable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable));
assert.ok(!previousExecutable || path.isAbsolute(previousExecutable));
let activeExecutable = previousExecutable ?? executable;
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-recovery-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = 'synthetic-' + randomUUID();
const sensitive = [password, upstreamSecret];
let child, origin, cookie = '', csrf = '', logs = '', calls = 0;
const mockErrors = [];
const mock = http.createServer(async (req, res) => {
  try {
    calls++;
    assert.equal(req.headers.authorization, 'Bearer ' + upstreamSecret);
    for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
    for await (const chunk of req) { /* discard synthetic employee input */ }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{"id":"synthetic","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}');
  } catch (error) { mockErrors.push(error.message); res.writeHead(500); res.end(); }
});
function launch(args) {
  const proc = spawn(activeExecutable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const output of [proc.stdout, proc.stderr]) output.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const done = once(child, 'exit'); child.stdin.end();
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
    try { await done; } finally { clearTimeout(timer); }
  }
}
async function start() {
  const port = http.createServer(); port.listen(0, '127.0.0.1'); await once(port, 'listening');
  origin = `http://127.0.0.1:${port.address().port}`; await new Promise(resolve => port.close(resolve));
  child = launch(['--listen', origin.slice(7), '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let n = 0; n < 100; n++) {
    if (child.exitCode !== null) throw Error('Synthetic service exited during startup');
    try {
      if ((await fetch(origin + '/healthz')).ok) {
        cookie = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token; return;
      }
    } catch { /* bounded startup poll */ }
    await delay(100);
  }
  throw Error('Synthetic service startup timed out');
}
async function admin(route, method = 'GET', body, expected = 200) {
  const headers = { Origin: origin, 'Content-Type': 'application/json' };
  if (cookie) headers.Cookie = cookie;
  if (csrf) headers['X-CSRF-Token'] = csrf;
  const response = await fetch(origin + '/admin/api/v1' + route, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  const result = await response.json();
  assert.equal(response.status, expected, route + ' returned ' + response.status);
  return result;
}
async function employee(key, expected) {
  const response = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: 'recovery-test', messages: [{ role: 'user', content: 'Synthetic acceptance input' }] }), signal: AbortSignal.timeout(12000) });
  assert.equal(response.status, expected); await response.text();
}
async function scan() {
  for (const file of await readdir(directory)) {
    const data = await readFile(path.join(directory, file));
    for (const value of sensitive) assert.ok(!data.includes(Buffer.from(value)), 'Plaintext secret in synthetic persisted data');
  }
  for (const value of sensitive) assert.ok(!logs.includes(value), 'Plaintext secret in synthetic service logs');
}
try {
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  const endpoint = `http://127.0.0.1:${mock.address().port}/v1`;
  child = launch(['--init']); const initDone = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initDone)[0], 0);
  await start();
  const account = await admin('/upstreams', 'POST', { name: 'Synthetic recovery', provider_kind: 'openai-compatible', endpoint, api_key: upstreamSecret }, 201);
  await admin('/models', 'POST', { id: 'recovery-test', upstream_id: account.id, upstream_model: 'actual' }, 201);
  await admin('/models/recovery-test/accounts', 'PUT', { expected_revision: 0, items: [{ upstream_id: account.id, upstream_model: 'actual', priority: 0, weight: 1, max_concurrency: 1 }] });
  const employeeRecord = await admin('/employees', 'POST', { name: 'Synthetic employee' }, 201);
  const key = await admin(`/employees/${employeeRecord.id}/keys`, 'POST', { name: 'Synthetic key', operation_id: randomUUID() }, 201);
  const employeeKey = key.key; sensitive.push(employeeKey);
  await employee(employeeKey, 200); assert.equal(calls, 1);
  await stop();
  if (previousExecutable) {
    const old = new DatabaseSync(path.join(directory, 'cpa-cloud.db'), { readOnly: true });
    try {
      assert.equal(old.prepare("SELECT COUNT(*) AS n FROM sqlite_master WHERE name IN ('system_probe_attempts','account_recovery_states','account_pool_maintenance_leases')").get().n, 0,
        'The supplied old binary already contains recovery tables');
    } finally { old.close(); }
  }
  activeExecutable = executable; await start();
  assert.deepEqual((await admin('/system-probes/summary')).currencies, []);
  await stop();
  const operation = randomUUID(), event = 'cool_synthetic_recovery', lease = 'lease_synthetic_recovery';
  const now = Date.now();
  // Match service's fixed 9-digit fractional timestamp representation.
  const stamp = value => new Date(value).toISOString().replace(/(\.\d{3})Z$/, '$1000000Z');
  const ledgerStamp = value => new Date(value).toISOString().replace(/\.000Z$/, 'Z').replace(/(\.\d*?[1-9])0+Z$/, '$1Z');
  const db = new DatabaseSync(path.join(directory, 'cpa-cloud.db'));
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN');
    db.prepare(`INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,'transient',?,?)`).run(account.id, event, stamp(now - 1000), stamp(now - 2000));
    db.prepare(`INSERT INTO account_recovery_states(account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,next_probe_at,created_at,updated_at)
      VALUES(?,?,?,1,1,1,'openai-compatible','api_key',NULL,'recovery-test','actual','openai-chat-completions','in_progress',?,?,?)`).run(account.id, event, operation, stamp(now - 1000), stamp(now - 2000), stamp(now - 1000));
    db.prepare(`INSERT INTO system_probe_attempts(operation_id,recovery_event_id,account_id,account_revision,pool_revision,public_model,upstream_model,provider,protocol,started_at,may_have_sent_at,status)
      VALUES(?,?,?,1,1,'recovery-test','actual','openai-compatible','openai-chat-completions',?,?,'pending')`).run(operation, event, account.id, ledgerStamp(now - 2000), ledgerStamp(now - 1000));
    db.prepare(`INSERT INTO account_pool_maintenance_leases(lease_id,operation_id,account_id,cooldown_event_id,recovery_revision,pool_revision,account_revision,public_model,upstream_model,provider_kind,protocol,dispatch_phase,expires_at,created_at)
      VALUES(?,?,?,?,1,1,1,'recovery-test','actual','openai-compatible','openai-chat-completions',2,?,?)`).run(lease, operation, account.id, event, stamp(now + 5000), stamp(now - 2000));
    db.exec('COMMIT');
  } finally { db.close(); }
  await start();
  const summary = await admin('/system-probes/summary');
  assert.equal(summary.scope, 'system_probes'); assert.equal(summary.currencies[0].interrupted, '1');
  assert.equal(summary.currencies[0].unknown_cost_attempts, '1');
  const anonymous = await fetch(origin + '/admin/api/v1/system-probes/summary', { headers: { Authorization: 'Bearer ' + employeeKey } });
  assert.equal(anonymous.status, 401);
  await employee(employeeKey, 503); assert.equal(calls, 1, 'Expired cooldown bypassed persistent recovery isolation');
  await admin(`/upstreams/${account.id}/cooldown/clear`, 'POST', { expected_revision: 1, expected_cooldown_event_id: event });
  await employee(employeeKey, 200); assert.equal(calls, 2, 'Unexpected system generation request');
  await stop(); await start();
  assert.equal((await admin('/system-probes/summary')).currencies[0].total, '1');
  assert.equal(calls, 2, 'Restart replayed an uncertain probe');
  await stop();
  const verify = new DatabaseSync(path.join(directory, 'cpa-cloud.db'), { readOnly: true });
  try {
    assert.equal(verify.prepare('SELECT COUNT(*) AS n FROM model_requests').get().n, 2);
    assert.equal(verify.prepare('SELECT COUNT(*) AS n FROM accounting_requests').get().n, 2);
    assert.equal(verify.prepare('SELECT COUNT(*) AS n FROM system_probe_attempts').get().n, 1);
  } finally { verify.close(); }
  await scan(); assert.deepEqual(mockErrors, []);
  console.log('PASS: isolated migration, independent probe accounting, interrupted recovery without replay, persistent isolation, manual clear, shared lease expiry, employee key preservation');
} catch (error) {
  let diagnostics = logs.slice(-4000);
  for (const value of sensitive) diagnostics = diagnostics.replaceAll(value, '[redacted]');
  console.error(diagnostics);
  throw error;
} finally {
  await stop(); await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory), temp = path.resolve(os.tmpdir()) + path.sep;
  assert.ok(resolved.startsWith(temp) && path.basename(resolved).startsWith('cpac-recovery-'));
  await rm(resolved, { recursive: true, force: true });
}
