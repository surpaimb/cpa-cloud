// Independent process acceptance with synthetic local providers only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-native-'));
const password = randomBytes(24).toString('hex');
const claudeSecret = `synthetic-claude-${randomUUID()}`;
const geminiSecret = `synthetic-gemini-${randomUUID()}`;
const privateError = `synthetic-error-${randomUUID()}`;
const prompt = `synthetic-prompt-${randomUUID()}`;
let child, origin, cookie = '', csrf = '', logs = '', calls = 0, fault = false, issued;
const upstreamErrors = [];
const received = [];
const tool = { name: 'lookup', description: 'Synthetic lookup', input_schema: { type: 'object', properties: { id: { type: 'string' } } } };
const geminiTool = { functionDeclarations: [{ name: 'lookup', parameters: tool.input_schema }] };
const mock = http.createServer(async (req, res) => {
  try {
    calls++;
    const isGemini = req.url.startsWith('/v1beta/');
    assert.equal(req.headers.authorization, isGemini ? undefined : `Bearer ${claudeSecret}`);
    assert.equal(req.headers['x-goog-api-key'], isGemini ? geminiSecret : undefined);
    for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
    if (req.method === 'GET') {
      const url = new URL(req.url, 'http://synthetic.invalid');
      res.setHeader('Content-Type', 'application/json');
      if (isGemini) {
        const second = url.searchParams.get('pageToken') === 'second';
        res.end(JSON.stringify({ models: [{ name: second ? 'models/native-gemini-b' : 'models/native-gemini', supportedGenerationMethods: ['generateContent'] }], ...(second ? {} : { nextPageToken: 'second' }) }));
      } else res.end(JSON.stringify({ data: [{ id: 'native-claude' }] }));
      return;
    }
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const payload = JSON.parse(Buffer.concat(chunks));
    received.push({ isGemini, payload });
    if (!isGemini) assert.equal(payload.model, 'native-claude');
    if (req.url === '/v1/messages/count_tokens') {
      res.setHeader('Content-Type', 'application/json'); res.end('{"input_tokens":7}'); return;
    }
    const stream = isGemini ? req.url.includes(':streamGenerateContent') : payload.stream;
    if (!stream) {
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify(isGemini
        ? { candidates: [{ content: { role: 'model', parts: [{ functionCall: { name: 'lookup', args: { id: '1' } } }] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 3, totalTokenCount: 10 } }
        : { id: 'synthetic-message', type: 'message', role: 'assistant', model: 'native-claude', content: [{ type: 'tool_use', id: 'synthetic-call', name: 'lookup', input: { id: '1' } }], stop_reason: 'tool_use', usage: { input_tokens: 7, output_tokens: 3 } }));
      return;
    }
    res.setHeader('Content-Type', 'text/event-stream');
    if (fault) {
      const error = { type: 'error', error: { type: 'api_error', message: privateError + claudeSecret + geminiSecret } };
      res.end((isGemini ? '' : 'event: error\n') + `data: ${JSON.stringify(error)}\n\n`); return;
    }
    const frames = isGemini ? [
      'data: {"candidates":[{"content":{"parts":[{"text":"synthetic"}]}}]}\n\n',
      'data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":10}}\n\n',
    ] : [
      'event: message_start\ndata: {"type":"message_start","message":{"id":"synthetic-message","type":"message","role":"assistant","content":[],"model":"native-claude","usage":{"input_tokens":7,"output_tokens":0}}}\n\n',
      'event: content_block_start\ndata: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n\n',
      'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"synthetic"}}\n\n',
      'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n',
      'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}\n\n',
      'event: message_stop\ndata: {"type":"message_stop"}\n\n',
    ];
    for (const frame of frames) { res.write(frame.slice(0, 11)); await delay(2); res.write(frame.slice(11)); }
    res.end();
  } catch (error) { upstreamErrors.push(error.message); if (!res.headersSent) res.writeHead(500); res.end(); }
});
function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = once(child, 'exit'); child.stdin.end();
    const timer = setTimeout(() => child.kill(), 5000);
    try { await ended; } finally { clearTimeout(timer); }
  }
}
async function start() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  origin = `http://127.0.0.1:${port}`; cookie = ''; csrf = '';
  child = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  let error; child.on('error', value => { error = value; });
  for (let attempt = 0; attempt < 100; attempt++) {
    if (error || child.exitCode !== null) throw new Error('Service startup failed');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* Wait for readiness. */ }
    await delay(100);
  }
  throw new Error('Readiness timeout');
}
async function admin(route, method = 'GET', body) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method,
    headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
  assert.ok(response.ok, `Admin ${route}: HTTP ${response.status}`);
  const setCookie = response.headers.get('set-cookie'); if (setCookie) cookie = setCookie.split(';')[0];
  return response.json();
}
async function request(kind, stream = false, extra = {}, key = issued.key) {
  const route = kind === 'claude' ? '/v1/messages' : `/v1beta/models/public-gemini:${stream ? 'streamGenerateContent' : 'generateContent'}`;
  const payload = kind === 'claude'
    ? { model: 'public-claude', max_tokens: 64, messages: [{ role: 'user', content: prompt }], tools: [tool], stream, ...extra }
    : { contents: [{ role: 'user', parts: [{ text: prompt }] }], tools: [geminiTool], ...extra };
  return fetch(origin + route, { method: 'POST', headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', 'anthropic-version': '2023-06-01', Cookie: 'synthetic=must-not-forward' }, body: JSON.stringify(payload), signal: AbortSignal.timeout(5000) });
}
try {
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  const init = launch(['--init']); const initialized = once(init, 'exit'); init.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0);
  await start(); csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  for (const [kind, provider_kind, api_key] of [['claude', 'anthropic-api-key', claudeSecret], ['gemini', 'gemini-api-key', geminiSecret]]) {
    const upstream = await admin('/upstreams', 'POST', { name: kind, provider_kind, endpoint: `http://127.0.0.1:${mock.address().port}`, api_key });
    const catalog = await admin(`/upstreams/${upstream.id}/discover-models`, 'POST');
    assert.deepEqual(catalog.items.map(item => item.id), kind === 'claude' ? ['native-claude'] : ['native-gemini', 'native-gemini-b']);
    await admin('/models', 'POST', { id: `public-${kind}`, upstream_id: upstream.id, upstream_model: `native-${kind}` });
  }
  let employee = await admin('/employees', 'POST', { name: 'native acceptance' });
  issued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'test', operation_id: randomUUID() });
  for (const kind of ['claude', 'gemini']) {
    const response = await request(kind); assert.equal(response.status, 200);
    const body = await response.json();
    if (kind === 'claude') {
      assert.equal(body.content[0].type, 'tool_use');
      assert.deepEqual(received.at(-1).payload.tools, [tool]);
      const followup = await request(kind, false, { messages: [{ role: 'assistant', content: body.content }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'synthetic-call', content: 'synthetic-result' }] }] });
      assert.equal(followup.status, 200); await followup.text();
      assert.equal(received.at(-1).payload.messages[1].content[0].tool_use_id, 'synthetic-call');
    } else {
      assert.equal(body.candidates[0].content.parts[0].functionCall.name, 'lookup');
      assert.deepEqual(received.at(-1).payload.tools, [geminiTool]);
      const followup = await request(kind, false, { contents: [{ role: 'model', parts: body.candidates[0].content.parts }, { role: 'user', parts: [{ functionResponse: { name: 'lookup', response: { result: 'synthetic-result' } } }] }] });
      assert.equal(followup.status, 200); await followup.text();
      assert.equal(received.at(-1).payload.contents[1].parts[0].functionResponse.name, 'lookup');
    }
    const stream = await request(kind, true); assert.equal(stream.status, 200);
    const text = await stream.text(); assert.ok(text.includes(kind === 'claude' ? 'message_stop' : 'STOP'));
    fault = true;
    const failed = await request(kind, true); const errorBody = await failed.text();
    assert.ok(![privateError, claudeSecret, geminiSecret].some(value => errorBody.includes(value)), 'Upstream error leaked');
    assert.ok(!errorBody.includes(kind === 'claude' ? 'message_stop' : 'STOP'), 'Error became success');
    fault = false;
  }
  employee = await admin(`/employees/${employee.id}/model-policy`, 'PUT', { expected_revision: employee.revision, mode: 'selected', models: [] });
  const beforeDenied = calls;
  for (const kind of ['claude', 'gemini']) { const denied = await request(kind); assert.equal(denied.status, 403); await denied.text(); }
  assert.equal(calls, beforeDenied);
  await admin(`/employees/${employee.id}/model-policy`, 'PUT', { expected_revision: employee.revision, mode: 'all', models: [] });
  await stop(); await start();
  for (const kind of ['claude', 'gemini']) { const response = await request(kind); assert.equal(response.status, 200); await response.text(); }
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  await admin(`/keys/${issued.id}/revoke`, 'POST', {});
  await stop(); await start();
  const beforeRevoked = calls;
  for (const kind of ['claude', 'gemini']) { const response = await request(kind); assert.equal(response.status, 401); await response.text(); }
  assert.equal(calls, beforeRevoked);
  await stop();
  assert.deepEqual(upstreamErrors, []);
  const secrets = [password, claudeSecret, geminiSecret, issued.key, prompt, privateError];
  assert.ok(!secrets.some(value => logs.includes(value)), 'Sensitive log content');
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const bytes = await readFile(path.join(directory, entry.name));
    assert.ok(!secrets.some(value => bytes.includes(Buffer.from(value))), 'Plaintext secret or content persisted');
  }
  console.log('PASS: native Claude/Gemini model discovery, paginated catalog, tools/results, SSE/error redaction, employee policy, credential isolation, restart/revocation');
} finally {
  await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-native-')) throw new Error('Unexpected cleanup path');
  await rm(resolved, { recursive: true, force: true });
}
