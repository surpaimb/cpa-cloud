// Independent real-process acceptance for Anthropic Messages <-> OpenAI Responses SSE.
// Uses only temporary CPA Cloud state, synthetic keys and loopback upstreams.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const name = process.argv[index], value = process.argv[index + 1];
  assert.ok(name?.startsWith('--') && value, `Invalid argument near ${name ?? '<end>'}`);
  args.set(name.slice(2), name === '--source-commit' ? value : path.resolve(value));
}
for (const name of ['server', 'source-commit']) assert.ok(args.has(name), `--${name} is required`);
assert.match(args.get('source-commit'), /^[0-9a-f]{40}$/);

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-messages-responses-stream-'));
const dataDir = path.join(root, 'data');
const password = randomBytes(24).toString('hex');
const openAISecret = `synthetic-openai-${randomUUID()}`;
const anthropicSecret = `synthetic-anthropic-${randomUUID()}`;
const promptMarker = `SYNTHETIC_MESSAGES_PROMPT_${randomUUID()}`;
const resultMarker = `SYNTHETIC_MESSAGES_RESULT_${randomUUID()}`;
const calls = [];
const claudeCaptures = [];
const fixtureErrors = [];
const upstreamClosed = new Map();
const controls = new Map();
let service, origin = '', captureOrigin = '', employeeKey = '', cookie = '', csrf = '', serviceLogs = '';

const routeSpecs = [
  ['m2r-early', 'messages', 'responses', 'early'],
  ['m2r-inprogress', 'messages', 'responses', 'inprogress'],
  ['m2r-delayed', 'messages', 'responses', 'delayed'],
  ['m2r-tool', 'messages', 'responses', 'tool'],
  ['m2r-result', 'messages', 'responses', 'result'],
  ['m2r-incomplete', 'messages', 'responses', 'incomplete'],
  ['m2r-failed', 'messages', 'responses', 'failed'],
  ['m2r-http-error', 'messages', 'responses', 'http-error'],
  ['m2r-unknown', 'messages', 'responses', 'unknown'],
  ['m2r-known-terminal-null', 'messages', 'responses', 'known-terminal-null'],
  ['m2r-malformed', 'messages', 'responses', 'malformed'],
  ['m2r-duplicate', 'messages', 'responses', 'duplicate'],
  ['m2r-missing', 'messages', 'responses', 'missing'],
  ['m2r-hang', 'messages', 'responses', 'hang'],
  ['m2r-cancel', 'messages', 'responses', 'cancel'],
  ['m2r-slow', 'messages', 'responses', 'slow'],
  ['m2r-claude', 'messages', 'responses', 'early'],
  ['r2m-text', 'responses', 'messages', 'text'],
  ['r2m-null-usage', 'responses', 'messages', 'null-usage'],
  ['r2m-tool', 'responses', 'messages', 'tool'],
  ['r2m-result', 'responses', 'messages', 'result'],
  ['r2m-incomplete', 'responses', 'messages', 'incomplete'],
  ['r2m-error', 'responses', 'messages', 'failed'],
  ['r2m-http-error', 'responses', 'messages', 'http-error'],
  ['r2m-malformed', 'responses', 'messages', 'malformed'],
  ['r2m-unknown-event', 'responses', 'messages', 'unknown-event'],
  ['r2m-duplicate', 'responses', 'messages', 'duplicate'],
  ['r2m-missing', 'responses', 'messages', 'missing'],
  ['r2m-hang', 'responses', 'messages', 'hang'],
].map(([id, client, wire, scenario]) => ({ id, client, wire, scenario, upstreamModel: `up-${id}`,
  wireName: wire === 'responses' ? 'openai-responses' : 'anthropic-messages',
  upstreamPath: wire === 'responses' ? '/v1/responses' : '/v1/messages' }));
const byModel = new Map(routeSpecs.map(route => [route.upstreamModel, route]));

function sse(name, data) {
  return `event: ${name}\ndata: ${typeof data === 'string' ? data : JSON.stringify(data)}\n\n`;
}

function usage(input = 12, output = 5) {
  return { input_tokens: input, output_tokens: output, total_tokens: input + output,
    input_tokens_details: { cached_tokens: 3, cache_write_tokens: 2 } };
}

function responsesText(model, mode, terminalKind = 'completed') {
  let seq = 0;
  const responseID = `resp-${model}`;
  const marker = mode === 'result' ? 'SYNTHETIC_RESULT_OK' : 'SYNTHETIC_RESPONSES_TEXT_OK';
  const createdUsage = ['early', 'known-terminal-null', 'incomplete', 'failed', 'result', 'cancel', 'slow'].includes(mode) ?
    usage(12, 0) : null;
  const events = [['response.created', { type: 'response.created', sequence_number: seq++, response: {
    id: responseID, created_at: 1, model, usage: createdUsage } }]];
  if (mode === 'inprogress') events.push(['response.in_progress', { type: 'response.in_progress', sequence_number: seq++,
    response: { id: responseID, created_at: 1, model, usage: usage(12, 0) } }]);
  if (mode === 'failed') {
    events.push(['response.failed', { type: 'response.failed', sequence_number: seq++, response: { id: responseID,
      object: 'response', created_at: 1, model, status: 'failed', error: { code: 'server_error', message: 'private-upstream-error' } } }]);
    return events;
  }
  const item = { id: 'msg_1', type: 'message', status: 'completed', role: 'assistant',
    content: [{ type: 'output_text', text: marker, annotations: [] }] };
  events.push(
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: seq++, response_id: responseID,
      output_index: 0, item: { ...item, status: 'in_progress', content: [] } }],
    ['response.content_part.added', { type: 'response.content_part.added', sequence_number: seq++, response_id: responseID,
      item_id: 'msg_1', output_index: 0, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } }],
    ['response.output_text.delta', { type: 'response.output_text.delta', sequence_number: seq++, response_id: responseID,
      item_id: 'msg_1', output_index: 0, content_index: 0, delta: marker }],
    ['response.output_text.done', { type: 'response.output_text.done', sequence_number: seq++, response_id: responseID,
      item_id: 'msg_1', output_index: 0, content_index: 0, text: marker }],
    ['response.content_part.done', { type: 'response.content_part.done', sequence_number: seq++, response_id: responseID,
      item_id: 'msg_1', output_index: 0, content_index: 0, part: item.content[0] }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: seq++, response_id: responseID,
      output_index: 0, item }],
  );
  if (mode === 'missing' || mode === 'malformed' || mode === 'cancel' || mode === 'slow') return events;
  const status = terminalKind === 'incomplete' ? 'incomplete' : 'completed';
  const terminalUsage = ['unknown', 'known-terminal-null'].includes(mode) ? null : usage(12, 5);
  const response = { id: responseID, object: 'response', created_at: 1, model, status, output: [item], usage: terminalUsage };
  if (status === 'incomplete') response.incomplete_details = { reason: 'max_output_tokens' };
  events.push([`response.${terminalKind}`, { type: `response.${terminalKind}`, sequence_number: seq++, response }]);
  return events;
}

function responsesTool(model) {
  let seq = 0;
  const responseID = `resp-${model}`;
  const item = { id: 'fc_1', type: 'function_call', status: 'completed', call_id: 'call_1', name: 'echo',
    arguments: '{"value":"synthetic"}' };
  return [
    ['response.created', { type: 'response.created', sequence_number: seq++, response: { id: responseID, created_at: 1,
      model, usage: usage(12, 0) } }],
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: seq++, response_id: responseID,
      output_index: 0, item: { ...item, status: 'in_progress', arguments: '' } }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: seq++,
      response_id: responseID, item_id: 'fc_1', output_index: 0, delta: '{"value":' }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: seq++,
      response_id: responseID, item_id: 'fc_1', output_index: 0, delta: '"synthetic"}' }],
    ['response.function_call_arguments.done', { type: 'response.function_call_arguments.done', sequence_number: seq++,
      response_id: responseID, item_id: 'fc_1', output_index: 0, arguments: item.arguments }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: seq++, response_id: responseID,
      output_index: 0, item }],
    ['response.completed', { type: 'response.completed', sequence_number: seq++, response: { id: responseID,
      object: 'response', created_at: 1, model, status: 'completed', output: [item], usage: usage(12, 5) } }],
  ];
}

function messagesText(model, scenario) {
  const marker = scenario === 'result' ? 'SYNTHETIC_RESULT_OK' : 'SYNTHETIC_MESSAGES_TEXT_OK';
  return [
    ['message_start', { type: 'message_start', message: { id: `msg-${model}`, type: 'message', role: 'assistant', model,
      content: [], stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 12, output_tokens: 0, cache_read_input_tokens: 3, cache_creation_input_tokens: 2 } } }],
    ['ping', { type: 'ping' }],
    ['content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }],
    ['content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: marker } }],
    ['content_block_stop', { type: 'content_block_stop', index: 0 }],
    ['message_delta', { type: 'message_delta', delta: { stop_reason: null, stop_sequence: null },
      usage: { output_tokens: 1 } }],
    ['message_delta', { type: 'message_delta', delta: { stop_reason: scenario === 'incomplete' ? 'max_tokens' : 'end_turn',
      stop_sequence: null }, usage: scenario === 'null-usage' ?
        { input_tokens: null, output_tokens: 5, cache_read_input_tokens: null } : { output_tokens: 5 } }],
    ['message_stop', { type: 'message_stop' }],
  ];
}

function messagesTool(model) {
  return [
    ['message_start', { type: 'message_start', message: { id: `msg-${model}`, type: 'message', role: 'assistant', model,
      content: [], stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 12, output_tokens: 0, cache_read_input_tokens: 3, cache_creation_input_tokens: 2 } } }],
    ['content_block_start', { type: 'content_block_start', index: 0,
      content_block: { type: 'tool_use', id: 'call_1', name: 'echo', input: {} } }],
    ['content_block_delta', { type: 'content_block_delta', index: 0,
      delta: { type: 'input_json_delta', partial_json: '{"value":' } }],
    ['content_block_delta', { type: 'content_block_delta', index: 0,
      delta: { type: 'input_json_delta', partial_json: '"synthetic"}' } }],
    ['content_block_stop', { type: 'content_block_stop', index: 0 }],
    ['message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use', stop_sequence: null },
      usage: { output_tokens: 5 } }],
    ['message_stop', { type: 'message_stop' }],
  ];
}

function assertIsolation(request, rawBody, route) {
  assert.ok(employeeKey);
  assert.equal(request.url.includes(employeeKey), false);
  assert.equal(Object.values(request.headers).some(value => String(value).includes(employeeKey)), false);
  assert.equal(rawBody.includes(employeeKey), false);
  assert.equal(request.headers.authorization, `Bearer ${route.wire === 'responses' ? openAISecret : anthropicSecret}`);
  if (route.wire === 'messages') assert.equal(request.headers['anthropic-version'], '2023-06-01');
}

async function writeEvents(response, events) {
  for (const [name, event] of events) response.write(sse(name, event));
}

const upstream = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const rawBody = Buffer.concat(chunks).toString('utf8');
    const body = JSON.parse(rawBody);
    const route = byModel.get(body.model);
    assert.ok(route, `Unknown upstream model ${body.model}`);
    assertIsolation(request, rawBody, route);
    assert.equal(request.url.split('?')[0], route.upstreamPath);
    calls.push({ route: route.id, path: route.upstreamPath });
    if (route.scenario === 'result') {
      assert.ok(rawBody.includes(resultMarker), `${route.id}: tool result lost`);
      if (route.wire === 'responses') {
        assert.ok(body.input.some(item => item.type === 'function_call' && item.call_id === 'call_1'));
        assert.ok(body.input.some(item => item.type === 'function_call_output' && item.call_id === 'call_1'));
      } else {
        assert.ok(body.messages.some(message => message.role === 'assistant' &&
          message.content?.some?.(part => part.type === 'tool_use' && part.id === 'call_1')));
        assert.ok(body.messages.some(message => message.role === 'user' &&
          message.content?.some?.(part => part.type === 'tool_result' && part.tool_use_id === 'call_1')));
      }
    }
    if (route.scenario === 'http-error') {
      response.writeHead(503, { 'content-type': 'application/json' });
      response.end('{"error":{"message":"private-upstream-error"}}');
      return;
    }
    response.writeHead(200, { 'content-type': 'text/event-stream' });
    response.on('close', () => upstreamClosed.set(route.id, Date.now()));

    if (route.scenario === 'slow') {
      const events = responsesText(body.model, 'early');
      await writeEvents(response, events.slice(0, 3));
      let sequence = 3;
      while (!response.destroyed) {
        const event = { type: 'response.output_text.delta', sequence_number: sequence++, response_id: `resp-${body.model}`,
          item_id: 'msg_1', output_index: 0, content_index: 0, delta: 'x'.repeat(180_000) };
        if (!response.write(sse('response.output_text.delta', event))) await once(response, 'drain');
      }
      return;
    }

    if (route.wire === 'responses') {
      let events = route.scenario === 'tool' ? responsesTool(body.model) :
        responsesText(body.model, route.scenario, route.scenario === 'incomplete' ? 'incomplete' : 'completed');
      if (route.scenario === 'malformed') {
        await writeEvents(response, events.slice(0, 4)); response.end('event: mystery\ndata: {bad-json}\n\n'); return;
      }
      if (route.scenario === 'cancel') { await writeEvents(response, events.slice(0, 4)); return; }
      await writeEvents(response, events);
      if (route.scenario === 'missing') { response.end(); return; }
      if (route.scenario === 'duplicate') { response.end(sse(events.at(-1)[0], events.at(-1)[1])); return; }
      if (route.scenario === 'hang') return;
      const control = controls.get(route.id);
      if (control) {
        control.reached();
        await control.wait;
      }
      response.end();
      return;
    }

    if (route.scenario === 'failed') {
      response.end(sse('error', { type: 'error', error: { type: 'overloaded_error', message: 'private-upstream-error' } }));
      return;
    }
    let events = route.scenario === 'tool' ? messagesTool(body.model) : messagesText(body.model, route.scenario);
    if (route.scenario === 'malformed') {
      await writeEvents(response, events.slice(0, 4)); response.end('event: content_block_delta\ndata: {bad-json}\n\n'); return;
    }
    if (route.scenario === 'unknown-event') {
      await writeEvents(response, events.slice(0, 2)); response.end(sse('mystery', { type: 'mystery' })); return;
    }
    if (route.scenario === 'missing') events = events.slice(0, -1);
    await writeEvents(response, events);
    if (route.scenario === 'duplicate') { response.end(sse('message_stop', { type: 'message_stop' })); return; }
    if (route.scenario === 'hang') return;
    response.end();
  } catch (error) {
    fixtureErrors.push(error.stack ?? String(error));
    if (!response.headersSent) response.writeHead(500, { 'content-type': 'application/json' });
    response.end('{"error":{"message":"synthetic fixture failure"}}');
  }
});

const captureProxy = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const bytes = Buffer.concat(chunks), body = bytes.length ? JSON.parse(bytes) : {};
    const capture = { path: request.url.split('?')[0], fields: Object.keys(body).sort(), model: body.model,
      stream: body.stream === true, tool_count: Array.isArray(body.tools) ? body.tools.length : 0 };
    const headers = {};
    for (const [name, value] of Object.entries(request.headers)) {
      if (!['host', 'connection', 'content-length'].includes(name) && value !== undefined) headers[name] = value;
    }
    const forwarded = await fetch(`${origin}${request.url}`, { method: request.method, headers,
      body: bytes.length ? bytes : undefined, signal: AbortSignal.timeout(30_000) });
    capture.http_status = forwarded.status; capture.content_type = forwarded.headers.get('content-type')?.split(';')[0];
    claudeCaptures.push(capture);
    response.writeHead(forwarded.status, { 'content-type': forwarded.headers.get('content-type') ?? 'application/json' });
    if (forwarded.body) for await (const chunk of forwarded.body) response.write(chunk);
    response.end();
  } catch (error) {
    fixtureErrors.push(`capture proxy: ${error.stack ?? String(error)}`);
    if (!response.headersSent) response.writeHead(502, { 'content-type': 'application/json' });
    response.end('{"error":{"type":"api_error","message":"capture proxy failure"}}');
  }
});

function launch(extra) {
  const child = spawn(args.get('server'), ['--data-dir', dataDir, ...extra], { windowsHide: true,
    stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    serviceLogs += chunk;
    if (serviceLogs.length > (2 << 20)) child.kill();
  });
  return child;
}

async function startService() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  origin = `http://127.0.0.1:${port}`;
  service = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let attempt = 0; attempt < 100; attempt++) {
    try { if ((await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* retry */ }
    await delay(100);
  }
  throw new Error('Service readiness timeout');
}

async function stopService() {
  if (!service || service.exitCode !== null) return;
  const exited = once(service, 'exit'); service.stdin.end();
  await Promise.race([exited, delay(5000).then(() => service.kill())]);
}

async function admin(route, method = 'GET', body) {
  const response = await fetch(`${origin}/admin/api/v1${route}`, { method,
    headers: { cookie, origin, 'content-type': 'application/json', 'x-csrf-token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(10_000) });
  const text = await response.text();
  assert.ok(response.ok, `Admin ${method} ${route}: ${response.status} ${text}`);
  const setCookie = response.headers.get('set-cookie'); if (setCookie) cookie = setCookie.split(';')[0];
  return JSON.parse(text);
}

async function usageRows(model) {
  return (await admin(`/usage/requests?model_id=${encodeURIComponent(model)}&limit=100`)).items;
}

async function exportRows(model) {
  const from = new Date(Date.now() - 3_600_000).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const to = new Date(Date.now() + 3_600_000).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const response = await fetch(`${origin}/admin/api/v1/usage/export?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}` +
    `&model_id=${encodeURIComponent(model)}&limit=100`, { headers: { cookie }, signal: AbortSignal.timeout(10_000) });
  const csv = await response.text(); assert.equal(response.status, 200);
  const lines = csv.trim().split(/\r?\n/); if (lines.length < 2) return [];
  const header = lines[0].split(',');
  return lines.slice(1).filter(Boolean).map(line => Object.fromEntries(line.split(',').map((value, i) => [header[i], value])));
}

async function ledger(model, before, expectedStatus, expectedProtocol) {
  const prior = new Set(before.map(row => row.id));
  let added = [];
  for (let index = 0; index < 60; index++) {
    added = (await usageRows(model)).filter(row => !prior.has(row.id));
    if (added.length === 1 && added[0].status !== 'pending') break;
    await delay(100);
  }
  assert.equal(added.length, 1, `${model}: parent count`);
  assert.equal(added[0].status, expectedStatus, `${model}: parent status`);
  assert.equal(Number(added[0].attempt_count), 1, `${model}: attempt count`);
  const attempts = (await admin(`/usage/requests/${added[0].id}/attempts`)).items;
  assert.equal(attempts.length, 1); assert.equal(attempts[0].status, expectedStatus);
  const exported = await exportRows(model); assert.equal(exported.length, 1);
  assert.equal(exported[0].protocol, expectedProtocol);
  return { parent_requests: 1, attempts: 1, request_status: expectedStatus, actual_protocol: exported[0].protocol,
    usage: { input: attempts[0].input_tokens, output: attempts[0].output_tokens,
      cache_read: attempts[0].cache_read_tokens, cache_write: attempts[0].cache_write_tokens } };
}

function requestFor(route) {
  const tool = { name: 'echo', description: 'Synthetic echo', input_schema: { type: 'object',
    properties: { value: { type: 'string' } }, required: ['value'] } };
  if (route.client === 'messages') {
    const messages = route.scenario === 'result' ? [
      { role: 'user', content: promptMarker },
      { role: 'assistant', content: [{ type: 'tool_use', id: 'call_1', name: 'echo', input: { value: 'synthetic' } }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'call_1', content: resultMarker }] },
    ] : [{ role: 'user', content: promptMarker }];
    return { url: `${origin}/v1/messages`, headers: { 'anthropic-version': '2023-06-01' }, body: {
      model: route.id, max_tokens: 64, stream: true, messages,
      ...(route.scenario === 'tool' || route.scenario === 'result' ? { tools: [tool] } : {}) } };
  }
  const input = route.scenario === 'result' ? [
    { type: 'message', role: 'user', content: [{ type: 'input_text', text: promptMarker }] },
    { type: 'function_call', id: 'fc_1', call_id: 'call_1', name: 'echo', arguments: '{"value":"synthetic"}', status: 'completed' },
    { type: 'function_call_output', call_id: 'call_1', output: resultMarker },
  ] : promptMarker;
  return { url: `${origin}/v1/responses`, headers: {}, body: { model: route.id, stream: true, store: false,
    max_output_tokens: 64, input,
    ...(route.scenario === 'tool' || route.scenario === 'result' ?
      { tools: [{ type: 'function', name: tool.name, description: tool.description, parameters: tool.input_schema }] } : {}) } };
}

async function readBody(response, onText) {
  if (!response.body) return '';
  const reader = response.body.getReader(), decoder = new TextDecoder(); let text = '';
  for (;;) {
    const { value, done } = await reader.read(); if (done) break;
    text += decoder.decode(value, { stream: true }); if (onText) await onText(text);
  }
  return text + decoder.decode();
}

async function send(route, options = {}) {
  const before = await usageRows(route.id), beforeCalls = calls.length;
  const controller = new AbortController(), request = requestFor(route);
  let firstEventBeforeRelease = false;
  const response = await fetch(request.url, { method: 'POST', headers: { authorization: `Bearer ${employeeKey}`,
    'content-type': 'application/json', ...request.headers }, body: JSON.stringify(request.body), signal: controller.signal });
  let text = '';
  try {
    text = await readBody(response, current => {
      if (options.abort && (current.includes('SYNTHETIC_') || current.includes('output_text.delta'))) controller.abort();
      if (options.releaseOnEvent && !firstEventBeforeRelease && current.includes(options.releaseOnEvent)) {
        firstEventBeforeRelease = true; controls.get(route.id)?.release();
      }
    });
  } catch (error) {
    if (!options.abort || error.name !== 'AbortError') throw error;
  }
  assert.equal(calls.length, beforeCalls + 1, `${route.id}: dispatch cardinality`);
  return { response, text, before, firstEventBeforeRelease };
}

function control(routeID) {
  let release, reached;
  const wait = new Promise(resolve => { release = resolve; });
  const hit = new Promise(resolve => { reached = resolve; });
  controls.set(routeID, { wait, release, reached, hit });
  return controls.get(routeID);
}

function assertSuccessTerminal(route, text, kind = 'completed') {
  if (route.client === 'messages') {
    assert.ok(text.includes('event: message_stop')); assert.equal((text.match(/event: message_stop/g) ?? []).length, 1);
  } else {
    assert.ok(text.includes(`event: response.${kind}`));
    assert.equal((text.match(new RegExp(`event: response\\.${kind}`, 'g')) ?? []).length, 1);
  }
}

function assertNoSuccess(route, text) {
  if (route.client === 'messages') assert.equal(text.includes('event: message_stop'), false);
  else assert.equal(text.includes('event: response.completed'), false);
}

async function slowReader(route) {
  const before = await usageRows(route.id), beforeCalls = calls.length, body = JSON.stringify(requestFor(route).body);
  const url = new URL(origin), socket = net.createConnection({ host: url.hostname, port: Number(url.port) });
  await once(socket, 'connect');
  socket.write(`POST /v1/messages HTTP/1.1\r\nHost: ${url.host}\r\nAuthorization: Bearer ${employeeKey}\r\n` +
    `Anthropic-Version: 2023-06-01\r\nContent-Type: application/json\r\nContent-Length: ${Buffer.byteLength(body)}\r\n` +
    `Connection: keep-alive\r\n\r\n${body}`);
  socket.pause(); const started = Date.now();
  for (let index = 0; index < 400 && !upstreamClosed.has(route.id); index++) await delay(100);
  const elapsed = Date.now() - started; socket.destroy();
  assert.ok(upstreamClosed.has(route.id), 'slow reader did not release upstream');
  assert.ok(elapsed >= 25_000 && elapsed < 40_000, `slow reader deadline ${elapsed}ms`);
  assert.equal(calls.length, beforeCalls + 1);
  return { ...(await ledger(route.id, before, 'cancelled', 'openai-responses')), upstream_calls: 1,
    upstream_released_ms: elapsed };
}

async function sha256(file) { return createHash('sha256').update(await readFile(file)).digest('hex'); }

async function runProcess(executable, processArgs, env, timeout = 45_000) {
  const child = spawn(executable, processArgs, { cwd: root, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > (1 << 20)) child.kill(); });
  child.stderr.on('data', chunk => { stderr += chunk; if (stderr.length > (1 << 20)) child.kill(); });
  const timer = setTimeout(() => child.kill(), timeout);
  const [code] = await once(child, 'exit'); clearTimeout(timer);
  return { code, stdout, stderr };
}

async function actualClaude(route) {
  if (!args.has('claude')) return { status: 'SKIP', reason: 'claude_not_supplied' };
  const home = path.join(root, 'claude-home');
  for (const directory of [home, path.join(home, 'config'), path.join(home, 'AppData', 'Roaming'),
    path.join(home, 'AppData', 'Local'), path.join(home, 'tmp')]) await mkdir(directory, { recursive: true });
  const before = await usageRows(route.id), beforeCalls = calls.length, beforeCaptures = claudeCaptures.length;
  const env = { SystemRoot: process.env.SystemRoot, WINDIR: process.env.WINDIR, ComSpec: process.env.ComSpec,
    PATH: process.env.PATH, PATHEXT: process.env.PATHEXT, HOME: home, USERPROFILE: home,
    APPDATA: path.join(home, 'AppData', 'Roaming'), LOCALAPPDATA: path.join(home, 'AppData', 'Local'),
    TEMP: path.join(home, 'tmp'), TMP: path.join(home, 'tmp'), CLAUDE_CONFIG_DIR: path.join(home, 'config'),
    ANTHROPIC_API_KEY: employeeKey, ANTHROPIC_BASE_URL: captureOrigin, CLAUDE_CODE_MAX_RETRIES: '0',
    DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1', NO_COLOR: '1', CI: '1' };
  const result = await runProcess(args.get('claude'), ['--bare', '--print', '--output-format', 'stream-json', '--verbose',
    '--include-partial-messages', '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk',
    '--permission-prompts', 'none', '--tools', '', '--model', route.id, 'Return exactly one word.'], env);
  const captured = claudeCaptures.slice(beforeCaptures).filter(item => item.path === '/v1/messages');
  const upstreamCalls = calls.length - beforeCalls;
  if (result.code === 0 && upstreamCalls === 1 && result.stdout.includes('SYNTHETIC_RESPONSES_TEXT_OK')) {
    return { status: 'PASS', exit_code: result.code, request_shapes: captured, upstream_calls: 1,
      ...(await ledger(route.id, before, 'succeeded', route.wireName)) };
  }
  if (upstreamCalls === 0 && captured.length >= 1 && captured.every(item => item.http_status === 400)) {
    const prior = new Set(before.map(row => row.id));
    const added = (await usageRows(route.id)).filter(row => !prior.has(row.id));
    assert.ok(added.every(row => Number(row.attempt_count) === 0));
    return { status: 'UNSUPPORTED', exit_code: result.code, request_shapes: captured,
      parent_requests: added.length, attempts: 0, upstream_calls: 0, reason: 'actual_claude_request_not_representable' };
  }
  return { status: 'FAIL', exit_code: result.code, request_shapes: captured, upstream_calls: upstreamCalls,
    reason_code: 'actual_claude_unexpected_result' };
}

const results = [];
try {
  upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
  const init = launch(['--init']); init.stdin.end(`${password}\n`); assert.equal((await once(init, 'exit'))[0], 0);
  await startService();
  captureProxy.listen(0, '127.0.0.1'); await once(captureProxy, 'listening');
  captureOrigin = `http://127.0.0.1:${captureProxy.address().port}`;
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const openAI = await admin('/upstreams', 'POST', { name: 'Synthetic Responses', provider_kind: 'openai-compatible',
    endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: openAISecret });
  const anthropic = await admin('/upstreams', 'POST', { name: 'Synthetic Messages', provider_kind: 'anthropic-api-key',
    endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: anthropicSecret });
  for (const route of routeSpecs) await admin('/models', 'POST', { id: route.id,
    upstream_id: route.wire === 'responses' ? openAI.id : anthropic.id,
    upstream_model: route.upstreamModel, wire_protocol: route.wireName });
  const employee = await admin('/employees', 'POST', { name: 'Messages Responses acceptance' });
  employeeKey = (await admin(`/employees/${employee.id}/keys`, 'POST',
    { name: 'acceptance', operation_id: randomUUID() })).key;

  for (const id of ['m2r-early', 'm2r-inprogress']) {
    const route = byModel.get(`up-${id}`), gate = control(id);
    const pending = send(route, { releaseOnEvent: 'SYNTHETIC_RESPONSES_TEXT_OK' });
    await gate.hit;
    const sent = await pending; controls.delete(id);
    assert.equal(sent.firstEventBeforeRelease, true); assertSuccessTerminal(route, sent.text);
    const facts = await ledger(id, sent.before, 'succeeded', 'openai-responses');
    assert.deepEqual(facts.usage, { input: '7', output: '5', cache_read: '3', cache_write: '2' });
    results.push({ scenario: id, status: 'PASS', streaming: 'LIVE', first_event_before_upstream_eof: true,
      upstream_path: route.upstreamPath, upstream_calls: 1, ...facts });
  }

  {
    const id = 'm2r-delayed', route = byModel.get(`up-${id}`), gate = control(id);
    let settled = false; const pending = send(route).then(value => { settled = true; return value; });
    await gate.hit; await delay(250); assert.equal(settled, false, 'terminal-only usage leaked target events before EOF');
    gate.release(); const sent = await pending; controls.delete(id); assertSuccessTerminal(route, sent.text);
    results.push({ scenario: id, status: 'PASS', streaming: 'DELAYED', leaked_before_clean_eof: false,
      bounded_full_stream_release: true, upstream_calls: 1,
      ...(await ledger(id, sent.before, 'succeeded', 'openai-responses')) });
  }

  for (const id of ['m2r-tool', 'r2m-tool']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertSuccessTerminal(route, sent.text);
    assert.ok(sent.text.includes('call_1') && sent.text.includes('echo'));
    assert.ok(sent.text.includes('synthetic'), `${id}: incremental arguments lost`);
    results.push({ scenario: id, status: 'PASS', incremental_arguments: true, stable_call_identity: true,
      upstream_calls: 1, ...(await ledger(id, sent.before, 'succeeded', route.wireName)) });
  }

  for (const id of ['m2r-result', 'r2m-result']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertSuccessTerminal(route, sent.text);
    assert.ok(sent.text.includes('SYNTHETIC_RESULT_OK'));
    results.push({ scenario: id, status: 'PASS', tool_result_on_next_request: true, upstream_calls: 1,
      ...(await ledger(id, sent.before, 'succeeded', route.wireName)) });
  }

  {
    const route = byModel.get('up-r2m-text'), sent = await send(route); assertSuccessTerminal(route, sent.text);
    assert.ok(sent.text.includes('SYNTHETIC_MESSAGES_TEXT_OK'));
    assert.ok(sent.text.includes('"output_tokens":5') && !sent.text.includes('"output_tokens":6'));
    const facts = await ledger(route.id, sent.before, 'succeeded', 'anthropic-messages');
    assert.deepEqual(facts.usage, { input: '12', output: '5', cache_read: '3', cache_write: '2' });
    results.push({ scenario: route.id, status: 'PASS', streaming: 'LIVE', cumulative_usage_replaced_not_summed: true,
      optional_usage_preserved: true, upstream_calls: 1, ...facts });
  }

  {
    const route = byModel.get('up-r2m-null-usage'), sent = await send(route); assertSuccessTerminal(route, sent.text);
    const facts = await ledger(route.id, sent.before, 'succeeded', 'anthropic-messages');
    const expected = { input: '12', output: '5', cache_read: '3', cache_write: '2' };
    const matched = JSON.stringify(facts.usage) === JSON.stringify(expected);
    results.push({ scenario: route.id, status: matched ? 'PASS' : 'FAIL', cumulative_null_preserves_prior: matched,
      expected_usage: expected, upstream_calls: 1, ...facts,
      ...(matched ? {} : { reason_code: 'anthropic_null_usage_clears_prior_snapshot' }) });
  }

  for (const id of ['m2r-incomplete', 'r2m-incomplete']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertSuccessTerminal(route, sent.text, 'incomplete');
    results.push({ scenario: id, status: 'PASS', terminal_outcome: 'incomplete', upstream_calls: 1,
      ...(await ledger(id, sent.before, 'interrupted', route.wireName)) });
  }

  for (const id of ['m2r-failed', 'r2m-error']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertNoSuccess(route, sent.text);
    assert.equal(sent.text.includes('private-upstream-error'), false);
    results.push({ scenario: id, status: 'PASS', terminal_outcome: 'failed', error_redacted: true, upstream_calls: 1,
      ...(await ledger(id, sent.before, 'failed', route.wireName)) });
  }

  for (const id of ['m2r-http-error', 'r2m-http-error']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assert.equal(sent.response.status, 502);
    assert.equal(sent.text.includes('private-upstream-error'), false);
    results.push({ scenario: id, status: 'PASS', upstream_http_status: 503, client_http_status: 502,
      upstream_calls: 1, ...(await ledger(id, sent.before, 'failed', route.wireName)) });
  }

  {
    const route = byModel.get('up-m2r-unknown'), sent = await send(route);
    assert.equal(sent.response.status, 502); assert.equal(sent.text.includes('event: message_start'), false);
    const facts = await ledger(route.id, sent.before, 'failed', route.wireName);
    assert.deepEqual(facts.usage, { input: null, output: null, cache_read: null, cache_write: null });
    results.push({ scenario: route.id, status: 'PASS', target_semantic_events: 0, upstream_calls: 1,
      unknown_usage_not_zero: true, ...facts });
  }

  {
    const route = byModel.get('up-m2r-known-terminal-null'), sent = await send(route);
    assert.equal(sent.response.status, 200); assert.ok(sent.text.includes('event: message_start'));
    assertNoSuccess(route, sent.text);
    results.push({ scenario: route.id, status: 'PASS', prior_target_events: true, success_terminal: false,
      upstream_calls: 1, ...(await ledger(route.id, sent.before, 'failed', route.wireName)) });
  }

  for (const id of ['m2r-malformed', 'r2m-malformed', 'r2m-unknown-event', 'm2r-duplicate', 'r2m-duplicate']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertNoSuccess(route, sent.text);
    results.push({ scenario: id, status: 'PASS', fail_closed: true, success_terminal: false, upstream_calls: 1,
      ...(await ledger(id, sent.before, 'failed', route.wireName)) });
  }

  for (const id of ['m2r-missing', 'r2m-missing', 'm2r-hang', 'r2m-hang']) {
    const route = byModel.get(`up-${id}`), sent = await send(route); assertNoSuccess(route, sent.text);
    results.push({ scenario: id, status: 'PASS', fail_closed: true, success_terminal: false, upstream_calls: 1,
      ...(await ledger(id, sent.before, 'interrupted', route.wireName)) });
  }

  {
    const route = byModel.get('up-m2r-cancel'), sent = await send(route, { abort: true });
    for (let i = 0; i < 60 && !upstreamClosed.has(route.id); i++) await delay(100);
    assert.ok(upstreamClosed.has(route.id));
    results.push({ scenario: route.id, status: 'PASS', client_close_released_upstream: true, upstream_calls: 1,
      ...(await ledger(route.id, sent.before, 'cancelled', route.wireName)) });
  }

  results.push({ scenario: 'm2r-slow', status: 'PASS', ...(await slowReader(byModel.get('up-m2r-slow'))) });
  results.push({ scenario: 'actual-claude-messages-to-responses',
    ...(await actualClaude(byModel.get('up-m2r-claude'))) });

  assert.deepEqual(fixtureErrors, []);
  assert.equal(serviceLogs.includes(employeeKey), false); assert.equal(serviceLogs.includes(promptMarker), false);
  assert.equal(serviceLogs.includes(resultMarker), false); assert.equal(JSON.stringify(calls).includes(employeeKey), false);
  const status = results.some(result => result.status === 'FAIL') ? 'FAIL' : 'PASS';
  const claude = args.has('claude') ? { version: (await runProcess(args.get('claude'), ['--version'], process.env)).stdout.trim(),
    sha256: await sha256(args.get('claude')) } : undefined;
  console.log(JSON.stringify({ status, boundary: 'messages_responses_cross_protocol_sse_real_process',
    source_commit: args.get('source-commit'), server_sha256: await sha256(args.get('server')),
    platform: { os: os.platform(), release: os.release(), arch: os.arch() }, claude, results }, null, 2));
  if (status === 'FAIL') process.exitCode = 1;
} finally {
  await stopService(); upstream.closeAllConnections(); captureProxy.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  if (captureProxy.listening) await new Promise(resolve => captureProxy.close(resolve));
  const resolved = path.resolve(root); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-messages-responses-stream-'));
  await rm(resolved, { recursive: true, force: true, maxRetries: 4, retryDelay: 250 });
  assert.equal((await readdir(os.tmpdir())).includes(path.basename(resolved)), false);
}
