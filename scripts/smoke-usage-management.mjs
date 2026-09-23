// Independent process acceptance for usage reporting and versioned cost prices.
// All accounts, credentials and model traffic below are synthetic and temporary.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-usage-'));
const password = randomBytes(24).toString('hex');
const prompt = 'synthetic-prompt-' + randomUUID(), answer = 'synthetic-answer-' + randomUUID();
const secrets = [password, prompt, answer];
const credentials = new Map(), received = [], mockErrors = [];
let child, origin, cookie = '', csrf = '', logs = '', blockNext = false, release, entered, partialNext = false;
const mock = http.createServer(async (req, res) => {
  try {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const input = JSON.parse(raw), account = credentials.get(req.headers.authorization?.replace('Bearer ', ''));
    assert.ok(account, 'Unknown synthetic credential');
    for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
    received.push({ account, model: input.model });
    if (blockNext) { blockNext = false; await new Promise(resolve => { release = resolve; entered(); }); }
    const usage = partialNext ? { prompt_tokens: 100, completion_tokens: 50 } : { prompt_tokens: 100, completion_tokens: 50, total_tokens: 150, prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 10 } };
    partialNext = false;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ id: 'synthetic-chat', object: 'chat.completion', model: input.model, choices: [{ index: 0, message: { role: 'assistant', content: answer }, finish_reason: 'stop' }], usage }));
  } catch (error) { mockErrors.push(error.message); if (!res.headersSent) res.writeHead(500); res.end(); }
});
function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exit = once(child, 'exit'); child.stdin.end(); const timer = setTimeout(() => child.kill(), 5000);
    try { await exit; } finally { clearTimeout(timer); }
  }
}
async function start() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening'); origin = `http://127.0.0.1:${probe.address().port}`; await new Promise(resolve => probe.close(resolve));
  child = launch(['--listen', new URL(origin).host, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw Error('Service startup failed');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) { cookie = ''; csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token; return; } } catch {}
    await delay(100);
  }
  throw Error('Readiness timeout');
}
async function admin(route, method = 'GET', body, status = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method, headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000) });
  if (response.headers.get('set-cookie')) cookie = response.headers.get('set-cookie').split(';')[0];
  const raw = await response.text(); assert.equal(response.status, status, method + ' ' + route + ' ' + raw); return JSON.parse(raw);
}
const price = multiplier => ({ currency: 'USD', input_per_million_micro: String(1000000 * multiplier), output_per_million_micro: String(2000000 * multiplier), cache_read_per_million_micro: String(100000 * multiplier), cache_write_per_million_micro: String(1500000 * multiplier) });
const priceBody = (revision, rates, model = 'actual-model') => ({ operation_id: randomUUID(), expected_revision: revision, upstream_model: model, price: rates });
function noSecrets(value) { const bytes = Buffer.from(value); assert.ok(!secrets.some(secret => bytes.includes(Buffer.from(secret))), 'Sensitive content persisted'); }
try {
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening'); const endpoint = `http://127.0.0.1:${mock.address().port}`;
  await start();
  const employee = await admin('/employees', 'POST', { name: 'Synthetic Usage Employee' }, 201);
  const key = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'usage', operation_id: randomUUID() }, 201); secrets.push(key.key);
  const accounts = [];
  for (const name of ['primary', 'backup']) {
    const api_key = 'synthetic-key-' + randomUUID(); secrets.push(api_key); credentials.set(api_key, name);
    accounts.push(await admin('/upstreams', 'POST', { name, provider_kind: 'openai-compatible', endpoint, api_key }, 201));
  }
  const [primary, backup] = accounts;
  await admin('/models', 'POST', { id: 'public-model', upstream_id: primary.id, upstream_model: 'actual-model' }, 201);
  const paths = accounts.map(account => '/upstreams/' + account.id + '/prices');
  const firstBody = priceBody(0, price(1)), first = await admin(paths[0], 'POST', firstBody);
  await admin(paths[0], 'POST', priceBody(0, price(9), 'public-model'));
  async function request() {
    const response = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + key.key, 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'public-model', messages: [{ role: 'user', content: prompt }] }), signal: AbortSignal.timeout(10000) });
    const result = await response.json(); assert.equal(response.status, 200); assert.equal(result.choices[0].message.content, answer); return response.headers.get('x-request-id');
  }
  blockNext = true; const began = new Promise(resolve => { entered = resolve; }); const pending = request(); await began;
  const secondBody = priceBody(1, price(2)), second = await admin(paths[0], 'POST', secondBody); release(); const firstID = await pending;
  assert.equal((await admin(paths[0], 'POST', firstBody)).version, first.version, 'Exact old operation replay');
  assert.equal((await admin(paths[0])).items.find(item => item.upstream_model === 'actual-model').version, second.version, 'Old replay rewound current price');
  const changed = { ...firstBody, price: price(3) }; assert.equal((await admin(paths[0], 'POST', changed, 409)).error.code, 'operation_conflict');
  assert.equal((await admin(paths[0], 'POST', priceBody(0, price(1)), 409)).error.code, 'revision_conflict');
  const secondID = await request();
  await admin(paths[0], 'POST', priceBody(2, null)); const disabledID = await request();
  await admin(paths[0], 'POST', priceBody(3, price(1))); partialNext = true; const partialID = await request();
  await admin(paths[1], 'POST', priceBody(0, price(3)));
  await admin('/models/public-model/accounts', 'PUT', { expected_revision: 0, items: [{ upstream_id: primary.id, upstream_model: 'actual-model', priority: 10, weight: 1, max_concurrency: 1 }, { upstream_id: backup.id, upstream_model: 'actual-model', priority: 1, weight: 1, max_concurrency: 1 }] });
  await admin('/upstreams/' + primary.id, 'PATCH', { expected_revision: 1, enabled: false }); const backupID = await request(); assert.equal(received.at(-1).account, 'backup');
  const check = async (id, cost, version, account) => {
    const detail = (await admin('/usage/requests/' + id + '/attempts')).items; assert.equal(detail.length, 1); assert.equal(detail[0].cost_micro, cost); assert.equal(detail[0].account_id, account);
    if (version !== undefined) assert.equal(detail[0].price_version, version); return detail[0];
  };
  await check(firstID, '187', first.version, primary.id); await check(secondID, '374', second.version, primary.id); await check(disabledID, null, null, primary.id); await check(partialID, null, undefined, primary.id); await check(backupID, '561', undefined, backup.id);
  const from = new Date(Math.floor(Date.now() / 1000) * 1000 - 3600000).toISOString().replace('.000Z', 'Z'), to = new Date(Math.floor(Date.now() / 1000) * 1000 + 3600000).toISOString().replace('.000Z', 'Z');
  const window = new URLSearchParams({ from, to, employee_id: employee.id }).toString();
  const summary = await admin('/usage/summary?' + window); assert.equal(summary.requests.total, '5'); assert.equal(summary.requests.succeeded, '5');
  const usd = summary.attempts.find(item => item.currency === 'USD'), unknown = summary.attempts.find(item => item.currency === 'UNKNOWN');
  assert.equal(usd.known_cost_micro, '1122'); assert.equal(usd.unknown_cost_attempts, '1'); assert.equal(unknown.unknown_cost_attempts, '1');
  const filtered = await admin('/usage/summary?' + window + '&upstream_id=' + backup.id); assert.equal(filtered.requests.total, '1'); assert.equal(filtered.attempts[0].known_cost_micro, '561');
  const ids = []; let cursor = null;
  do { const page = await admin('/usage/requests?' + window + '&limit=2' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : '')); ids.push(...page.items.map(item => item.id)); cursor = page.next_cursor; } while (cursor);
  assert.equal(new Set(ids).size, 5); assert.equal(ids.length, 5);
  for (const route of ['/usage/summary', '/usage/requests', '/usage/requests/' + firstID + '/attempts', paths[0]]) {
    const denied = await fetch(origin + '/admin/api/v1' + route, { headers: { Authorization: 'Bearer ' + key.key } }); assert.equal(denied.status, 401); await denied.text();
  }
  const oldCsrf = csrf; csrf = 'invalid'; await admin(paths[0], 'POST', priceBody(4, price(1)), 403); csrf = oldCsrf;
  await admin('/usage/summary?' + window + '&status=ok', 'GET', undefined, 400);
  await stop(); await start(); await check(firstID, '187', first.version, primary.id); await check(secondID, '374', second.version, primary.id);
  assert.equal((await admin('/usage/summary?' + window)).requests.total, '5');
  await admin('/keys/' + key.id + '/revoke', 'POST', {});
  const revoked = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: 'Bearer ' + key.key, 'Content-Type': 'application/json' }, body: '{"model":"public-model","messages":[]}' }); assert.equal(revoked.status, 401); await revoked.text(); assert.equal(received.length, 5);
  assert.deepEqual(mockErrors, []); await stop(); noSecrets(logs);
  for (const entry of await readdir(directory, { withFileTypes: true })) if (entry.isFile()) noSecrets(await readFile(path.join(directory, entry.name)));
  console.log('PASS: price versions/idempotency/CAS, in-flight snapshot, actual pool account/model, unknown cost, summary/filter/pagination, admin isolation/CSRF, restart, revocation, metadata-only persistence');
} finally {
  release?.(); await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory); assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-usage-')); await rm(resolved, { recursive: true, force: true });
}
