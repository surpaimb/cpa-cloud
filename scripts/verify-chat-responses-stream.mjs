// Independent real-process acceptance for Chat Completions <-> Responses SSE.
// Uses only a temporary CPA Cloud data directory, synthetic keys and loopback servers.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const name = process.argv[index];
  const value = process.argv[index + 1];
  assert.ok(name?.startsWith('--') && value, `Invalid argument near ${name ?? '<end>'}`);
  args.set(name.slice(2), name === '--source-commit' ? value : path.resolve(value));
}
for (const name of ['server', 'source-commit']) assert.ok(args.has(name), `--${name} is required`);
assert.match(args.get('source-commit'), /^[0-9a-f]{40}$/);

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-chat-responses-stream-'));
const dataDir = path.join(root, 'data');
const clientRoot = path.join(root, 'codex-home');
const password = randomBytes(24).toString('hex');
const upstreamKey = `synthetic-upstream-${randomUUID()}`;
const promptMarker = `SYNTHETIC_STREAM_PROMPT_${randomUUID()}`;
const resultMarker = `SYNTHETIC_TOOL_RESULT_${randomUUID()}`;
const calls = [];
const codexCaptures = [];
const upstreamErrors = [];
const upstreamClosed = new Map();
const gates = new Map();
let employeeKey = '';
let service;
let serviceLogs = '';
let origin = '';
let cookie = '';
let csrf = '';
let captureOrigin = '';

const routes = [
  ['chat-text', 'chat', 'responses', 'openai-responses', 'text'],
  ['responses-text', 'responses', 'chat', 'openai-chat', 'text'],
  ['chat-tool', 'chat', 'responses', 'openai-responses', 'tool'],
  ['responses-tool', 'responses', 'chat', 'openai-chat', 'tool'],
  ['chat-result', 'chat', 'responses', 'openai-responses', 'result'],
  ['responses-result', 'responses', 'chat', 'openai-chat', 'result'],
  ['chat-malformed', 'chat', 'responses', 'openai-responses', 'malformed'],
  ['responses-malformed', 'responses', 'chat', 'openai-chat', 'malformed'],
  ['chat-duplicate', 'chat', 'responses', 'openai-responses', 'duplicate'],
  ['responses-duplicate', 'responses', 'chat', 'openai-chat', 'duplicate'],
  ['chat-missing', 'chat', 'responses', 'openai-responses', 'missing'],
  ['responses-missing', 'responses', 'chat', 'openai-chat', 'missing'],
  ['chat-hang', 'chat', 'responses', 'openai-responses', 'hang'],
  ['responses-hang', 'responses', 'chat', 'openai-chat', 'hang'],
  ['chat-cancel', 'chat', 'responses', 'openai-responses', 'cancel'],
  ['chat-slow-reader', 'chat', 'responses', 'openai-responses', 'slow-reader'],
  ['codex-cross-wire', 'responses', 'chat', 'openai-chat', 'codex'],
].map(([id, client, wire, wireName, scenario]) => ({ id, client, wire, wireName, scenario,
  upstreamModel: `up-${id}`, upstreamPath: wire === 'responses' ? '/v1/responses' : '/v1/chat/completions' }));

const routeByModel = new Map(routes.map(route => [route.upstreamModel, route]));

function sse(name, value) {
  return `${name ? `event: ${name}\n` : ''}data: ${typeof value === 'string' ? value : JSON.stringify(value)}\n\n`;
}

function responseTextEvents(model, marker = 'SYNTHETIC_TEXT_OK') {
  const response = { id: `resp-${model}`, object: 'response', created_at: 1, model, status: 'completed',
    output: [{ id: 'msg_1', type: 'message', status: 'completed', role: 'assistant',
      content: [{ type: 'output_text', text: marker, annotations: [] }] }],
    usage: { input_tokens: 7, output_tokens: 3, total_tokens: 10 } };
  return [
    ['response.created', { type: 'response.created', sequence_number: 0,
      response: { id: response.id, created_at: 1, model } }],
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: 1, output_index: 0,
      item: { id: 'msg_1', type: 'message', status: 'in_progress', role: 'assistant', content: [] } }],
    ['response.content_part.added', { type: 'response.content_part.added', sequence_number: 2, item_id: 'msg_1',
      output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } }],
    ['response.output_text.delta', { type: 'response.output_text.delta', sequence_number: 3, item_id: 'msg_1',
      output_index: 0, content_index: 0, delta: marker }],
    ['response.output_text.done', { type: 'response.output_text.done', sequence_number: 4, item_id: 'msg_1',
      output_index: 0, content_index: 0, text: marker }],
    ['response.content_part.done', { type: 'response.content_part.done', sequence_number: 5, item_id: 'msg_1',
      output_index: 0, content_index: 0, part: { type: 'output_text', text: marker, annotations: [] } }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: 6, output_index: 0,
      item: response.output[0] }],
    ['response.completed', { type: 'response.completed', sequence_number: 7, response }],
  ];
}

function responseToolEvents(model) {
  const item = { id: 'fc_1', type: 'function_call', status: 'completed', call_id: 'call_1', name: 'echo',
    arguments: '{"value":"synthetic"}' };
  return [
    ['response.created', { type: 'response.created', sequence_number: 0,
      response: { id: `resp-${model}`, created_at: 1, model } }],
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: 1, output_index: 0,
      item: { ...item, status: 'in_progress', arguments: '' } }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: 2,
      item_id: 'fc_1', output_index: 0, delta: '{"value":' }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: 3,
      item_id: 'fc_1', output_index: 0, delta: '"synthetic"}' }],
    ['response.function_call_arguments.done', { type: 'response.function_call_arguments.done', sequence_number: 4,
      item_id: 'fc_1', output_index: 0, arguments: item.arguments }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: 5, output_index: 0, item }],
    ['response.completed', { type: 'response.completed', sequence_number: 6, response: { id: `resp-${model}`,
      object: 'response', created_at: 1, model, status: 'completed', output: [item] } }],
  ];
}

function chatTextEvents(model, marker = 'SYNTHETIC_REVERSE_OK') {
  return [
    { id: `chat-${model}`, object: 'chat.completion.chunk', created: 1, model,
      choices: [{ index: 0, delta: { role: 'assistant', content: marker }, finish_reason: null }] },
    { id: `chat-${model}`, object: 'chat.completion.chunk', created: 1, model,
      choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] },
    { id: `chat-${model}`, object: 'chat.completion.chunk', created: 1, model, choices: [],
      usage: { prompt_tokens: 9, completion_tokens: 4, total_tokens: 13 } },
  ];
}

function chatToolEvents(model) {
  return [
    { id: `chat-${model}`, object: 'chat.completion.chunk', created: 1, model, choices: [{ index: 0,
      delta: { role: 'assistant', tool_calls: [{ index: 0, id: 'call_1', type: 'function',
        function: { name: 'echo', arguments: '{"value":' } }] }, finish_reason: null }] },
    { id: `chat-${model}`, object: 'chat.completion.chunk', created: 1, model, choices: [{ index: 0,
      delta: { tool_calls: [{ index: 0, function: { arguments: '"synthetic"}' } }] }, finish_reason: 'tool_calls' }] },
  ];
}

function assertCredentialIsolation(request, rawBody) {
  assert.ok(employeeKey);
  assert.equal(request.url.includes(employeeKey), false);
  assert.equal(Object.values(request.headers).some(value => String(value).includes(employeeKey)), false);
  assert.equal(rawBody.includes(employeeKey), false);
  assert.equal(request.headers.authorization, `Bearer ${upstreamKey}`);
}

async function writeEvents(response, events) {
  for (const [name, event] of events) response.write(sse(name, event));
}

const upstream = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const rawBody = Buffer.concat(chunks).toString('utf8');
    assertCredentialIsolation(request, rawBody);
    const body = JSON.parse(rawBody);
    const route = routeByModel.get(body.model);
    assert.ok(route, `Unknown model ${body.model}`);
    assert.equal(request.url.split('?')[0], route.upstreamPath);
    calls.push({ route: route.id, path: route.upstreamPath });
    if (route.scenario === 'result') {
      assert.ok(rawBody.includes(resultMarker), `${route.id} lost the tool result`);
      if (route.wire === 'responses') {
        assert.ok(body.input.some(item => item.type === 'function_call' && item.call_id === 'call_1'));
        assert.ok(body.input.some(item => item.type === 'function_call_output' && item.call_id === 'call_1'));
      } else {
        assert.ok(body.messages.some(item => item.role === 'assistant' && item.tool_calls?.[0]?.id === 'call_1'));
        assert.ok(body.messages.some(item => item.role === 'tool' && item.tool_call_id === 'call_1'));
      }
    }
    response.writeHead(200, { 'content-type': 'text/event-stream' });
    response.on('close', () => upstreamClosed.set(route.id, Date.now()));

    if (route.scenario === 'slow-reader') {
      const events = responseTextEvents(body.model, 'x'.repeat(180_000));
      for (const pair of events.slice(0, 3)) response.write(sse(...pair));
      let sequence = 3;
      while (!response.destroyed) {
        const event = { type: 'response.output_text.delta', sequence_number: sequence++, item_id: 'msg_1',
          output_index: 0, content_index: 0, delta: 'x'.repeat(180_000) };
        if (!response.write(sse('response.output_text.delta', event))) await once(response, 'drain');
      }
      return;
    }

    const responsesEvents = route.scenario === 'tool' ? responseToolEvents(body.model) :
      responseTextEvents(body.model, route.scenario === 'result' ? 'SYNTHETIC_RESULT_OK' : 'SYNTHETIC_TEXT_OK');
    const chatEvents = route.scenario === 'tool' ? chatToolEvents(body.model) :
      chatTextEvents(body.model, route.scenario === 'result' ? 'SYNTHETIC_RESULT_OK' : 'SYNTHETIC_REVERSE_OK');

    if (route.scenario === 'cancel') {
      await writeEvents(response, responsesEvents.slice(0, 4));
      return;
    }
    if (route.scenario === 'malformed') {
      if (route.wire === 'responses') await writeEvents(response, responsesEvents.slice(0, 4));
      else response.write(sse('', chatEvents[0]));
      response.end('data: {not-json}\n\n');
      return;
    }
    if (route.scenario === 'missing') {
      if (route.wire === 'responses') await writeEvents(response, responsesEvents.slice(0, 4));
      else response.write(sse('', chatEvents[0]));
      response.end();
      return;
    }
    if (route.wire === 'responses') await writeEvents(response, responsesEvents);
    else {
      for (const event of chatEvents) response.write(sse('', event));
      response.write(sse('', '[DONE]'));
    }
    if (route.scenario === 'duplicate') {
      if (route.wire === 'responses') response.end(sse('response.completed', responsesEvents.at(-1)[1]));
      else response.end(sse('', '[DONE]'));
      return;
    }
    if (route.scenario === 'hang') return;
    if (gates.has(route.id)) await gates.get(route.id).wait;
    response.end();
  } catch (error) {
    upstreamErrors.push(error.stack ?? String(error));
    if (!response.headersSent) response.writeHead(500, { 'content-type': 'application/json' });
    response.end('{"error":{"message":"synthetic failure"}}');
  }
});

const captureProxy = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const bytes = Buffer.concat(chunks);
    const body = bytes.length ? JSON.parse(bytes) : {};
    const capture = { path: request.url.split('?')[0], stream: body.stream === true,
      tool_types: [...new Set((body.tools ?? []).map(tool => tool?.type ?? 'unknown'))].sort() };
    const headers = {};
    for (const [name, value] of Object.entries(request.headers)) {
      if (!['host', 'connection', 'content-length'].includes(name) && value !== undefined) headers[name] = value;
    }
    const forwarded = await fetch(`${origin}${request.url}`, { method: request.method, headers,
      body: bytes.length ? bytes : undefined, signal: AbortSignal.timeout(10_000) });
    const responseBytes = Buffer.from(await forwarded.arrayBuffer());
    capture.http_status = forwarded.status;
    try { capture.error_code = JSON.parse(responseBytes).error?.code; } catch { /* retain safe status only */ }
    codexCaptures.push(capture);
    response.writeHead(forwarded.status, { 'content-type': forwarded.headers.get('content-type') ?? 'application/json' });
    response.end(responseBytes);
  } catch (error) {
    upstreamErrors.push(`capture proxy: ${error.stack ?? String(error)}`);
    response.writeHead(502, { 'content-type': 'application/json' });
    response.end('{"error":{"code":"capture_proxy_failure"}}');
  }
});

function launch(extra) {
  const child = spawn(args.get('server'), ['--data-dir', dataDir, ...extra],
    { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    serviceLogs += chunk;
    if (serviceLogs.length > (2 << 20)) child.kill();
  });
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
    try { if ((await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* retry */ }
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
    headers: { cookie, origin, 'content-type': 'application/json', 'x-csrf-token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10_000) });
  const text = await response.text();
  assert.ok(response.ok, `Admin ${method} ${route}: ${response.status} ${text}`);
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  return JSON.parse(text);
}

async function usageRows(model) {
  return (await admin(`/usage/requests?model_id=${encodeURIComponent(model)}&limit=100`)).items;
}

async function ledgerDelta(model, before, expectedStatus) {
  const prior = new Set(before.map(item => item.id));
  let added = [];
  for (let index = 0; index < 50; index++) {
    added = (await usageRows(model)).filter(item => !prior.has(item.id));
    if (added.length === 1 && added[0].status !== 'pending') break;
    await delay(100);
  }
  assert.equal(added.length, 1, `${model}: parent cardinality`);
  assert.equal(added[0].status, expectedStatus, `${model}: request status`);
  assert.equal(Number(added[0].attempt_count), 1, `${model}: attempt count`);
  const attempts = (await admin(`/usage/requests/${added[0].id}/attempts`)).items;
  assert.equal(attempts.length, 1, `${model}: attempt detail count`);
  assert.equal(attempts[0].status, expectedStatus, `${model}: attempt status`);
  return { parent_requests: 1, attempts: 1, request_status: added[0].status,
    usage: { input: attempts[0].input_tokens, output: attempts[0].output_tokens,
      cache_read: attempts[0].cache_read_tokens, cache_write: attempts[0].cache_write_tokens } };
}

function assertUsage(model, ledger, output) {
  assert.equal(ledger.usage.input, null, `${model}: unknown ordinary input was coerced`);
  assert.equal(ledger.usage.output, output, `${model}: raw output usage was not retained`);
  assert.equal(ledger.usage.cache_read, null, `${model}: unknown cache read was coerced`);
  assert.equal(ledger.usage.cache_write, null, `${model}: unknown cache write was coerced`);
}

function requestFor(route) {
  const tool = { name: 'echo', description: 'Synthetic echo', parameters: { type: 'object', properties: {
    value: { type: 'string' } }, required: ['value'] } };
  if (route.client === 'chat') {
    const messages = route.scenario === 'result' ? [
      { role: 'user', content: promptMarker },
      { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function',
        function: { name: 'echo', arguments: '{"value":"synthetic"}' } }] },
      { role: 'tool', tool_call_id: 'call_1', content: resultMarker },
    ] : [{ role: 'user', content: promptMarker }];
    return { url: `${origin}/v1/chat/completions`, body: { model: route.id, stream: true, messages,
      ...(route.scenario === 'tool' || route.scenario === 'result' ?
        { tools: [{ type: 'function', function: tool }] } : {}) } };
  }
  const input = route.scenario === 'result' ? [
    { type: 'message', role: 'user', content: [{ type: 'input_text', text: promptMarker }] },
    { type: 'function_call', id: 'fc_1', call_id: 'call_1', name: 'echo',
      arguments: '{"value":"synthetic"}', status: 'completed' },
    { type: 'function_call_output', call_id: 'call_1', output: resultMarker },
  ] : promptMarker;
  return { url: `${origin}/v1/responses`, body: { model: route.id, stream: true, store: false, input,
    ...(route.scenario === 'tool' || route.scenario === 'result' ? { tools: [{ type: 'function', ...tool }] } : {}) } };
}

async function readStream(response, onText) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let text = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    text += decoder.decode(value, { stream: true });
    if (onText) await onText(text);
  }
  return text + decoder.decode();
}

async function send(route, { gate = false, abort = false } = {}) {
  const before = await usageRows(route.id);
  const beforeCalls = calls.length;
  let release;
  if (gate) {
    let resolve;
    const wait = new Promise(done => { resolve = done; });
    release = resolve;
    gates.set(route.id, { wait });
  }
  const controller = new AbortController();
  const request = requestFor(route);
  const started = Date.now();
  const response = await fetch(request.url, { method: 'POST', headers: { authorization: `Bearer ${employeeKey}`,
    'content-type': 'application/json' }, body: JSON.stringify(request.body), signal: controller.signal });
  assert.equal(response.status, 200, `${route.id}: HTTP ${response.status}`);
  let firstSemanticAt = 0;
  let released = false;
  let observedBeforeUpstreamEOF = false;
  let text = '';
  try {
    text = await readStream(response, async current => {
      if (!firstSemanticAt && (current.includes('SYNTHETIC_') || current.includes('"arguments"'))) {
        firstSemanticAt = Date.now();
        if (gate && !released) {
          assert.equal(upstreamClosed.has(route.id), false, `${route.id}: upstream closed before first downstream event`);
          observedBeforeUpstreamEOF = true;
          released = true;
          release();
        }
        if (abort) controller.abort();
      }
    });
  } catch (error) {
    if (!abort || error.name !== 'AbortError') throw error;
  } finally {
    if (gate && !released) release();
    gates.delete(route.id);
  }
  assert.equal(calls.length, beforeCalls + 1, `${route.id}: upstream dispatch count`);
  if (gate) assert.ok(firstSemanticAt >= started && observedBeforeUpstreamEOF,
    `${route.id}: no event arrived before full upstream completion`);
  return { text, before, first_event_before_upstream_eof: gate && observedBeforeUpstreamEOF, calls: 1 };
}

function assertSuccess(route, text) {
  if (route.client === 'chat') {
    assert.ok(text.includes('data: [DONE]'));
    assert.equal((text.match(/data: \[DONE\]/g) ?? []).length, 1);
  } else {
    assert.ok(text.includes('event: response.completed'));
    assert.equal((text.match(/event: response\.completed/g) ?? []).length, 1);
  }
}

function assertNoSuccessTerminal(route, text) {
  if (route.client === 'chat') assert.equal(text.includes('data: [DONE]'), false);
  else assert.equal(text.includes('event: response.completed'), false);
}

async function runProcess(executable, processArgs, env, timeout = 30_000) {
  const child = spawn(executable, processArgs, { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > (1 << 20)) child.kill(); });
  child.stderr.on('data', chunk => { stderr += chunk; if (stderr.length > (1 << 20)) child.kill(); });
  const timer = setTimeout(() => child.kill(), timeout);
  const [code] = await once(child, 'exit');
  clearTimeout(timer);
  return { code, stdout, stderr };
}

async function sha256(file) {
  return createHash('sha256').update(await readFile(file)).digest('hex');
}

async function slowReader(route) {
  const before = await usageRows(route.id);
  const beforeCalls = calls.length;
  const body = JSON.stringify(requestFor(route).body);
  const url = new URL(origin);
  const socket = net.createConnection({ host: url.hostname, port: Number(url.port) });
  await once(socket, 'connect');
  socket.write(`POST /v1/chat/completions HTTP/1.1\r\nHost: ${url.host}\r\nAuthorization: Bearer ${employeeKey}\r\n` +
    `Content-Type: application/json\r\nContent-Length: ${Buffer.byteLength(body)}\r\nConnection: keep-alive\r\n\r\n${body}`);
  socket.pause();
  const started = Date.now();
  for (let index = 0; index < 400 && !upstreamClosed.has(route.id); index++) await delay(100);
  const elapsed = Date.now() - started;
  socket.destroy();
  assert.ok(upstreamClosed.has(route.id), `${route.id}: blocked downstream did not release upstream`);
  assert.ok(elapsed >= 25_000 && elapsed < 40_000, `${route.id}: write deadline elapsed ${elapsed}ms`);
  assert.equal(calls.length, beforeCalls + 1);
  return { ...(await ledgerDelta(route.id, before, 'cancelled')), upstream_released_ms: elapsed, upstream_calls: 1 };
}

async function actualCodex(route) {
  if (!args.has('codex')) return { status: 'SKIP', reason: 'codex_not_supplied' };
  const home = clientRoot;
  await mkdir(path.join(home, '.codex'), { recursive: true });
  await mkdir(path.join(home, 'AppData', 'Roaming'), { recursive: true });
  await mkdir(path.join(home, 'AppData', 'Local'), { recursive: true });
  await mkdir(path.join(home, 'tmp'), { recursive: true });
  await writeFile(path.join(home, '.codex', 'config.toml'), `model="${route.id}"\nmodel_provider="cpacloud"\n` +
    `approval_policy="never"\nsandbox_mode="read-only"\ncheck_for_update_on_startup=false\n` +
    `[analytics]\nenabled=false\n[features]\napps=false\nplugins=false\nremote_plugin=false\nrecommended_plugins=false\n` +
    `[model_providers.cpacloud]\nname="CPA synthetic"\nbase_url="${captureOrigin}/v1"\nenv_key="CPA_STREAM_KEY"\n` +
    `wire_api="responses"\nrequest_max_retries=0\nstream_max_retries=0\n`, 'utf8');
  const before = await usageRows(route.id);
  const beforeCalls = calls.length;
  const beforeCaptures = codexCaptures.length;
  const env = { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, ComSpec: process.env.ComSpec,
    PATH: process.env.PATH, PATHEXT: process.env.PATHEXT, HOME: home, USERPROFILE: home,
    APPDATA: path.join(home, 'AppData', 'Roaming'), LOCALAPPDATA: path.join(home, 'AppData', 'Local'),
    TEMP: path.join(home, 'tmp'), TMP: path.join(home, 'tmp'), CODEX_HOME: path.join(home, '.codex'),
    CPA_STREAM_KEY: employeeKey, NO_COLOR: '1', CI: '1' };
  const result = await runProcess(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', route.id, 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', 'Reply with one word.'], env);
  assert.notEqual(result.code, 0, 'Codex unexpectedly dispatched a representable request');
  assert.equal(calls.length, beforeCalls, 'Unsupported Codex request reached upstream');
  const captured = codexCaptures.slice(beforeCaptures).filter(item => item.path === '/v1/responses');
  assert.ok(captured.length >= 1, 'Codex did not reach the capture boundary');
  assert.ok(captured.every(item => item.stream && item.http_status === 400 && item.error_code === 'invalid_request_error'),
    `Codex rejection mismatch: ${JSON.stringify(captured)}`);
  const prior = new Set(before.map(item => item.id));
  const added = (await usageRows(route.id)).filter(item => !prior.has(item.id));
  assert.ok(added.every(item => Number(item.attempt_count) === 0));
  return { status: 'UNSUPPORTED', exit_code: result.code, parent_requests: added.length, attempts: 0, upstream_calls: 0,
    request_shapes: captured, reason: 'actual_codex_request_not_representable_by_chat_wire' };
}

const results = [];
try {
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  const init = launch(['--init']);
  init.stdin.end(`${password}\n`);
  assert.equal((await once(init, 'exit'))[0], 0);
  await startService();
  captureProxy.listen(0, '127.0.0.1');
  await once(captureProxy, 'listening');
  captureOrigin = `http://127.0.0.1:${captureProxy.address().port}`;
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const configured = await admin('/upstreams', 'POST', { name: 'Synthetic cross-stream', provider_kind: 'openai-compatible',
    endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: upstreamKey });
  for (const route of routes) await admin('/models', 'POST', { id: route.id, upstream_id: configured.id,
    upstream_model: route.upstreamModel, wire_protocol: route.wireName });
  const employee = await admin('/employees', 'POST', { name: 'Cross-stream acceptance' });
  employeeKey = (await admin(`/employees/${employee.id}/keys`, 'POST',
    { name: 'acceptance', operation_id: randomUUID() })).key;

  for (const id of ['chat-text', 'responses-text']) {
    const route = routes.find(item => item.id === id);
    const sent = await send(route, { gate: true });
    assertSuccess(route, sent.text);
    assert.ok(sent.text.includes(id === 'chat-text' ? 'SYNTHETIC_TEXT_OK' : 'SYNTHETIC_REVERSE_OK'));
    const ledger = await ledgerDelta(id, sent.before, 'succeeded');
    assertUsage(id, ledger, id === 'chat-text' ? '3' : '4');
    results.push({ scenario: id, status: 'PASS', first_event_before_upstream_eof: sent.first_event_before_upstream_eof,
      upstream_path: routes.find(item => item.id === id).upstreamPath, upstream_calls: 1, ...ledger,
      raw_usage: id === 'chat-text' ? { input_tokens: 7, output_tokens: 3, total_tokens: 10 } :
        { prompt_tokens: 9, completion_tokens: 4, total_tokens: 13 }, usage_unknown_not_zero: true });
  }

  for (const id of ['chat-tool', 'responses-tool']) {
    const route = routes.find(item => item.id === id);
    const sent = await send(route);
    assertSuccess(route, sent.text);
    assert.ok(sent.text.includes('{\\"value\\":') && sent.text.includes('\\"synthetic\\"}'), `${id}: incremental arguments lost`);
    const ledger = await ledgerDelta(id, sent.before, 'succeeded');
    assertUsage(id, ledger, null);
    results.push({ scenario: id, status: 'PASS', incremental_arguments: true,
      ...ledger, upstream_calls: 1, usage_unknown_not_zero: true });
  }

  for (const id of ['chat-result', 'responses-result']) {
    const route = routes.find(item => item.id === id);
    const sent = await send(route);
    assertSuccess(route, sent.text);
    assert.ok(sent.text.includes('SYNTHETIC_RESULT_OK'));
    results.push({ scenario: id, status: 'PASS', tool_result_on_next_request: true,
      ...(await ledgerDelta(id, sent.before, 'succeeded')), upstream_calls: 1 });
  }

  for (const suffix of ['malformed', 'duplicate', 'missing', 'hang']) {
    for (const prefix of ['chat', 'responses']) {
      const id = `${prefix}-${suffix}`;
      const route = routes.find(item => item.id === id);
      const sent = await send(route);
      assertNoSuccessTerminal(route, sent.text);
      const expected = suffix === 'missing' || suffix === 'hang' ? 'interrupted' : 'failed';
      results.push({ scenario: id, status: 'PASS', success_terminal_withheld: true,
        ...(await ledgerDelta(id, sent.before, expected)), upstream_calls: 1 });
    }
  }

  {
    const route = routes.find(item => item.id === 'chat-cancel');
    const sent = await send(route, { abort: true });
    for (let index = 0; index < 50 && !upstreamClosed.has(route.id); index++) await delay(100);
    assert.ok(upstreamClosed.has(route.id));
    results.push({ scenario: route.id, status: 'PASS', bounded_upstream_cancel: true,
      ...(await ledgerDelta(route.id, sent.before, 'cancelled')), upstream_calls: 1 });
  }

  results.push({ scenario: 'chat-slow-reader', status: 'PASS',
    ...(await slowReader(routes.find(item => item.id === 'chat-slow-reader'))) });
  results.push({ scenario: 'actual-codex-cross-wire', ...(await actualCodex(routes.find(item => item.id === 'codex-cross-wire'))) });

  assert.deepEqual(upstreamErrors, []);
  assert.equal(serviceLogs.includes(employeeKey), false, 'Employee key leaked into service logs');
  assert.equal(serviceLogs.includes(promptMarker), false, 'Prompt leaked into service logs');
  assert.equal(serviceLogs.includes(resultMarker), false, 'Tool result leaked into service logs');
  assert.equal(JSON.stringify(calls).includes(employeeKey), false);
  const version = args.has('codex') ? (await runProcess(args.get('codex'), ['--version'], process.env)).stdout.trim() : undefined;
  console.log(JSON.stringify({ status: 'PASS', boundary: 'chat_responses_cross_protocol_sse_real_process',
    source_commit: args.get('source-commit'), server_sha256: await sha256(args.get('server')),
    platform: { os: os.platform(), release: os.release(), arch: os.arch() },
    codex: args.has('codex') ? { version, sha256: await sha256(args.get('codex')) } : undefined,
    results }, null, 2));
} finally {
  await stopService();
  upstream.closeAllConnections();
  captureProxy.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  if (captureProxy.listening) await new Promise(resolve => captureProxy.close(resolve));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-chat-responses-stream-'));
  await rm(resolved, { recursive: true, force: true, maxRetries: 4, retryDelay: 250 });
  assert.equal((await readdir(os.tmpdir())).includes(path.basename(resolved)), false);
}
