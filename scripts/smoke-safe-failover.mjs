// Independent process acceptance: only synthetic credentials and loopback providers.
// Requires Node 22.13+ (node:sqlite). Fixture corruption occurs only while Go is stopped.
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

const [executable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'An absolute executable path is required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-preflight-'));
const password = randomBytes(24).toString('hex');
const prompt = 'synthetic-prompt-' + randomUUID();
const privateError = 'synthetic-private-error-' + randomUUID();
const secrets = [password, prompt, privateError];
const accounts = new Map(), received = [], mockErrors = [], pools = [];
let child, origin, cookie = '', csrf = '', logs = '';

const mock = http.createServer(async (req, res) => {
  try {
    let raw = '';
    for await (const chunk of req) raw += chunk;
    const input = JSON.parse(raw);
    const credential = req.headers['x-goog-api-key'] ?? req.headers.authorization?.replace('Bearer ', '');
    const account = accounts.get(credential);
    assert.ok(account, 'Unexpected provider credential');
    for (const name of ['cookie', 'origin', 'x-csrf-token', 'x-cpa-session']) assert.equal(req.headers[name], undefined);
    received.push({ account, model: input.model, url: req.url });
    if (account === 'unsafe-first') {
      res.writeHead(429, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: privateError }));
      return;
    }
    res.setHeader('Content-Type', 'application/json');
    if (req.url.endsWith('/responses')) {
      res.end(JSON.stringify({ id: 'response_synthetic', object: 'response', status: 'completed', output: [],
        usage: { input_tokens: 20, output_tokens: 3, input_tokens_details: { cached_tokens: 0, cache_write_tokens: 0 } } }));
    } else if (req.url.endsWith('/messages/count_tokens')) {
      res.end('{"input_tokens":20}');
    } else if (req.url.endsWith('/messages')) {
      res.end(JSON.stringify({ id: 'message_synthetic', type: 'message', role: 'assistant', model: input.model,
        content: [{ type: 'text', text: 'ok' }], stop_reason: 'end_turn',
        usage: { input_tokens: 20, output_tokens: 3, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 } }));
    } else if (req.url.startsWith('/v1beta/')) {
      res.end(JSON.stringify({ candidates: [{ content: { parts: [{ text: 'ok' }] }, finishReason: 'STOP' }],
        usageMetadata: { promptTokenCount: 20, candidatesTokenCount: 3, thoughtsTokenCount: 0, cachedContentTokenCount: 0, totalTokenCount: 23 } }));
    } else {
      res.end(JSON.stringify({ id: 'chat_synthetic', object: 'chat.completion', choices: [],
        usage: { prompt_tokens: 20, completion_tokens: 3, prompt_tokens_details: { cached_tokens: 0, cache_write_tokens: 0 } } }));
    }
  } catch (error) {
    mockErrors.push(error.message);
    if (!res.headersSent) res.writeHead(500);
    res.end();
  }
});

function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = once(child, 'exit');
    child.stdin.end();
    const timeout = setTimeout(() => child.kill(), 5000);
    try { await exited; } finally { clearTimeout(timeout); }
  }
}
async function start() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  origin = `http://127.0.0.1:${probe.address().port}`;
  await new Promise(resolve => probe.close(resolve));
  child = launch(['--listen', new URL(origin).host, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let i = 0; i < 100; i++) {
    assert.equal(child.exitCode, null, 'Test server exited before readiness');
    try {
      if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) {
        cookie = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
        return;
      }
    } catch { /* poll this isolated process only */ }
    await delay(100);
  }
  throw Error('Isolated test server did not become ready');
}
async function admin(route, method = 'GET', body, status = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method,
    headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000) });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  assert.equal(response.status, status, `${method} ${route} returned an unexpected status`);
  return response.json();
}
async function request(pool, key, countTokens = false) {
  const url = pool.protocol === 'gemini' ? `/v1beta/models/${pool.model}:generateContent` :
    pool.protocol === 'responses' ? '/v1/responses' : pool.protocol === 'claude' ? '/v1/messages' + (countTokens ? '/count_tokens' : '') : '/v1/chat/completions';
  const body = pool.protocol === 'gemini' ? { contents: [{ parts: [{ text: prompt }] }] } :
    pool.protocol === 'responses' ? { model: pool.model, input: prompt } :
    { model: pool.model, messages: [{ role: 'user', content: prompt }], ...(pool.protocol === 'claude' && !countTokens ? { max_tokens: 16 } : {}) };
  return fetch(origin + url, { method: 'POST',
    headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json', 'X-CPA-Session': 'synthetic-sticky',
      ...(pool.protocol === 'claude' ? { 'Anthropic-Version': '2023-06-01' } : {}) },
    body: JSON.stringify(body), signal: AbortSignal.timeout(10000) });
}
async function createPool(protocol, providerKind, endpoint, prefix = protocol) {
  const items = [], members = [];
  for (const [index, suffix] of ['first', 'second'].entries()) {
    const apiKey = 'synthetic-' + randomUUID(), name = prefix + '-' + suffix;
    secrets.push(apiKey); accounts.set(apiKey, name);
    const member = await admin('/upstreams', 'POST', { name, provider_kind: providerKind, endpoint, api_key: apiKey }, 201);
    const actualModel = 'actual-' + name;
    members.push(member);
    items.push({ upstream_id: member.id, upstream_model: actualModel, priority: index ? 1 : 10, weight: 1, max_concurrency: 1 });
    await admin(`/upstreams/${member.id}/prices`, 'POST', { operation_id: randomUUID(), expected_revision: 0, upstream_model: actualModel,
      price: { currency: 'USD', input_per_million_micro: index ? '1000000' : '9000000', output_per_million_micro: '1000000',
        cache_read_per_million_micro: '0', cache_write_per_million_micro: '0' } });
  }
  const model = 'preflight-' + prefix;
  await admin('/models', 'POST', { id: model, upstream_id: members[0].id, upstream_model: 'legacy-' + prefix }, 201);
  await admin(`/models/${model}/accounts`, 'PUT', { expected_revision: 0, items });
  return { protocol, model, members, items, prefix };
}
function fixtureDatabase() {
  assert.ok(!child || child.exitCode !== null || child.signalCode !== null, 'Fixture database requires stopped Go process');
  return new DatabaseSync(path.join(directory, 'cpa-cloud.db'));
}

try {
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  const endpoint = `http://127.0.0.1:${mock.address().port}`;
  await start();
  const employee = await admin('/employees', 'POST', { name: 'Synthetic preflight employee' }, 201);
  const key = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'synthetic', operation_id: randomUUID() }, 201);
  secrets.push(key.key);
  for (const [protocol, provider] of [['chat', 'openai-compatible'], ['responses', 'openai-compatible'], ['claude', 'anthropic-api-key'], ['gemini', 'gemini-api-key']]) {
    pools.push(await createPool(protocol, provider, endpoint));
  }
  const unsafe = await createPool('chat', 'openai-compatible', endpoint, 'unsafe');
  await stop();
  let db = fixtureDatabase();
  try {
    // A synthetic corruption fixture; never inspect or mutate any user database.
    const damage = db.prepare('UPDATE upstreams SET credential_ciphertext=? WHERE id=?');
    for (const pool of pools) assert.equal(damage.run(new Uint8Array([1, 2, 3]), pool.members[0].id).changes, 1);
  } finally { db.close(); }
  await start();
  for (const pool of pools) {
    const count = received.length;
    const response = await request(pool, key.key); assert.equal(response.status, 200, pool.protocol + ' preflight failover'); await response.text();
    assert.equal(received.length, count + 1, 'Unsent account produced a provider request');
    assert.equal(received.at(-1).account, pool.prefix + '-second');
    if (pool.protocol === 'gemini') assert.ok(received.at(-1).url.includes('/actual-gemini-second:'));
    else assert.equal(received.at(-1).model, 'actual-' + pool.prefix + '-second');
  }
  const counted = await request(pools.find(pool => pool.protocol === 'claude'), key.key, true);
  assert.equal(counted.status, 200); await counted.text();
  const before = received.length, rejected = await request(unsafe, key.key);
  assert.equal(rejected.status, 502); assert.ok(!(await rejected.text()).includes(privateError));
  assert.equal(received.length, before + 1, '429 dispatched a second account');
  assert.equal(received.at(-1).account, 'unsafe-first');
  // The API's default window ends at the current whole second. Use an explicit
  // whole-second window that includes requests completed in this same second.
  const wholeSecond = time => new Date(time).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const query = new URLSearchParams({ from: wholeSecond(Date.now() - 60000), to: wholeSecond(Date.now() + 2000) });
  const requests = await admin('/usage/requests?' + query); assert.equal(requests.items.length, 5, 'count_tokens was incorrectly billed');
  await stop();
  db = fixtureDatabase();
  try {
    const rows = db.prepare('SELECT r.model_id,a.account_id,a.dispatch,a.cost_micro FROM accounting_requests r JOIN accounting_attempts a ON a.request_id=r.id').all();
    assert.equal(rows.length, 5);
    for (const pool of pools) {
      const row = rows.find(item => item.model_id === pool.model);
      assert.equal(row.account_id, pool.members[1].id); assert.equal(row.dispatch, 'failover'); assert.equal(row.cost_micro, 23);
    }
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM model_requests').get().n, 5);
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM accounting_requests').get().n, 5);
  } finally { db.close(); }
  await start();
  const afterRestart = await request(pools[0], key.key); assert.equal(afterRestart.status, 200); await afterRestart.text();
  await admin(`/keys/${key.id}/revoke`, 'POST', {});
  const count = received.length, denied = await request(pools[0], key.key);
  assert.equal(denied.status, 401); await denied.text(); assert.equal(received.length, count);
  assert.deepEqual(mockErrors, []);
  await stop();
  for (const bytes of [Buffer.from(logs), ...await Promise.all((await readdir(directory, { withFileTypes: true })).filter(entry => entry.isFile()).map(entry => readFile(path.join(directory, entry.name))))]) {
    assert.ok(!secrets.some(secret => bytes.includes(Buffer.from(secret))), 'Secret or model content persisted outside encrypted storage');
  }
  console.log('PASS: four-protocol preflight-only failover, actual model/price, one parent/attempt, count_tokens exclusion, no 429 replay, restart, key revocation, metadata isolation');
} finally {
  await stop(); mock.closeAllConnections();
  if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory);
  assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-preflight-'));
  await rm(resolved, { recursive: true, force: true });
}
