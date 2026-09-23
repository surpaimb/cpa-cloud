// Independent process acceptance for proxy storage, upgrade and restart.
// Synthetic credentials and disposable directories only; no model dispatch.
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-proxy-storage-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = randomBytes(24).toString('hex');
const proxyUsername = randomBytes(24).toString('hex');
const proxyPassword = randomBytes(24).toString('hex');
const sensitive = [password, upstreamSecret, proxyUsername, proxyPassword];
let activeExecutable = previousExecutable ?? executable;
let child, origin, cookie = '', csrf = '', logs = '', proxyConnections = 0;
const trap = http.createServer((_, res) => { res.end(); });
trap.on('connection', socket => { proxyConnections++; socket.destroy(); });
function launch(args) {
  const proc = spawn(activeExecutable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', bytes => { logs += bytes; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const done = once(child, 'exit'); child.stdin.end();
    const timer = setTimeout(() => child.kill('SIGKILL'), 10000);
    try { assert.equal((await done)[0], 0, 'Service shutdown failed'); } finally { clearTimeout(timer); }
  }
}
async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method,
    headers: { Origin: origin, 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...(csrf ? { 'X-CSRF-Token': csrf } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000) });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  const value = await response.json();
  assert.equal(response.status, expected, route + ' response status');
  return value;
}
async function start() {
  const reservation = http.createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening');
  origin = `http://127.0.0.1:${reservation.address().port}`; await new Promise(resolve => reservation.close(resolve));
  child = launch(['--listen', new URL(origin).host, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) throw Error('Service startup failed');
    try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch {}
    if (ready) break; await delay(100);
  }
  assert.ok(ready, 'Service startup timed out'); cookie = ''; csrf = '';
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
}
try {
  trap.listen(0, '127.0.0.1'); await once(trap, 'listening');
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0);
  await start();
  const employee = await admin('/employees', 'POST', { name: 'Synthetic migration employee' }, 201);
  const key = (await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'Synthetic permanent key', operation_id: randomUUID() }, 201)).key;
  sensitive.push(key);
  const account = await admin('/upstreams', 'POST', { name: 'Synthetic saved account', provider_kind: 'openai-compatible', endpoint: `https://127.0.0.1:${trap.address().port}/v1`, api_key: upstreamSecret }, 201);
  await admin('/models', 'POST', { id: 'proxy-storage-test', upstream_id: account.id, upstream_model: 'actual' }, 201);
  await stop();
  if (previousExecutable) {
    const old = new DatabaseSync(path.join(directory, 'cpa-cloud.db'), { readOnly: true });
    try { assert.equal(old.prepare("SELECT COUNT(*) AS n FROM sqlite_master WHERE name='outbound_proxies'").get().n, 0, 'Old binary already contains proxy tables'); } finally { old.close(); }
  }
  activeExecutable = executable; await start();
  const models = await fetch(origin + '/v1/models', { headers: { Authorization: 'Bearer ' + key } });
  assert.equal(models.status, 200); assert.ok((await models.json()).data.some(model => model.id === 'proxy-storage-test'));
  const write = { operation_id: randomUUID(), name: 'Synthetic proxy', scheme: 'https', host: '127.0.0.1', port: trap.address().port,
    address_scope: 'private', enabled: true, credentials: { username: proxyUsername, password: proxyPassword } };
  const created = await admin('/outbound-proxies', 'POST', write);
  assert.equal((await admin('/outbound-proxies', 'POST', write)).id, created.id);
  assert.equal(created.has_credentials, true);
  for (const secret of sensitive) assert.ok(!JSON.stringify(created).includes(secret), 'Management secret readback');
  await admin('/outbound-proxies', 'POST', { ...write, name: 'Conflicting payload' }, 409);
  let binding = await admin(`/upstreams/${account.id}/proxy`, 'PUT', { expected_upstream_revision: account.revision, proxy_id: created.id, expected_proxy_revision: created.revision, bind: true });
  const denied = await fetch(origin + '/admin/api/v1/outbound-proxies', { headers: { Authorization: 'Bearer ' + key } }); assert.equal(denied.status, 401);
  const csrfDenied = await fetch(origin + '/admin/api/v1/outbound-proxies', { method: 'POST', headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json' }, body: JSON.stringify(write) }); assert.equal(csrfDenied.status, 403);
  await stop();
  const pendingID = randomUUID();
  const database = new DatabaseSync(path.join(directory, 'cpa-cloud.db'));
  try {
    database.exec('PRAGMA foreign_keys=ON');
    database.prepare("INSERT INTO outbound_proxy_test_operations(operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)")
      .run(pendingID, created.id, created.revision, created.connection_revision, account.id, binding.upstream_revision, new Date().toISOString().replace('Z', '000000Z'));
  } finally { database.close(); }
  await start();
  binding = await admin(`/upstreams/${account.id}/proxy`); assert.equal(binding.binding.proxy_id, created.id);
  const interrupted = await admin(`/outbound-proxies/${created.id}/tests/${pendingID}`);
  assert.equal(interrupted.state, 'completed'); assert.equal(interrupted.result_code, 'interrupted');
  const replay = await admin(`/outbound-proxies/${created.id}/tests`, 'POST', { operation_id: pendingID, expected_proxy_revision: created.revision,
    expected_connection_revision: created.connection_revision, upstream_id: account.id, expected_upstream_revision: binding.upstream_revision });
  assert.equal(replay.result_code, 'interrupted');
  const disabled = await admin(`/outbound-proxies/${created.id}`, 'PATCH', { expected_revision: created.revision, name: created.name, scheme: 'https', host: created.host, port: created.port, address_scope: created.address_scope, enabled: false, credential_mode: 'keep' });
  binding = await admin(`/upstreams/${account.id}/proxy`);
  assert.equal(binding.binding.enabled, false); assert.equal(binding.upstream_revision, account.revision + 2);
  await admin(`/upstreams/${account.id}/proxy`, 'PUT', { expected_upstream_revision: account.revision, proxy_id: created.id, expected_proxy_revision: disabled.revision, bind: false }, 409);
  const direct = await admin(`/upstreams/${account.id}/proxy`, 'PUT', { expected_upstream_revision: binding.upstream_revision, proxy_id: created.id, expected_proxy_revision: disabled.revision, bind: false });
  assert.equal(direct.binding, null);
  await stop();
  assert.equal(proxyConnections, 0, 'Upgrade/restart/idempotent recovery reconnected');
  for (const name of await readdir(directory)) {
    const bytes = await readFile(path.join(directory, name));
    for (const secret of sensitive) assert.ok(!bytes.includes(Buffer.from(secret)), 'Plaintext secret persisted');
  }
  for (const secret of sensitive) assert.ok(!logs.includes(secret), 'Secret logged');
  console.log('PASS proxy storage: upgrade/employee key/encryption/CSRF/idempotency/binding/restart zero replay/explicit unbind');
} finally {
  await stop();
  await new Promise(resolve => trap.close(resolve));
  const resolved = path.resolve(directory);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-proxy-storage-'));
  await rm(resolved, { recursive: true, force: true });
}
