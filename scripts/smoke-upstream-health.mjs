// Independently authored acceptance. Only isolated processes and synthetic loopback providers.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, previousExecutable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable path required');
assert.ok(!previousExecutable || path.isAbsolute(previousExecutable), 'Absolute baseline executable path required');
let activeExecutable = previousExecutable ?? executable;
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-health-'));
const password = randomBytes(24).toString('hex');
const privateBody = 'synthetic-private-catalog-' + randomUUID();
const sensitive = [password, privateBody];
const credentials = new Map(), calls = [], mockErrors = [], gates = new Map();
let child, origin, cookie = '', csrf = '', logs = '', endpoint;

const mock = http.createServer(async (req, res) => {
  try {
    const key = req.headers['x-goog-api-key'] ?? req.headers['x-api-key'] ?? req.headers.authorization?.replace('Bearer ', '');
    const fixture = credentials.get(key);
    assert.ok(fixture, 'Unknown synthetic provider credential');
    for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
    calls.push({ key, method: req.method, url: req.url });
    const gate = gates.get(key);
    if (gate) {
      gates.delete(key);
      gate.enter();
      await gate.released;
    }
    if (res.destroyed) return;
    const status = req.method === 'GET' ? fixture.catalogStatus ?? 200 : fixture.generationStatus ?? 200;
    res.writeHead(status, { 'Content-Type': 'application/json' });
    if (status !== 200) { res.end(JSON.stringify({ error: privateBody })); return; }
    if (req.method !== 'GET') {
      for await (const ignored of req) { /* never retain generation content */ }
      res.end('{"id":"synthetic","object":"chat.completion","choices":[]}');
    } else if (req.url.startsWith('/v1beta/')) {
      const second = new URL(req.url, endpoint).searchParams.get('pageToken') === 'synthetic-next';
      res.end(JSON.stringify({ models: [{ name: 'models/synthetic-' + (second ? 'two' : 'one'), supportedGenerationMethods: ['generateContent'] }], ...(second ? {} : { nextPageToken: 'synthetic-next' }) }));
    } else if (req.headers['anthropic-version']) {
      const second = new URL(req.url, endpoint).searchParams.has('after_id');
      const id = privateBody + (second ? '-two' : '-one');
      res.end(JSON.stringify({ data: [{ id }], has_more: !second, last_id: id }));
    } else {
      res.end(JSON.stringify({ data: [{ id: privateBody }], has_more: false }));
    }
  } catch (error) {
    mockErrors.push(error.message);
    if (!res.headersSent) res.writeHead(500);
    res.end();
  }
});

function gateNext(key) {
  let enter, release;
  const entered = new Promise(resolve => { enter = resolve; });
  const released = new Promise(resolve => { release = resolve; });
  gates.set(key, { enter, released, release });
  return { entered: Promise.race([entered, delay(12000).then(() => { throw Error('Synthetic provider gate timed out'); })]), release };
}
function launch(args) {
  const process = spawn(activeExecutable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [process.stdout, process.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) process.kill(); });
  return process;
}
async function stop(force = false) {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = once(child, 'exit');
    // Crash recovery needs an uncatchable stop on Unix as well as Windows.
    // SIGTERM is handled gracefully by the server and could finalize the test.
    if (force) child.kill('SIGKILL'); else child.stdin.end();
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
    try { await exited; } finally { clearTimeout(timer); }
  }
}
async function start() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  origin = `http://127.0.0.1:${probe.address().port}`; await new Promise(resolve => probe.close(resolve));
  child = launch(['--listen', new URL(origin).host, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let i = 0; i < 100; i++) {
    assert.equal(child.exitCode, null, 'Isolated server exited before readiness');
    let ready = false;
    try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { /* own process readiness */ }
    if (ready) {
      cookie = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
      return;
    }
    await delay(100);
  }
  throw Error('Isolated server readiness timed out');
}
async function admin(route, method = 'GET', body, expected = 200, extraHeaders = {}) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json', ...extraHeaders },
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body), signal: AbortSignal.timeout(15000),
  });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  assert.equal(response.status, expected, `${method} ${route} returned an unexpected status`);
  const result = await response.json();
  if (route !== '/sessions' && !route.endsWith('/keys')) clean(result);
  return result;
}
function clean(value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(typeof value === 'string' ? value : JSON.stringify(value));
  for (const secret of sensitive) assert.ok(!bytes.includes(Buffer.from(secret)), 'Sensitive data escaped its intended boundary');
}
async function create(name, provider_kind = 'openai-compatible') {
  const key = 'synthetic-key-' + randomUUID(); sensitive.push(key); credentials.set(key, {});
  const item = await admin('/upstreams', 'POST', { name, provider_kind, endpoint, api_key: key }, 201);
  return { ...item, key };
}
const operation = (revision, scope = 'catalog') => ({ operation_id: randomUUID(), expected_revision: revision, scope });
const test = (account, input, expected = 200) => admin(`/upstreams/${account.id}/tests`, 'POST', input, expected);
async function view(account) {
  const list = await admin('/upstreams'); assert.ok(Number.isFinite(Date.parse(list.server_time)));
  return list.items.find(item => item.id === account.id);
}
function completed(result, code) { assert.equal(result.state, 'completed'); assert.equal(result.result_code, code); }

try {
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening'); endpoint = `http://127.0.0.1:${mock.address().port}`;
  await start();
  const primary = await create('Synthetic primary'), backup = await create('Synthetic backup');
  const claude = await create('Synthetic Claude', 'anthropic-api-key'), gemini = await create('Synthetic Gemini', 'gemini-api-key');
  const employee = await admin('/employees', 'POST', { name: 'Synthetic health employee' }, 201);
  const issued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'health test', operation_id: randomUUID() }, 201);
  sensitive.push(issued.key);
  if (previousExecutable) {
    await stop(); activeExecutable = executable; await start();
    assert.equal((await view(primary)).revision, 1, 'Upgrade lost the baseline account');
    assert.equal((await admin('/employees')).items[0].id, employee.id, 'Upgrade lost the baseline employee');
  }
  const features = (await admin('/system/status')).features;
  assert.equal(features.upstream_account_tests, true); assert.equal(features.upstream_cooldown_management, true);
  const beforeLocal = calls.length;
  completed(await test(primary, operation(1, 'local_credential')), 'local_credential_ok');
  assert.equal(calls.length, beforeLocal, 'Local credential check made a network request');
  for (const account of [primary, claude, gemini]) {
    const input = operation(1), before = calls.length;
    const result = await test(account, input); completed(result, 'catalog_ok');
    assert.equal(result.tested_revision, 1); assert.equal(result.requested_revision, 1);
    const count = calls.length; assert.ok(count > before);
    assert.deepEqual(await test(account, input), result); assert.equal(calls.length, count, 'Duplicate operation repeated provider access');
    assert.deepEqual(await admin(`/upstreams/${account.id}/tests/${input.operation_id}`), result);
    await test(account, { ...input, scope: 'local_credential' }, 409);
    await test(backup, input, 409);
    assert.equal((await view(account)).latest_observation.result_code, 'catalog_ok');
  }
  assert.equal(calls.filter(call => call.url.startsWith('/v1beta/')).length, 2, 'Gemini catalog did not traverse two pages');
  assert.equal(calls.filter(call => call.key === claude.key).length, 2, 'Anthropic catalog did not traverse two pages');
  for (const bad of [0, -1, 1.5, '1', null]) await test(primary, operation(bad), 400);
  await admin(`/upstreams/${primary.id}/tests`, 'POST', '{"operation_id":"' + randomUUID() + '","expected_revision":1,"expected_revision":1,"scope":"catalog"}', 400);
  await test(primary, { ...operation(1), endpoint }, 400);
  await admin(`/upstreams/${primary.id}/tests`, 'POST', operation(1), 403, { 'X-CSRF-Token': '' });
  await admin(`/upstreams/${primary.id}/tests`, 'POST', operation(1), 401, { Cookie: '', Authorization: 'Bearer ' + issued.key });
  credentials.get(primary.key).catalogStatus = 401;
  completed(await test(primary, operation(1)), 'authentication_failed');
  credentials.get(primary.key).catalogStatus = 429;
  completed(await test(primary, operation(1)), 'rate_limited');
  credentials.get(primary.key).catalogStatus = 200;

  const staleInput = operation(1), staleGate = gateNext(primary.key);
  const staleResult = test(primary, staleInput); await staleGate.entered;
  assert.equal((await test(primary, staleInput, 202)).state, 'in_progress');
  await test(primary, operation(1), 409);
  const replacement = 'synthetic-key-' + randomUUID(); sensitive.push(replacement); credentials.set(replacement, {});
  await admin(`/upstreams/${primary.id}`, 'PATCH', { expected_revision: 1, api_key: replacement });
  primary.key = replacement; primary.revision = 2;
  staleGate.release(); completed(await staleResult, 'stale');
  assert.equal((await view(primary)).latest_observation, null, 'Old credential observation leaked into new revision');

  const interruptedInput = operation(2), interruptedGate = gateNext(primary.key);
  const interruptedResponse = test(primary, interruptedInput).catch(() => null);
  await interruptedGate.entered; const crashCalls = calls.length;
  await stop(true); interruptedGate.release(); await interruptedResponse;
  await start();
  completed(await admin(`/upstreams/${primary.id}/tests/${interruptedInput.operation_id}`), 'interrupted');
  completed(await test(primary, interruptedInput), 'interrupted');
  assert.equal(calls.length, crashCalls, 'Restart replayed an uncertain catalog operation');

  await admin('/models', 'POST', { id: 'health-pool', upstream_id: primary.id, upstream_model: 'synthetic-model' }, 201);
  await admin('/models/health-pool/accounts', 'PUT', { expected_revision: 0, items: [primary, backup].map((account, i) => ({ upstream_id: account.id, upstream_model: 'synthetic-model', priority: i ? 1 : 10, weight: 1, max_concurrency: 1 })) });
  const generate = async (expected = 502) => {
    const response = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + issued.key, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: 'health-pool', messages: [{ role: 'user', content: 'synthetic health fixture' }] }), signal: AbortSignal.timeout(10000) });
    assert.equal(response.status, expected); clean(await response.text());
  };
  credentials.get(primary.key).generationStatus = 429;
  await generate(); const cooling = await view(primary); assert.equal(cooling.cooldown.active, true);
  assert.ok(cooling.cooldown.event_id); const event = cooling.cooldown.event_id;
  completed(await test(primary, operation(2)), 'catalog_ok');
  assert.equal((await view(primary)).cooldown.event_id, event, 'Catalog success cleared cooldown');
  const clear = (revision, eventID, expected = 200) => admin(`/upstreams/${primary.id}/cooldown/clear`, 'POST', { expected_revision: revision, expected_cooldown_event_id: eventID }, expected);
  await clear(1, event, 409); await clear(2, 'stale-event', 409);
  assert.equal((await clear(2, event)).result, 'cleared');
  const cleared = await view(primary); assert.equal(cleared.cooldown, null); assert.equal(cleared.revision, 2); assert.equal(cleared.enabled, true);
  assert.equal(cleared.latest_observation.result_code, 'catalog_ok');
  assert.equal((await clear(2, event)).result, 'already_clear');
  await generate(); const newer = (await view(primary)).cooldown; assert.notEqual(newer.event_id, event);
  await clear(2, event, 409); assert.equal((await view(primary)).cooldown.event_id, newer.event_id);
  await stop(); await start(); assert.equal((await view(primary)).cooldown.event_id, newer.event_id, 'Cooldown event was not durable');
  assert.equal((await clear(2, newer.event_id)).result, 'cleared');
  credentials.get(primary.key).generationStatus = 200;
  const beforeHealthy = calls.length; await generate(200); assert.equal(calls.length, beforeHealthy + 1); assert.equal(calls.at(-1).key, primary.key, 'Memory cooldown survived manual clear');
  await admin('/keys/' + issued.id + '/revoke', 'POST', {});
  const beforeRevoked = calls.length; await generate(401); assert.equal(calls.length, beforeRevoked);
  assert.deepEqual(mockErrors, []); await stop(); clean(logs);
  for (const entry of await readdir(directory, { withFileTypes: true })) if (entry.isFile()) clean(await readFile(path.join(directory, entry.name)));
  console.log('PASS: local/no-network and three-provider catalog, pagination, stable operation IDs, strict input and admin auth, stale replacement, crash/interrupted/no replay, cooldown event CAS, restart, memory clear, employee revocation, metadata isolation');
} finally {
  for (const gate of gates.values()) gate.release();
  await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory); assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-health-'));
  await rm(resolved, { recursive: true, force: true });
}
