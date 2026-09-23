// Independently authored acceptance: real disposable Go processes and synthetic traffic.
// This never reads user credentials or calls a real model provider.
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-governance-observations-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = randomBytes(24).toString('hex');
const prompt = 'synthetic-observation-prompt-' + randomUUID();
const answer = 'synthetic-observation-answer-' + randomUUID();
const sensitive = [password, upstreamSecret, prompt, answer];
let activeExecutable = previousExecutable ?? executable;
let child, origin, cookie = '', csrf = '', logs = '', calls = 0;
let block = false, blocked = false, partial = false, release;
const mockErrors = [];
const mock = http.createServer(async (req, res) => {
  try {
    calls++;
    assert.equal(req.headers.authorization, 'Bearer ' + upstreamSecret);
    for await (const chunk of req) { /* drain without retaining model contents */ }
    if (block) {
      blocked = true;
      await new Promise(resolve => { release = resolve; res.once('close', resolve); });
      blocked = false;
    }
    if (res.destroyed) return;
    const usage = partial ? { prompt_tokens: 100, completion_tokens: 50 } : {
      prompt_tokens: 100, completion_tokens: 50, total_tokens: 150,
      prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 10 },
    };
    partial = false;
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ id: 'synthetic-observation', choices: [{ message: { role: 'assistant', content: answer }, finish_reason: 'stop' }], usage }));
  } catch (error) { mockErrors.push(error.message); if (!res.headersSent) res.writeHead(500); res.end(); }
});
function launch(args) {
  const proc = spawn(activeExecutable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', bytes => { logs += bytes; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const done = once(child, 'exit'); child.stdin.end(); const timer = setTimeout(() => child.kill('SIGKILL'), 10000);
    try { assert.equal((await done)[0], 0, 'Graceful shutdown failed'); } finally { clearTimeout(timer); }
  }
}
async function until(condition, label) {
  const end = Date.now() + 10000;
  while (Date.now() < end) { if (await condition()) return; await delay(25); }
  throw Error('Timed out: ' + label);
}
async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Origin: origin, 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...(csrf ? { 'X-CSRF-Token': csrf } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000),
  });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  const result = await response.json(); assert.equal(response.status, expected, route + ' status'); return result;
}
async function start() {
  const reservation = http.createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening');
  origin = `http://127.0.0.1:${reservation.address().port}`; await new Promise(resolve => reservation.close(resolve));
  child = launch(['--listen', new URL(origin).host, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  await until(async () => {
    if (child.exitCode !== null) throw Error('Service startup failed');
    try { return (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { return false; }
  }, 'startup');
  cookie = ''; csrf = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
}
async function generate(key, expected = 200) {
  const response = await fetch(origin + '/v1/chat/completions', {
    method: 'POST', headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: 'observation-test', messages: [{ role: 'user', content: prompt }] }), signal: AbortSignal.timeout(15000),
  });
  assert.equal(response.status, expected); const result = await response.json();
  if (expected === 200) assert.equal(result.choices[0].message.content, answer);
}
const rates = (currency, multiplier = 1) => ({ currency, input_per_million_micro: String(1000000 * multiplier), output_per_million_micro: String(2000000 * multiplier), cache_read_per_million_micro: String(100000 * multiplier), cache_write_per_million_micro: String(1500000 * multiplier) });
const shadow = (tpm, cost) => ({ tpm, cost_micro: String(cost), currency: 'USD', window: 'rolling_24h' });
const noHardLimits = { rpm: null, concurrency: null };
const endpoint = '/governance/observations';
const query = values => new URLSearchParams(values).toString();
function noSecrets(value) { const bytes = Buffer.from(value); for (const secret of sensitive) assert.ok(!bytes.includes(Buffer.from(secret)), 'Sensitive data leaked'); }
function validatePage(page) {
  for (const field of ['window_end', 'observed_at', 'tpm_from', 'cost_from']) assert.match(page[field], /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$/);
  assert.ok(Array.isArray(page.items)); noSecrets(JSON.stringify(page));
  if (page.next_cursor !== null) assert.ok(typeof page.next_cursor === 'string' && Buffer.byteLength(page.next_cursor) <= 2048);
}
try {
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  await start();
  const account = await admin('/upstreams', 'POST', { name: 'Synthetic observation account', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}/v1`, api_key: upstreamSecret }, 201);
  await admin('/models', 'POST', { id: 'observation-test', upstream_id: account.id, upstream_model: 'actual-observation' }, 201);
  const person = await admin('/employees', 'POST', { name: 'Synthetic observation employee' }, 201);
  const key = await admin(`/employees/${person.id}/keys`, 'POST', { name: 'Synthetic observation key', operation_id: randomUUID() }, 201); sensitive.push(key.key);
  const employeePolicy = await admin('/governance/policies', 'POST', { operation_id: randomUUID(), scope_kind: 'employee', scope_id: person.id, enabled: true, hard: noHardLimits, shadow: shadow(100, 100) });
  const group = await admin('/governance/groups', 'POST', { operation_id: randomUUID(), name: 'Synthetic observation group', employee_ids: [person.id] });
  await admin('/governance/policies', 'POST', { operation_id: randomUUID(), scope_kind: 'group', scope_id: group.resource_id, enabled: true, hard: noHardLimits, shadow: shadow(1000, 1000) });
  await admin('/governance/policies', 'POST', { operation_id: randomUUID(), scope_kind: 'key', scope_id: key.id, enabled: true, hard: { rpm: 100, concurrency: null }, shadow: { tpm: null, cost_micro: null, currency: null, window: null } });
  const pricePath = `/upstreams/${account.id}/prices`;
  const setPrice = (revision, currency, multiplier = 1) => admin(pricePath, 'POST', { operation_id: randomUUID(), expected_revision: revision, upstream_model: 'actual-observation', price: rates(currency, multiplier) });
  await setPrice(0, 'USD');
  await generate(key.key); // default-off history must never be retroactively attributed
  if (!previousExecutable) { const empty = await admin(endpoint); validatePage(empty); assert.deepEqual(empty.items, []); }
  const settings = await admin('/governance/settings');
  assert.equal(settings.enabled, false);
  await admin('/governance/settings', 'PUT', { operation_id: randomUUID(), expected_revision: settings.revision, enabled: true });
  await generate(key.key); // persisted governance/ledger facts must survive the index-only upgrade
  await stop(); activeExecutable = executable; await start();
  assert.equal((await admin('/governance/settings')).enabled, true);
  let page = await admin(endpoint); validatePage(page); assert.equal(page.items.length, 3);
  for (const row of page.items) assert.equal(row.scope_totals.tpm.known_tokens, '150');
  block = true; const pending = generate(key.key); await until(() => blocked, 'pending generation');
  page = await admin(endpoint); assert.equal(page.items.length, 3);
  for (const row of page.items) {
    assert.equal(row.scope_totals.tpm.pending_requests, '1'); assert.equal(row.scope_totals.cost.pending_requests, '1');
    assert.equal(row.scope_totals.tpm.pending_attempts, '1'); assert.equal(row.scope_totals.tpm.known_tokens, '150');
    assert.equal(row.interpretation.tpm_state, row.snapshot.shadow_tpm === null ? null : row.snapshot.scope_kind === 'employee' ? 'exceeded' : 'unknown');
  }
  await admin('/governance/policies/' + employeePolicy.resource_id, 'PUT', { operation_id: randomUUID(), expected_revision: 1, enabled: true, hard: noHardLimits, shadow: shadow(1000, 1000) });
  await admin('/governance/groups/' + group.resource_id, 'PUT', { operation_id: randomUUID(), expected_revision: 1, name: 'Synthetic revised group', employee_ids: [person.id] });
  await setPrice(1, 'USD', 2); block = false; release(); await pending;
  await generate(key.key); partial = true; await generate(key.key);
  await setPrice(2, 'EUR'); await generate(key.key);
  page = await admin(endpoint); validatePage(page); assert.equal(page.items.length, 5);
  for (const row of page.items) {
    const { tpm, cost } = row.scope_totals;
    assert.equal(tpm.known_tokens, '600'); assert.equal(tpm.known_attempts, '4'); assert.equal(tpm.unknown_token_attempts, '1');
    assert.equal(tpm.pending_requests, '0'); assert.equal(cost.pending_requests, '0');
    assert.equal(tpm.pending_attempts, '0'); assert.equal(tpm.pending_requests_without_attempt, '0');
    assert.equal(cost.known_attempts, '4'); assert.equal(cost.unknown_cost_attempts, '1');
    assert.deepEqual(cost.by_currency, [{ currency: 'EUR', known_cost_micro: '187', attempts: '1' }, { currency: 'USD', known_cost_micro: '748', attempts: '3' }]);
    const oldEmployee = row.snapshot.scope_kind === 'employee' && row.snapshot.policy_revision === '1';
    const noThreshold = row.snapshot.scope_kind === 'key';
    assert.equal(row.interpretation.tpm_state, noThreshold ? null : oldEmployee ? 'exceeded' : 'unknown');
    assert.equal(row.interpretation.cost_state, noThreshold ? null : oldEmployee ? 'exceeded' : 'unknown');
  }
  const filtered = await admin(endpoint + '?' + query({ policy_id: employeePolicy.resource_id }));
  assert.equal(filtered.items.length, 2); for (const row of filtered.items) assert.equal(row.scope_totals.tpm.known_tokens, '600');
  const firstPage = await admin(endpoint + '?limit=1'); const cursor = firstPage.next_cursor; assert.ok(cursor);
  const ids = firstPage.items.map(item => JSON.stringify(item.snapshot)); let next = cursor;
  while (next) {
    const current = await admin(endpoint + '?' + query({ limit: '1', cursor: next })); validatePage(current);
    assert.equal(current.window_end, firstPage.window_end); ids.push(...current.items.map(item => JSON.stringify(item.snapshot))); next = current.next_cursor;
    assert.ok(ids.length <= 5, 'Pagination repeated rows');
  }
  assert.equal(ids.length, 5); assert.equal(new Set(ids).size, 5);
  const tampered = (cursor[0] === 'A' ? 'B' : 'A') + cursor.slice(1);
  for (const bad of ['limit=0', 'limit=101', 'limit=1&limit=2', 'as_of=2020-01-01', 'scope_id=orphan', 'limit=%GG', query({ cursor: tampered }), query({ cursor, scope_kind: 'employee' })]) await admin(endpoint + '?' + bad, 'GET', undefined, 400);
  for (const headers of [{}, { Authorization: 'Bearer ' + key.key }]) {
    const denied = await fetch(origin + '/admin/api/v1' + endpoint, { headers }); assert.equal(denied.status, 401); await denied.text();
  }
  const currentSettings = await admin('/governance/settings');
  await admin('/governance/settings', 'PUT', { operation_id: randomUUID(), expected_revision: currentSettings.revision, enabled: false });
  await generate(key.key); // off-period request must not enlarge historical totals
  await stop(); await start();
  const resumed = await admin(endpoint + '?' + query({ limit: '1', cursor })); assert.equal(resumed.window_end, firstPage.window_end); assert.equal(resumed.items.length, 1);
  const history = await admin(endpoint); assert.equal(history.items.length, 5); for (const row of history.items) assert.equal(row.scope_totals.cost.known_attempts, '4');
  const beforeRevoked = calls; await admin(`/keys/${key.id}/revoke`, 'POST', {}); await generate(key.key, 401); assert.equal(calls, beforeRevoked);
  await stop();
  const db = new DatabaseSync(path.join(directory, 'cpa-cloud.db'), { readOnly: true });
  try {
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM governance_requests').get().n, 5);
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM governance_requests WHERE released_at IS NULL').get().n, 0);
  } finally { db.close(); }
  noSecrets(logs); for (const name of await readdir(directory)) noSecrets(await readFile(path.join(directory, name)));
  assert.deepEqual(mockErrors, []);
  console.log('PASS governance observations: upgrade/default-off/pending/revision-wide totals/unknown/multiple currencies/immutable price/pagination/HMAC/restart/admin/revocation/no sensitive persistence');
} finally {
  block = false; release?.(); await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir())); assert.ok(path.basename(resolved).startsWith('cpac-governance-observations-'));
  await rm(resolved, { recursive: true, force: true });
}
