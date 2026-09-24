// Real CPA Cloud process acceptance for explicit non-streaming wire conversion.
// Uses only synthetic loopback services and temporary client homes.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const argv = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const name = process.argv[index];
  const value = process.argv[index + 1];
  assert.ok(name?.startsWith('--') && value, `Invalid argument near ${name ?? '<end>'}`);
  argv.set(name.slice(2), name === '--source-commit' ? value : path.resolve(value));
}
for (const name of ['server', 'codex', 'claude', 'gemini-entry']) assert.ok(argv.has(name), `--${name} is required`);
assert.match(argv.get('source-commit') ?? '', /^[0-9a-f]{40}$/, '--source-commit must be a full SHA');

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-protocol-routes-'));
const dataDir = path.join(root, 'data');
const workspace = path.join(root, 'workspace');
const clientRoot = path.join(root, 'clients');
const password = randomBytes(24).toString('hex');
const upstreamSecrets = {
  openai: `upstream-openai-${randomUUID()}`,
  anthropic: `upstream-anthropic-${randomUUID()}`,
  gemini: `upstream-gemini-${randomUUID()}`,
};
const textMarker = 'SYNTHETIC_ROUTE_TEXT_OK';
const finalMarker = 'SYNTHETIC_ROUTE_TOOL_OK';
const toolResult = 'SYNTHETIC_TOOL_RESULT';
const calls = [];
const captures = [];
const errors = [];
let service;
let origin;
let employeeKey;
let cookie = '';
let csrf = '';
let logs = '';
const evidenceOnly = process.env.CPAC_EVIDENCE_ONLY === '1';

const routes = [
  { id: 'chat-to-responses', client: 'chat', wire: 'responses', wireName: 'openai-responses', provider: 'openai', path: '/v1/responses' },
  { id: 'responses-to-chat', client: 'responses', wire: 'chat', wireName: 'openai-chat', provider: 'openai', path: '/v1/chat/completions' },
  { id: 'messages-to-responses', client: 'messages', wire: 'responses', wireName: 'openai-responses', provider: 'openai', path: '/v1/responses' },
  { id: 'gemini-to-responses', client: 'gemini', wire: 'responses', wireName: 'openai-responses', provider: 'openai', path: '/v1/responses' },
  { id: 'responses-to-messages', client: 'responses', wire: 'messages', wireName: 'anthropic-messages', provider: 'anthropic', path: '/v1/messages' },
  { id: 'responses-to-gemini', client: 'responses', wire: 'gemini', wireName: 'gemini-generate-content', provider: 'gemini', path: '/v1beta/models/up-responses-to-gemini:generateContent' },
];

function json(res, status, value) {
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(JSON.stringify(value));
}

function responseFor(wire, model, phase) {
  const marker = phase === 'result' ? finalMarker : textMarker;
  const usage = phase === 'tool' ? {} : wire === 'chat' ?
    { usage: { prompt_tokens: 4, completion_tokens: 2, total_tokens: 6 } } :
    wire === 'messages' ? { usage: { input_tokens: 4, output_tokens: 2 } } :
    wire === 'gemini' ? { usageMetadata: { promptTokenCount: 4, candidatesTokenCount: 2, totalTokenCount: 6 } } :
    { usage: { input_tokens: 4, output_tokens: 2, total_tokens: 6 } };
  if (wire === 'responses') return { id: 'resp_route', object: 'response', created_at: 1, model, status: 'completed',
    output: phase === 'tool' ? [{ id: 'fc_route', type: 'function_call', status: 'completed', call_id: 'call_route',
      name: 'echo', arguments: '{"value":"synthetic"}' }] : [{ id: 'msg_route', type: 'message', status: 'completed',
      role: 'assistant', content: [{ type: 'output_text', text: marker, annotations: [] }] }], ...usage };
  if (wire === 'chat') return { id: 'chat_route', object: 'chat.completion', created: 1, model,
    choices: [{ index: 0, finish_reason: phase === 'tool' ? 'tool_calls' : 'stop', message: phase === 'tool' ?
      { role: 'assistant', content: null, tool_calls: [{ id: 'call_route', type: 'function', function: {
        name: 'echo', arguments: '{"value":"synthetic"}' } }] } : { role: 'assistant', content: marker } }], ...usage };
  if (wire === 'messages') return { id: 'msg_route', type: 'message', role: 'assistant', model,
    content: phase === 'tool' ? [{ type: 'tool_use', id: 'call_route', name: 'echo', input: { value: 'synthetic' } }] :
      [{ type: 'text', text: marker }], stop_reason: phase === 'tool' ? 'tool_use' : 'end_turn', stop_sequence: null, ...usage };
  return { responseId: 'gemini_route', modelVersion: model, candidates: [{ index: 0, finishReason: 'STOP',
    content: { role: 'model', parts: phase === 'tool' ? [{ functionCall: { name: 'echo',
      args: { value: 'synthetic' } } }] :
      [{ text: marker }] } }], ...usage };
}

const upstream = http.createServer(async (req, res) => {
  try {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const bytes = Buffer.concat(chunks);
    const rawBody = bytes.toString('utf8');
    assert.ok(employeeKey, 'Employee credential was not initialized');
    assert.equal(req.url.includes(employeeKey), false, 'Employee credential reached upstream URL');
    assert.equal(Object.values(req.headers).some(value => (Array.isArray(value) ? value : [value])
      .some(item => String(item ?? '').includes(employeeKey))), false, 'Employee credential reached upstream headers');
    assert.equal(rawBody.includes(employeeKey), false, 'Employee credential reached upstream body');
    const body = bytes.length ? JSON.parse(bytes) : undefined;
    const serialized = JSON.stringify(body ?? {});
    const model = body?.model ?? req.url.match(/\/models\/([^:]+):/)?.[1];
    const route = routes.find(item => `up-${item.id}` === model);
    assert.ok(route, `Unknown synthetic model ${model}`);
    assert.equal(req.url.split('?')[0], route.path);
    if (route.provider === 'openai') assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.openai}`);
    if (route.provider === 'anthropic') assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.anthropic}`);
    if (route.provider === 'gemini') assert.equal(req.headers['x-goog-api-key'], upstreamSecrets.gemini);
    const phase = serialized.includes(toolResult) || serialized.includes('function_call_output') ||
      serialized.includes('tool_result') || serialized.includes('functionResponse') ? 'result' :
      serialized.includes('"tools"') ? 'tool' : 'text';
    calls.push({ route: route.id, path: req.url.split('?')[0], phase, fields: Object.keys(body ?? {}).sort() });
    json(res, 200, responseFor(route.wire, model, phase));
  } catch (error) {
    errors.push(error.stack ?? error.message);
    json(res, 500, { error: { message: 'synthetic upstream failure' } });
  }
});

const captureProxy = http.createServer(async (req, res) => {
  try {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const bytes = Buffer.concat(chunks);
    const body = bytes.length ? JSON.parse(bytes) : undefined;
    const capture = { path: req.url.split('?')[0], fields: Object.keys(body ?? {}).sort(),
      tool_types: [...new Set((body?.tools ?? []).map(tool => tool?.type ??
        (tool?.functionDeclarations ? 'functionDeclarations' : 'unknown')))].sort(),
      stream: body?.stream === true || req.url.includes(':streamGenerateContent') };
    captures.push(capture);
    const headers = {};
    for (const [name, value] of Object.entries(req.headers)) {
      if (!['host', 'connection', 'content-length'].includes(name) && value !== undefined) headers[name] = value;
    }
    const forwarded = await fetch(`${origin}${req.url}`, { method: req.method, headers,
      body: bytes.length ? bytes : undefined, signal: AbortSignal.timeout(20000) });
    const responseBytes = Buffer.from(await forwarded.arrayBuffer());
    capture.http_status = forwarded.status;
    try {
      const envelope = JSON.parse(responseBytes);
      const error = envelope?.error;
      if (error && typeof error === 'object') {
        if (typeof error.code === 'string' || typeof error.code === 'number') capture.error_code = error.code;
        if (typeof error.type === 'string') capture.error_type = error.type;
        if (typeof error.status === 'string') capture.error_status = error.status;
        if (error.message === 'This request cannot be represented by the selected route.') {
          capture.rejection_reason = 'route_not_representable';
        }
      }
    } catch { /* Non-JSON responses retain only their safe status. */ }
    res.writeHead(forwarded.status, { 'content-type': forwarded.headers.get('content-type') ?? 'application/json' });
    res.end(responseBytes);
  } catch (error) {
    errors.push(error.stack ?? error.message);
    json(res, 502, { error: { message: 'synthetic capture failure' } });
  }
});

function launch(extra) {
  const child = spawn(argv.get('server'), ['--data-dir', dataDir, ...extra], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > (1 << 20)) child.kill(); });
  return child;
}

async function startService() {
  const probe = http.createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  origin = `http://127.0.0.1:${port}`;
  service = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let attempt = 0; attempt < 100; attempt++) {
    try { if ((await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* wait */ }
    await delay(100);
  }
  throw new Error('service readiness timeout');
}

async function stopService() {
  if (!service || service.exitCode !== null) return;
  const exited = once(service, 'exit');
  service.stdin.end();
  await Promise.race([exited, delay(5000).then(() => service.kill())]);
}

async function admin(route, method = 'GET', body) {
  const response = await fetch(`${origin}/admin/api/v1${route}`, { method,
    headers: { Cookie: cookie, Origin: origin, 'content-type': 'application/json', 'x-csrf-token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10000) });
  const text = await response.text();
  assert.ok(response.ok, `Admin ${method} ${route}: ${response.status} ${text}`);
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  return JSON.parse(text);
}

async function usageRows(model) {
  const page = await admin(`/usage/requests?model_id=${encodeURIComponent(model)}&limit=100`);
  assert.equal(page.next_cursor, null);
  return page.items;
}

async function accountingRows(model) {
  const from = new Date(Date.now() - 3600000).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const to = new Date(Date.now() + 3600000).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const response = await fetch(`${origin}/admin/api/v1/usage/export?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}` +
    `&model_id=${encodeURIComponent(model)}&limit=100`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(10000) });
  const csv = await response.text();
  assert.equal(response.status, 200, `Accounting export failed: ${response.status} ${csv}`);
  const lines = csv.trim().split(/\r?\n/);
  if (lines.length < 2) return [];
  const header = lines[0].split(',');
  return lines.slice(1).filter(Boolean).map(line => Object.fromEntries(line.split(',').map((value, index) => [header[index], value])));
}

async function ledgerDelta(model, previous, previousAccounting, knownUsage, expectedAttempts = 1) {
  const prior = new Set(previous.map(item => item.id));
  let rows = [];
  for (let attempt = 0; attempt < 20; attempt++) {
    rows = (await usageRows(model)).filter(item => !prior.has(item.id));
    if (rows.length) break;
    await delay(100);
  }
  assert.equal(rows.length, 1, `${model} did not create exactly one parent request`);
  assert.equal(Number(rows[0].attempt_count), expectedAttempts);
  const details = (await admin(`/usage/requests/${rows[0].id}/attempts`)).items;
  assert.equal(details.length, expectedAttempts);
  let reliableUsage;
  if (expectedAttempts === 1) {
    const priorAttempts = new Set(previousAccounting.map(item => item.attempt_id));
    const reliable = (await accountingRows(model)).filter(item => !priorAttempts.has(item.attempt_id));
    assert.equal(reliable.length, 1, `${model} reliable accounting attempt mismatch`);
    const wire = routes.find(route => route.id === model).wire;
    // OpenAI and Gemini prompt/input totals may include cached input, so the
    // non-cached count stays unknown without the matching cache detail. The
    // Messages input counter is independently reliable.
    const expectedInput = knownUsage && wire === 'messages' ? '4' : '';
    const expectedCacheWrite = knownUsage && wire === 'gemini' ? '0' : '';
    const usageMatches = reliable[0].input_tokens === expectedInput && reliable[0].output_tokens === (knownUsage ? '2' : '') &&
      reliable[0].cache_read_tokens === '' && reliable[0].cache_write_tokens === expectedCacheWrite;
    assert.equal(reliable[0].protocol, routes.find(route => route.id === model).wireName === 'openai-chat' ?
      'openai-chat-completions' : routes.find(route => route.id === model).wireName);
    reliableUsage = usageMatches ? (knownUsage ? (expectedInput ? 'known_input_output' :
      expectedCacheWrite ? 'partial_known_output_zero_cache_write' : 'partial_known_output') : 'unknown') : 'MISMATCH';
  }
  return { parent_requests: rows.length, attempts: details.length, request_status: rows[0].status,
    ...(reliableUsage === undefined ? {} : { reliable_usage: reliableUsage }) };
}

function employeeRequest(protocol, model, phase, stream = false) {
  const tool = { name: 'echo', description: 'Synthetic echo', parameters: { type: 'object', properties: {
    value: { type: 'string' } }, required: ['value'] } };
  if (protocol === 'chat') {
    const messages = phase === 'result' ? [{ role: 'user', content: 'tool' }, { role: 'assistant', content: null,
      tool_calls: [{ id: 'call_route', type: 'function', function: { name: 'echo', arguments: '{"value":"synthetic"}' } }] },
    { role: 'tool', tool_call_id: 'call_route', content: toolResult }] : [{ role: 'user', content: phase }];
    return { url: `${origin}/v1/chat/completions`, headers: { authorization: `Bearer ${employeeKey}` },
      body: { model, messages, stream, ...(phase === 'text' ? {} : { tools: [{ type: 'function', function: tool }] }) } };
  }
  if (protocol === 'responses') {
    const input = phase === 'result' ? [{ type: 'message', role: 'user', content: [{ type: 'input_text', text: 'tool' }] },
      { type: 'function_call', id: 'fc_route', call_id: 'call_route', name: 'echo', arguments: '{"value":"synthetic"}',
        status: 'completed' }, { type: 'function_call_output', call_id: 'call_route',
        output: model === 'responses-to-gemini' ? JSON.stringify({ output: toolResult }) : toolResult }] : phase;
    return { url: `${origin}/v1/responses`, headers: { authorization: `Bearer ${employeeKey}` },
      body: { model, input, store: false, stream, max_output_tokens: 32,
        ...(phase === 'text' ? {} : { tools: [{ type: 'function', ...tool }] }) } };
  }
  if (protocol === 'messages') {
    const messages = phase === 'result' ? [{ role: 'user', content: 'tool' }, { role: 'assistant', content: [{ type: 'tool_use',
      id: 'call_route', name: 'echo', input: { value: 'synthetic' } }] }, { role: 'user', content: [{ type: 'tool_result',
      tool_use_id: 'call_route', content: toolResult }] }] : [{ role: 'user', content: phase }];
    return { url: `${origin}/v1/messages`, headers: { authorization: `Bearer ${employeeKey}`, 'anthropic-version': '2023-06-01' },
      body: { model, max_tokens: 32, messages, stream, ...(phase === 'text' ? {} : { tools: [{ ...tool, input_schema: tool.parameters, parameters: undefined }] }) } };
  }
  const contents = phase === 'result' ? [{ role: 'user', parts: [{ text: 'tool' }] }, { role: 'model', parts: [{ functionCall: {
    name: 'echo', args: { value: 'synthetic' } } }] }, { role: 'user', parts: [{ functionResponse: {
      name: 'echo', response: { output: toolResult } } }] }] : [{ role: 'user', parts: [{ text: phase }] }];
  return { url: `${origin}/v1beta/models/${model}:${stream ? 'streamGenerateContent?alt=sse' : 'generateContent'}`,
    headers: { 'x-goog-api-key': employeeKey }, body: { contents, ...(phase === 'text' ? {} : { tools: [{ functionDeclarations: [{
      name: tool.name, description: tool.description, parametersJsonSchema: tool.parameters }] }] }) } };
}

function assertClientShape(protocol, phase, body) {
  const marker = phase === 'result' ? finalMarker : textMarker;
  const serialized = JSON.stringify(body);
  if (phase === 'tool') {
    assert.ok(serialized.includes('echo'), `${protocol} tool response omitted echo`);
    assert.equal(body.usage ?? body.usageMetadata, undefined, `${protocol} fabricated missing usage`);
    return;
  }
  assert.ok(serialized.includes(marker), `${protocol} response omitted marker`);
  if (protocol === 'chat') assert.deepEqual([body.usage.prompt_tokens, body.usage.completion_tokens, body.usage.total_tokens], [4, 2, 6]);
  if (protocol === 'responses') assert.deepEqual([body.usage.input_tokens, body.usage.output_tokens, body.usage.total_tokens], [4, 2, 6]);
  if (protocol === 'messages') assert.deepEqual([body.usage.input_tokens, body.usage.output_tokens], [4, 2]);
  if (protocol === 'gemini') assert.deepEqual([body.usageMetadata.promptTokenCount, body.usageMetadata.candidatesTokenCount,
    body.usageMetadata.totalTokenCount], [4, 2, 6]);
}

async function sendEmployee(route, phase, stream = false) {
  const request = employeeRequest(route.client, route.id, phase, stream);
  const beforeRows = await usageRows(route.id);
  const beforeAccounting = await accountingRows(route.id);
  const beforeCalls = calls.length;
  const response = await fetch(request.url, { method: 'POST', headers: { ...request.headers, 'content-type': 'application/json' },
    body: JSON.stringify(request.body), signal: AbortSignal.timeout(15000) });
  const body = await response.json();
  if (stream) {
    assert.equal(response.status, 400, `${route.id} stream should be rejected`);
    assert.equal(calls.length, beforeCalls, `${route.id} stream reached upstream`);
    const after = await usageRows(route.id);
    const prior = new Set(beforeRows.map(item => item.id));
    const added = after.filter(item => !prior.has(item.id));
    assert.ok(added.every(item => Number(item.attempt_count) === 0), `${route.id} stream created an attempt`);
    const reliable = await accountingRows(route.id);
    assert.equal(reliable.length, beforeAccounting.length, `${route.id} stream created reliable accounting attempt`);
    return { status: 'PASS', http_status: response.status, upstream_calls: 0, attempts: 0, parent_requests: added.length,
      error_code: body.error?.code ?? body.error?.status };
  }
  assert.equal(response.status, 200, `${route.id}/${phase}: ${JSON.stringify(body)}`);
  assert.equal(calls.length, beforeCalls + 1);
  assert.deepEqual(calls.at(-1), { route: route.id, path: route.path, phase,
    fields: calls.at(-1).fields });
  assertClientShape(route.client, phase, body);
  const ledger = await ledgerDelta(route.id, beforeRows, beforeAccounting, phase !== 'tool');
  return { status: ledger.reliable_usage === 'MISMATCH' ? 'FAIL' : 'PASS', http_status: response.status,
    upstream_path: route.path, upstream_calls: 1, ...ledger,
    ...(ledger.reliable_usage === 'MISMATCH' ? { reason_code: 'reliable_usage_mismatch' } : {}) };
}

async function run(executable, args, env, timeout = 30000) {
  const child = spawn(executable, args, { cwd: workspace, env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > (1 << 20)) child.kill(); });
  child.stderr.on('data', chunk => { stderr += chunk; if (stderr.length > (1 << 20)) child.kill(); });
  const timer = setTimeout(() => child.kill(), timeout);
  const [code] = await once(child, 'exit');
  clearTimeout(timer);
  return { code, stdout, stderr };
}

function isolated(home, extra = {}) {
  return { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, ComSpec: process.env.ComSpec,
    PATH: process.env.PATH, PATHEXT: process.env.PATHEXT, HOME: home, USERPROFILE: home,
    APPDATA: path.join(home, 'AppData', 'Roaming'), LOCALAPPDATA: path.join(home, 'AppData', 'Local'),
    TEMP: path.join(home, 'tmp'), TMP: path.join(home, 'tmp'), NO_COLOR: '1', CI: '1', ...extra };
}

async function sha256(file) { return createHash('sha256').update(await readFile(file)).digest('hex'); }

async function rejectRealCLI(name, route, executable, clientArgs, env) {
  const beforeCalls = calls.length;
  const beforeRows = await usageRows(route.id);
  const beforeAccounting = await accountingRows(route.id);
  const beforeCaptures = captures.length;
  const result = await run(executable, clientArgs, env);
  assert.notEqual(result.code, 0, `${name} unexpectedly accepted cross-protocol streaming`);
  assert.equal(calls.length, beforeCalls, `${name} cross-wire request reached upstream`);
  const relevant = captures.slice(beforeCaptures).filter(item => item.path.includes(name === 'Codex CLI' ? '/responses' :
    name === 'Claude Code' ? '/messages' : ':streamGenerateContent'));
  assert.ok(relevant.length >= 1 && relevant.every(item => item.stream), `${name} stream shape was not captured`);
  assert.ok(relevant.every(item => item.http_status === 400 && item.rejection_reason === 'route_not_representable'),
    `${name} did not receive the cross-protocol route rejection`);
  if (name === 'Codex CLI') assert.ok(relevant.every(item => item.error_code === 'unsupported_feature'));
  if (name === 'Claude Code') assert.ok(relevant.every(item => item.error_type === 'invalid_request_error'));
  if (name === 'Gemini CLI') assert.ok(relevant.every(item => item.error_code === 400 && item.error_status === 'INVALID_ARGUMENT'));
  const afterRows = await usageRows(route.id);
  const prior = new Set(beforeRows.map(item => item.id));
  const added = afterRows.filter(item => !prior.has(item.id));
  assert.ok(added.every(item => Number(item.attempt_count) === 0), `${name} rejection created an attempt`);
  assert.equal((await accountingRows(route.id)).length, beforeAccounting.length, `${name} rejection created reliable attempt`);
  return { client: name, scenario: 'actual_cli_cross_wire_stream_reject', status: 'PASS', exit_code: result.code,
    request_shapes: relevant, parent_requests: added.length, attempts: 0, upstream_calls: 0 };
}

const results = [];
try {
  await mkdir(workspace, { recursive: true });
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  const upstreamOrigin = `http://127.0.0.1:${upstream.address().port}`;
  const init = launch(['--init']);
  init.stdin.end(`${password}\n`);
  assert.equal((await once(init, 'exit'))[0], 0);
  await startService();
  captureProxy.listen(0, '127.0.0.1');
  await once(captureProxy, 'listening');
  const captureOrigin = `http://127.0.0.1:${captureProxy.address().port}`;
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const upstreamIDs = {};
  for (const [kind, provider_kind, api_key] of [['openai', 'openai-compatible', upstreamSecrets.openai],
    ['anthropic', 'anthropic-api-key', upstreamSecrets.anthropic], ['gemini', 'gemini-api-key', upstreamSecrets.gemini]]) {
    upstreamIDs[kind] = (await admin('/upstreams', 'POST', { name: `route-${kind}`, provider_kind,
      endpoint: upstreamOrigin, api_key })).id;
  }
  for (const route of routes) await admin('/models', 'POST', { id: route.id, upstream_id: upstreamIDs[route.provider],
    upstream_model: `up-${route.id}`, wire_protocol: route.wireName });
  const employee = await admin('/employees', 'POST', { name: 'protocol route acceptance' });
  employeeKey = (await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'route smoke', operation_id: randomUUID() })).key;

  for (const route of routes) {
    const scenarios = {};
    scenarios.text = await sendEmployee(route, 'text');
    if (evidenceOnly) {
      const { DatabaseSync } = await import('node:sqlite');
      const database = new DatabaseSync(path.join(dataDir, 'cpa-cloud.db'), { readOnly: true });
      const stored = database.prepare(`SELECT c.protocol,c.effective_model,a.input_tokens,a.output_tokens,
        a.cache_read_tokens,a.cache_write_tokens FROM accounting_attempt_contexts c
        JOIN accounting_attempts a ON a.id=c.attempt_id JOIN accounting_requests r ON r.id=a.request_id
        WHERE r.model_id=? ORDER BY a.started_at DESC LIMIT 1`).get(route.id);
      database.close();
      const exported = (await accountingRows(route.id)).at(-1);
      console.log(JSON.stringify({ source_commit: argv.get('source-commit'), route: route.id,
        synthetic_upstream_response: responseFor(route.wire, `up-${route.id}`, 'text'),
        export_header: Object.keys(exported ?? {}), export_row: exported, stored }, null, 2));
      throw new Error('CPAC_EVIDENCE_COMPLETE');
    }
    scenarios.tool_call = await sendEmployee(route, 'tool');
    scenarios.tool_result = await sendEmployee(route, 'result');
    scenarios.stream_reject = await sendEmployee(route, 'text', true);
    results.push({ route: route.id, client_protocol: route.client, upstream_wire: route.wireName, scenarios });
  }

  const codexHome = path.join(clientRoot, 'codex');
  const claudeHome = path.join(clientRoot, 'claude');
  const geminiHome = path.join(clientRoot, 'gemini');
  for (const home of [codexHome, claudeHome, geminiHome]) await mkdir(path.join(home, 'AppData', 'Roaming'), { recursive: true });
  for (const home of [codexHome, claudeHome, geminiHome]) await mkdir(path.join(home, 'AppData', 'Local'), { recursive: true });
  for (const home of [codexHome, claudeHome, geminiHome]) await mkdir(path.join(home, 'tmp'), { recursive: true });
  await mkdir(path.join(codexHome, '.codex'), { recursive: true });
  await writeFile(path.join(codexHome, '.codex', 'config.toml'), `model="responses-to-chat"\nmodel_provider="cpacloud"\napproval_policy="never"\nsandbox_mode="read-only"\ncheck_for_update_on_startup=false\n[analytics]\nenabled=false\n[features]\napps=false\nplugins=false\nremote_plugin=false\nrecommended_plugins=false\nskill_search=false\n[model_providers.cpacloud]\nname="CPA synthetic"\nbase_url="${captureOrigin}/v1"\nenv_key="CPA_ROUTE_KEY"\nwire_api="responses"\nrequest_max_retries=0\nstream_max_retries=0\n`, 'utf8');
  await mkdir(path.join(geminiHome, '.gemini'), { recursive: true });
  await writeFile(path.join(geminiHome, '.gemini', 'settings.json'), JSON.stringify({ security: { auth: {
    selectedType: 'gemini-api-key' } }, telemetry: { enabled: false } }), 'utf8');

  results.push(await rejectRealCLI('Codex CLI', routes[1], argv.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only',
    '-a', 'never', '-m', 'responses-to-chat', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', 'synthetic'],
  isolated(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_ROUTE_KEY: employeeKey })));
  results.push(await rejectRealCLI('Claude Code', routes[2], argv.get('claude'), ['--bare', '--print', '--output-format', 'stream-json',
    '--verbose', '--include-partial-messages', '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk',
    '--permission-prompts', 'none', '--tools', '', '--model', 'messages-to-responses', 'synthetic'], isolated(claudeHome, {
      CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'), ANTHROPIC_API_KEY: employeeKey, ANTHROPIC_BASE_URL: captureOrigin,
      CLAUDE_CODE_MAX_RETRIES: '0', DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' })));
  results.push(await rejectRealCLI('Gemini CLI', routes[3], process.execPath, [argv.get('gemini-entry'), '--prompt', 'synthetic',
    '--model', 'gemini-to-responses', '--approval-mode', 'plan', '--output-format', 'stream-json', '--skip-trust'], isolated(geminiHome, {
      GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey, GOOGLE_GEMINI_BASE_URL: captureOrigin,
      GEMINI_TELEMETRY_ENABLED: '0' })));

  assert.deepEqual(errors, []);
  assert.equal(JSON.stringify(calls).includes(employeeKey), false);
  assert.equal(logs.includes(employeeKey), false);
  const status = results.some(result => result.status === 'FAIL' || Object.values(result.scenarios ?? {}).some(value => value.status === 'FAIL')) ? 'FAIL' : 'PASS';
  console.log(JSON.stringify({ status, boundary: 'explicit_wire_real_process_synthetic_upstream',
    source_commit: argv.get('source-commit'), platform: { os: os.platform(), release: os.release(), arch: os.arch() },
    server_sha256: await sha256(argv.get('server')), clients: {
      codex: { version: (await run(argv.get('codex'), ['--version'], isolated(codexHome))).stdout.trim(), sha256: await sha256(argv.get('codex')) },
      claude: { version: (await run(argv.get('claude'), ['--version'], isolated(claudeHome))).stdout.trim(), sha256: await sha256(argv.get('claude')) },
      gemini: { version: (await run(process.execPath, [argv.get('gemini-entry'), '--version'], isolated(geminiHome))).stdout.trim(),
        sha256: await sha256(argv.get('gemini-entry')) } }, results }, null, 2));
  if (status === 'FAIL') process.exitCode = 1;
} catch (error) {
  if (error?.message !== 'CPAC_EVIDENCE_COMPLETE') throw error;
} finally {
  await stopService();
  upstream.closeAllConnections();
  captureProxy.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  if (captureProxy.listening) await new Promise(resolve => captureProxy.close(resolve));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-protocol-routes-'));
  await rm(resolved, { recursive: true, force: true, maxRetries: 4, retryDelay: 250 });
  assert.equal((await readdir(os.tmpdir())).includes(path.basename(resolved)), false);
}
