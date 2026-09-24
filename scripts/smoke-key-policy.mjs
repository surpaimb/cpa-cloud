// Real isolated CPA Cloud process acceptance for per-key protocol/model policy.
// Usage: node scripts/smoke-key-policy.mjs <absolute executable> <absolute web directory>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, webDirectory] = process.argv.slice(2);
for (const value of [executable, webDirectory]) assert.ok(value && path.isAbsolute(value), 'Absolute paths required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-key-policy-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = randomBytes(24).toString('hex');
let child, cookie = '', csrf = '', calls = 0;

const mock = http.createServer(async (request, response) => {
  const chunks = []; for await (const chunk of request) chunks.push(chunk);
  const body = JSON.parse(Buffer.concat(chunks));
  calls++;
  assert.equal(request.url, '/v1/chat/completions');
  assert.equal(request.headers.authorization, `Bearer ${upstreamSecret}`);
  assert.equal(body.model, 'actual-policy-model');
  response.writeHead(200, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify({ id: 'result', object: 'chat.completion', model: body.model, choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }] }));
});
const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
const baseArgs = ['--data-dir', directory];
const launch = args => spawn(executable, [...baseArgs, ...args], { windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'] });
async function stop() { if (child && child.exitCode === null && child.signalCode === null) { const exited = once(child, 'exit'); child.kill(); await exited; } }
async function start() {
  child = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--web-dir', webDirectory]);
  for (let attempt = 0; attempt < 100; attempt++) { try { if ((await fetch(origin + '/healthz')).ok) return; } catch {} await delay(100); }
  throw new Error('Service readiness timeout');
}
async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method, headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
  if (response.status !== expected) throw new Error(`${method} ${route}: ${response.status} ${await response.text()}`);
  const setCookie = response.headers.get('set-cookie'); if (setCookie) cookie = setCookie.split(';')[0];
  return response.json();
}
async function call(pathname, key, body, header = 'Authorization') {
  const headers = { 'Content-Type': 'application/json' }; headers[header] = header === 'Authorization' ? `Bearer ${key}` : key;
  return fetch(origin + pathname, { method: 'POST', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(5000) });
}

try {
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening'); await start();
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const upstream = await admin('/upstreams', 'POST', { name: 'Synthetic policy', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}`, api_key: upstreamSecret }, 201);
  await admin('/models', 'POST', { id: 'policy-model', upstream_id: upstream.id, upstream_model: 'actual-policy-model', wire_protocol: 'openai-chat' }, 201);
  const employee = await admin('/employees', 'POST', { name: 'Synthetic policy employee' }, 201);
  const issued = await admin(`/employees/${employee.id}/keys`, 'POST', {
    name: 'deny first', operation_id: randomUUID(),
    policy: { protocol_mode: 'selected', protocols: [], model_mode: 'selected', models: [] },
  }, 201);
  assert.ok(issued.key); assert.equal(issued.policy.revision, 1);
  const requests = [
    ['chat', () => call('/v1/chat/completions', issued.key, { model: 'policy-model', messages: [{ role: 'user', content: 'denied' }] })],
    ['responses', () => call('/v1/responses', issued.key, { model: 'policy-model', input: 'denied' })],
    ['anthropic', () => fetch(origin + '/v1/messages', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-API-Key': issued.key, 'Anthropic-Version': '2023-06-01' }, body: JSON.stringify({ model: 'policy-model', max_tokens: 8, messages: [{ role: 'user', content: 'denied' }] }), signal: AbortSignal.timeout(5000) })],
    ['gemini', () => call('/v1beta/models/policy-model:generateContent', issued.key, { contents: [{ role: 'user', parts: [{ text: 'denied' }] }] }, 'X-Goog-Api-Key')],
  ];
  for (const [name, request] of requests) { const response = await request(); assert.equal(response.status, 403, name); await response.text(); }
  assert.equal(calls, 0, 'Denied protocols reached upstream');
  const modelList = await fetch(origin + '/v1/models', { headers: { Authorization: `Bearer ${issued.key}` } });
  assert.deepEqual((await modelList.json()).data, []);
  const geminiList = await fetch(origin + '/v1beta/models', { headers: { 'X-Goog-Api-Key': issued.key } });
  assert.deepEqual((await geminiList.json()).models, []);

  const allowed = await admin(`/keys/${issued.id}/policy`, 'PUT', { expected_revision: 1, protocol_mode: 'selected', protocols: ['openai-chat'], model_mode: 'selected', models: ['policy-model'] });
  assert.equal(allowed.revision, 2); assert.deepEqual(allowed.effective_models, ['policy-model']);
  const stale = await admin(`/keys/${issued.id}/policy`, 'PUT', { expected_revision: 1, protocol_mode: 'all', protocols: [], model_mode: 'all', models: [] }, 409);
  assert.equal(stale.error.code, 'revision_conflict');
  const success = await requests[0][1](); assert.equal(success.status, 200); await success.text(); assert.equal(calls, 1);
  const listing = await admin(`/employees/${employee.id}/keys`); assert.ok(!JSON.stringify(listing).includes(issued.key)); assert.equal(listing.items[0].policy.revision, 2);

  await stop(); cookie = ''; csrf = ''; await start();
  const restored = await requests[0][1](); assert.equal(restored.status, 200); await restored.text(); assert.equal(calls, 2);
  console.log('PASS: real process key policy four-protocol denial, zero dispatch, directory intersection, CAS, plaintext redaction and restart persistence');
} finally {
  await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir())); assert.ok(path.basename(resolved).startsWith('cpac-key-policy-'));
  await rm(resolved, { recursive: true, force: true });
}
