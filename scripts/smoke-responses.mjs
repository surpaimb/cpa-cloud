// Independent Responses acceptance: real CPA process, synthetic local upstream.
// Never accesses existing user data, provider credentials, or public model APIs.
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-responses-'));
const password = randomBytes(24).toString('hex');
const upstreamKey = `synthetic-upstream-${randomUUID()}`;
const privateError = `synthetic-private-error-${randomUUID()}`;
const prompt = `synthetic-prompt-${randomUUID()}`;
const toolOutput = `synthetic-tool-result-${randomUUID()}`;
const tools = [{ type: 'function', name: 'inventory_count', description: 'Synthetic inventory count',
  parameters: { type: 'object', properties: { sku: { type: 'string' } }, required: ['sku'], additionalProperties: false }, strict: true }];
const call = { type: 'function_call', id: 'fc_synthetic', call_id: 'call_synthetic', name: 'inventory_count', arguments: '{"sku":"test"}', status: 'completed' };
let scenario = 'tool', child, origin, cookie = '', csrf = '', logs = '', calls = 0, upstreamValid = true, issued;
const requests = [];
const finalResponse = output => ({ id: 'resp_synthetic', object: 'response', status: 'completed', model: 'native-model', output });

const mock = http.createServer(async (req, res) => {
  try {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const payload = JSON.parse(Buffer.concat(chunks));
    calls++;
    requests.push(payload);
    upstreamValid &&= req.method === 'POST' && req.url === '/v1/responses' && payload.model === 'native-model'
      && req.headers.authorization === `Bearer ${upstreamKey}` && !req.headers.cookie && !req.headers['x-api-key'];
    if (scenario === 'unauthorized' || scenario === 'limited') {
      res.writeHead(scenario === 'limited' ? 429 : 401, { 'Content-Type': 'application/json', 'Retry-After': '2' });
      res.end(JSON.stringify({ error: { message: privateError + upstreamKey } }));
      return;
    }
    if (scenario === 'invalid-json') {
      res.writeHead(200, { 'Content-Type': 'application/json' }); res.end('{invalid'); return;
    }
    const completed = finalResponse(scenario === 'tool' ? [call] : [
      { type: 'message', id: 'msg_synthetic', role: 'assistant', status: 'completed', content: [{ type: 'output_text', text: toolOutput, annotations: [] }] },
    ]);
    if (!payload.stream) {
      res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(completed)); return;
    }
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    // The event spans writes, has CRLF delimiters and multiple data lines.
    const created = 'event: response.created\r\ndata: {"type":"response.created",\r\ndata: "response":{"id":"resp_synthetic","object":"response","status":"in_progress"}}\r\n\r\n';
    res.write(created.slice(0, 39)); await delay(5); res.write(created.slice(39));
    if (scenario === 'truncated') { res.end(); return; }
    if (scenario === 'failed' || scenario === 'incomplete') {
      const type = `response.${scenario}`;
      res.end(`event: ${type}\ndata: ${JSON.stringify({ type, response: { id: 'resp_synthetic', object: 'response', status: scenario,
        error: { message: privateError + upstreamKey }, incomplete_details: { reason: privateError } } })}\n\n`); return;
    }
    for (const event of [
      { type: 'response.output_item.added', output_index: 0, item: { ...call, arguments: '', status: 'in_progress' } },
      { type: 'response.function_call_arguments.delta', item_id: call.id, output_index: 0, delta: '{"sku":' },
      { type: 'response.function_call_arguments.delta', item_id: call.id, output_index: 0, delta: '"test"}' },
      { type: 'response.function_call_arguments.done', item_id: call.id, output_index: 0, arguments: call.arguments },
      { type: 'response.output_item.done', output_index: 0, item: call },
      { type: 'response.completed', response: completed },
    ]) {
      const frame = `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
      res.write(frame.slice(0, 23)); await delay(2); res.write(frame.slice(23));
    }
    res.end();
  } catch { if (!res.headersSent) res.writeHead(500); res.end(); upstreamValid = false; }
});

function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { logs += chunk.toString(); if (logs.length > 1 << 20) proc.kill(); });
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
  let launchError; child.on('error', error => { launchError = error; });
  for (let i = 0; i < 100; i++) {
    if (launchError || child.exitCode !== null) throw new Error('Service launch failed');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* Await readiness. */ }
    await delay(100);
  }
  throw new Error('Readiness timeout');
}
async function admin(route, method = 'GET', body) {
  const response = await fetch(origin + '/admin/api/v1' + route, { method,
    headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000) });
  assert.ok(response.ok, `Admin ${method} ${route} HTTP ${response.status}`);
  const setCookie = response.headers.get('set-cookie'); if (setCookie) cookie = setCookie.split(';')[0];
  return response.status === 204 ? {} : response.json();
}
async function request(extra = {}, key = issued.key) {
  return fetch(origin + '/v1/responses', { method: 'POST',
    headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', Cookie: 'untrusted=must-not-forward' },
    body: JSON.stringify({ model: 'public-model', input: prompt, instructions: 'Synthetic instructions', tools, stream: false, ...extra }),
    signal: AbortSignal.timeout(5000) });
}
function events(text) {
  return text.replaceAll('\r\n', '\n').split('\n\n').map(block => block.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n'))
    .filter(Boolean).map(data => JSON.parse(data));
}
function assertRedacted(text) {
  assert.ok(![privateError, upstreamKey].some(secret => text.includes(secret)), 'Upstream error leaked');
}

try {
  mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
  const init = launch(['--init']); const initialized = once(init, 'exit'); init.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0, 'Initialization failed');
  await start(); csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const upstream = await admin('/upstreams', 'POST', { name: 'Responses acceptance', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}/v1`, api_key: upstreamKey });
  await admin('/models', 'POST', { id: 'public-model', upstream_id: upstream.id, upstream_model: 'native-model' });
  let employee = await admin('/employees', 'POST', { name: 'Responses acceptance' });
  issued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'test', operation_id: randomUUID() });
  const first = await request(); assert.equal(first.status, 200);
  const output = await first.json(); assert.deepEqual(output.output, [call]); assert.ok(!('usage' in output), 'Unknown usage was invented');
  assert.deepEqual(requests[0].tools, tools); assert.equal(requests[0].instructions, 'Synthetic instructions');
  scenario = 'result';
  const history = [{ role: 'user', content: prompt }, ...output.output, { type: 'function_call_output', call_id: call.call_id, output: toolOutput }];
  const followup = await request({ input: history }); assert.equal(followup.status, 200); assert.equal((await followup.json()).output[0].content[0].text, toolOutput);
  assert.deepEqual(requests.at(-1).input, history, 'Tool-result history changed');
  scenario = 'tool';
  const stream = await request({ stream: true }); assert.equal(stream.status, 200);
  const streamed = events(await stream.text());
  assert.equal(streamed.filter(event => event.type === 'response.completed').length, 1);
  assert.equal(streamed.filter(event => event.type === 'response.function_call_arguments.delta').map(event => event.delta).join(''), call.arguments);
  assert.deepEqual(streamed.at(-1).response.output, [call]);
  for (scenario of ['failed', 'incomplete', 'truncated']) {
    const response = await request({ stream: true }); const text = await response.text(); assertRedacted(text);
    assert.ok(!text.includes('response.completed'), `${scenario} became successful completion`);
    assert.ok(response.status >= 400 || events(text).some(event => event.type === 'error' || event.type === 'response.failed' || event.type === 'response.incomplete' || event.error), `${scenario} did not communicate failure`);
  }
  for (scenario of ['unauthorized', 'limited', 'invalid-json']) {
    const response = await request(); assert.ok(response.status >= 400, `${scenario} not rejected`); assertRedacted(await response.text());
  }
  scenario = 'tool';
  const beforeInvalid = calls;
  const invalid = await request({ stream: null }); assert.equal(invalid.status, 400); await invalid.text(); assert.equal(calls, beforeInvalid);
  employee = await admin(`/employees/${employee.id}/model-policy`, 'PUT', { expected_revision: employee.revision, mode: 'selected', models: [] });
  const forbidden = await request(); assert.equal(forbidden.status, 403); await forbidden.text(); assert.equal(calls, beforeInvalid);
  await admin(`/employees/${employee.id}/model-policy`, 'PUT', { expected_revision: employee.revision, mode: 'all', models: [] });
  await stop(); await start();
  const restarted = await request(); assert.equal(restarted.status, 200); await restarted.text();
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  await admin(`/keys/${issued.id}/revoke`, 'POST', {});
  const beforeRevocation = calls;
  const revoked = await request(); assert.equal(revoked.status, 401); await revoked.text(); assert.equal(calls, beforeRevocation);
  await stop(); await start();
  const persisted = await request(); assert.equal(persisted.status, 401); await persisted.text(); assert.equal(calls, beforeRevocation);
  await stop();
  assert.ok(upstreamValid, 'Upstream URL/model/credential/header isolation failed');
  assert.ok(![password, upstreamKey, issued.key, prompt, toolOutput, privateError].some(value => logs.includes(value)), 'Sensitive request material in logs');
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const bytes = await readFile(path.join(directory, entry.name));
    assert.ok(![upstreamKey, issued.key, prompt, toolOutput].some(value => bytes.includes(Buffer.from(value))), 'Sensitive request material persisted as plaintext');
  }
  console.log('PASS: Responses function-call/result round trip, JSON/SSE framing, terminal failures, redaction, model policy, credential isolation, restart and revocation');
} finally {
  await stop(); mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-responses-')) throw new Error('Unexpected cleanup path');
  await rm(resolved, { recursive: true, force: true });
}
