// Real isolated CPA Cloud process + synthetic Chat/Responses SSE acceptance.
// Usage: node scripts/smoke-protocol-stream.mjs <absolute executable> <absolute web directory>
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-protocol-stream-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = randomBytes(24).toString('hex');
let child, cookie = '', csrf = '', calls = 0, cancelled = false;

const responsesSSE = [
  ['response.created', { type: 'response.created', sequence_number: 0, response: { id: 'resp', created_at: 1, model: 'actual-responses' } }],
  ['response.output_item.added', { type: 'response.output_item.added', sequence_number: 1, output_index: 0, item: { id: 'msg', type: 'message', status: 'in_progress', role: 'assistant', content: [] } }],
  ['response.content_part.added', { type: 'response.content_part.added', sequence_number: 2, item_id: 'msg', output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } }],
  ['response.output_text.delta', { type: 'response.output_text.delta', sequence_number: 3, item_id: 'msg', output_index: 0, content_index: 0, delta: 'stream-ok' }],
  ['response.output_text.done', { type: 'response.output_text.done', sequence_number: 4, item_id: 'msg', output_index: 0, content_index: 0, text: 'stream-ok' }],
  ['response.content_part.done', { type: 'response.content_part.done', sequence_number: 5, item_id: 'msg', output_index: 0, content_index: 0, part: { type: 'output_text', text: 'stream-ok', annotations: [] } }],
  ['response.output_item.done', { type: 'response.output_item.done', sequence_number: 6, output_index: 0, item: { id: 'msg', type: 'message', status: 'completed', role: 'assistant', content: [{ type: 'output_text', text: 'stream-ok', annotations: [] }] } }],
  ['response.completed', { type: 'response.completed', sequence_number: 7, response: { id: 'resp', object: 'response', created_at: 1, model: 'actual-responses', status: 'completed', output: [{ id: 'msg', type: 'message', status: 'completed', role: 'assistant', content: [{ type: 'output_text', text: 'stream-ok', annotations: [] }] }], usage: { input_tokens: 2, output_tokens: 1, total_tokens: 3 } } }],
];

const mock = http.createServer(async (request, response) => {
  const chunks = []; for await (const chunk of request) chunks.push(chunk);
  const body = JSON.parse(Buffer.concat(chunks));
  assert.equal(request.headers.authorization, `Bearer ${upstreamSecret}`);
  calls++;
  response.writeHead(200, { 'Content-Type': 'text/event-stream' });
  if (request.url === '/v1/responses') {
    if (body.model === 'actual-cancel') {
      for (const [name, event] of responsesSSE.slice(0, 4)) response.write(`event: ${name}\ndata: ${JSON.stringify(event)}\n\n`);
      response.on('close', () => { cancelled = true; });
      return;
    }
    assert.equal(body.model, 'actual-responses');
    for (const [name, event] of responsesSSE) response.write(`event: ${name}\ndata: ${JSON.stringify(event)}\n\n`);
    response.end();
    return;
  }
  assert.equal(request.url, '/v1/chat/completions');
  assert.equal(body.model, 'actual-chat');
  response.write('data: {"id":"chat","object":"chat.completion.chunk","created":1,"model":"actual-chat","choices":[{"index":0,"delta":{"content":"reverse-ok"},"finish_reason":"stop"}]}\n\n');
  response.end('data: [DONE]\n\n');
});

const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
const baseArgs = ['--data-dir', directory];
const launch = args => spawn(executable, [...baseArgs, ...args], { windowsHide: true, stdio: ['pipe', 'ignore', 'ignore'] });
async function stop() { if (child && child.exitCode === null && child.signalCode === null) { const exited = once(child, 'exit'); child.kill(); await exited; } }
async function admin(route, method = 'GET', body) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method, headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`${method} ${route}: ${response.status} ${await response.text()}`);
  const setCookie = response.headers.get('set-cookie'); if (setCookie) cookie = setCookie.split(';')[0];
  return response.json();
}
async function employee(pathname, key, body, signal = AbortSignal.timeout(5000)) {
  return fetch(origin + pathname, { method: 'POST', headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal });
}
async function assertSingleSettledAttempt(model, expectedOutput) {
  let rows = [];
  for (let attempt = 0; attempt < 30; attempt++) {
    rows = (await admin(`/usage/requests?model_id=${encodeURIComponent(model)}&limit=100`)).items;
    if (rows.length === 1 && rows[0].status !== 'pending') break;
    await delay(100);
  }
  assert.equal(rows.length, 1, `${model}: parent request count`);
  assert.equal(rows[0].status, 'succeeded', `${model}: parent status`);
  assert.equal(Number(rows[0].attempt_count), 1, `${model}: attempt count`);
  const attempts = (await admin(`/usage/requests/${rows[0].id}/attempts`)).items;
  assert.equal(attempts.length, 1, `${model}: attempt detail count`);
  assert.equal(attempts[0].status, 'succeeded', `${model}: attempt status`);
  assert.equal(attempts[0].output_tokens, expectedOutput, `${model}: original wire output usage`);
  assert.equal(attempts[0].input_tokens, null, `${model}: unknown input usage was coerced`);
  return rows;
}

try {
  console.error('stage: initialize');
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
  console.error('stage: start');
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  child = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--web-dir', webDirectory]);
  let ready = false;
  for (let attempt = 0; attempt < 100; attempt++) { try { if ((await fetch(origin + '/healthz')).ok) { ready = true; break; } } catch {} await delay(100); }
  assert.ok(ready, 'Service readiness timeout');
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const upstream = await admin('/upstreams', 'POST', { name: 'Synthetic SSE', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}`, api_key: upstreamSecret });
  for (const model of [
    { id: 'chat-via-responses', upstream_model: 'actual-responses', wire_protocol: 'openai-responses' },
    { id: 'responses-via-chat', upstream_model: 'actual-chat', wire_protocol: 'openai-chat' },
    { id: 'cancel-via-responses', upstream_model: 'actual-cancel', wire_protocol: 'openai-responses' },
    { id: 'denied-via-responses', upstream_model: 'actual-responses', wire_protocol: 'openai-responses' },
  ]) await admin('/models', 'POST', { ...model, upstream_id: upstream.id });
  const employeeObject = await admin('/employees', 'POST', { name: 'Synthetic stream employee' });
  const selectedModels = ['chat-via-responses', 'responses-via-chat', 'cancel-via-responses'];
  const issued = await admin(`/employees/${employeeObject.id}/keys`, 'POST', {
    name: 'stream', operation_id: randomUUID(),
    policy: {
      protocol_mode: 'selected', protocols: ['openai-chat', 'openai-responses'],
      model_mode: 'selected', models: selectedModels,
    },
  });

  console.error('stage: chat-to-responses');
  const chat = await employee('/v1/chat/completions', issued.key, { model: 'chat-via-responses', stream: true, messages: [{ role: 'user', content: 'hello' }] });
  const chatBody = await chat.text(); assert.equal(chat.status, 200); assert.match(chatBody, /stream-ok/); assert.match(chatBody, /data: \[DONE\]/);
  await assertSingleSettledAttempt('chat-via-responses', '1');
  console.error('stage: responses-to-chat');
  const responses = await employee('/v1/responses', issued.key, { model: 'responses-via-chat', stream: true, input: 'hello' });
  const responsesBody = await responses.text(); assert.equal(responses.status, 200); assert.match(responsesBody, /response\.output_text\.delta/); assert.match(responsesBody, /response\.completed/);
  const responseRows = await assertSingleSettledAttempt('responses-via-chat', null);

  console.error('stage: selected-model-deny');
  const deniedModel = await employee('/v1/chat/completions', issued.key, { model: 'denied-via-responses', stream: true, messages: [{ role: 'user', content: 'deny' }] });
  assert.equal(deniedModel.status, 403); await deniedModel.body.cancel(); assert.equal(calls, 2, 'Denied model reached upstream');
  assert.equal((await admin('/usage/requests?model_id=denied-via-responses&limit=100')).items.length, 0, 'Denied model created a parent request');

  console.error('stage: selected-protocol-deny');
  const narrowed = await admin(`/keys/${issued.id}/policy`, 'PUT', {
    expected_revision: issued.policy.revision,
    protocol_mode: 'selected', protocols: ['openai-chat'],
    model_mode: 'selected', models: selectedModels,
  });
  assert.equal(narrowed.revision, issued.policy.revision + 1);
  const deniedProtocol = await employee('/v1/responses', issued.key, { model: 'responses-via-chat', stream: true, input: 'deny' });
  assert.equal(deniedProtocol.status, 403); await deniedProtocol.body.cancel(); assert.equal(calls, 2, 'Denied protocol reached upstream');
  assert.deepEqual((await admin('/usage/requests?model_id=responses-via-chat&limit=100')).items.map(item => item.id), responseRows.map(item => item.id), 'Denied protocol changed the request ledger');

  console.error('stage: cancel');
  const abort = new AbortController();
  const pending = await employee('/v1/chat/completions', issued.key, { model: 'cancel-via-responses', stream: true, messages: [{ role: 'user', content: 'cancel' }] }, abort.signal);
  await pending.body.getReader().read(); abort.abort();
  for (let attempt = 0; attempt < 50 && !cancelled; attempt++) await delay(20);
  assert.ok(cancelled, 'Client cancellation did not close the single upstream stream');
  assert.equal(calls, 3, 'Cross-protocol streams replayed or dispatched extra upstream calls');
  console.log('PASS: real process key-selected Chat<->Responses SSE, model/protocol denial, terminal conversion, single dispatch, credential isolation and cancellation');
} finally {
  await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir())); assert.ok(path.basename(resolved).startsWith('cpac-protocol-stream-'));
  await rm(resolved, { recursive: true, force: true });
}
