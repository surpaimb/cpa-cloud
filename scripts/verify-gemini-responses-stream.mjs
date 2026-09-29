// Independent real-process acceptance for Gemini v1beta streamGenerateContent <-> OpenAI Responses SSE.
// Uses only temporary CPA Cloud state, synthetic credentials, Node.js built-ins, and loopback upstreams.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
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

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-gemini-responses-stream-'));
const dataDir = path.join(root, 'data');
const password = randomBytes(24).toString('hex');
const openAISecret = `synthetic-openai-${randomUUID()}`;
const geminiSecret = `synthetic-gemini-${randomUUID()}`;
const promptMarker = `SYNTHETIC_GEMINI_PROMPT_${randomUUID()}`;
const resultMarker = `SYNTHETIC_GEMINI_RESULT_${randomUUID()}`;
const privateError = `SYNTHETIC_PRIVATE_ERROR_${randomUUID()}`;
const employeeKeys = [];
const calls = [];
const fixtureErrors = [];
const upstreamClosed = new Map();
const controls = new Map();
let service, origin = '', cookie = '', csrf = '', serviceLogs = '';

const routeSpecs = [
  ['g2r-text', 'gemini', 'responses', 'text'],
  ['g2r-drain', 'gemini', 'responses', 'drain'],
  ['g2r-tool', 'gemini', 'responses', 'tool'],
  ['g2r-result', 'gemini', 'responses', 'result'],
  ['g2r-incomplete', 'gemini', 'responses', 'incomplete'],
  ['g2r-failed', 'gemini', 'responses', 'failed'],
  ['g2r-duplicate', 'gemini', 'responses', 'duplicate'],
  ['g2r-missing', 'gemini', 'responses', 'missing'],
  ['g2r-cancel', 'gemini', 'responses', 'cancel'],
  ['g2r-slow', 'gemini', 'responses', 'slow'],
  ['g2r-unknown', 'gemini', 'responses', 'unknown'],
  ['r2g-text', 'responses', 'gemini', 'text'],
  ['r2g-late', 'responses', 'gemini', 'late'],
  ['r2g-drain', 'responses', 'gemini', 'drain'],
  ['r2g-missing-identity', 'responses', 'gemini', 'missing-identity'],
  ['r2g-tool', 'responses', 'gemini', 'tool'],
  ['r2g-generated-call', 'responses', 'gemini', 'generated-call'],
  ['r2g-result', 'responses', 'gemini', 'result'],
  ['r2g-incomplete', 'responses', 'gemini', 'incomplete'],
  ['r2g-failed', 'responses', 'gemini', 'failed'],
  ['r2g-badtail', 'responses', 'gemini', 'badtail'],
  ['r2g-duplicate', 'responses', 'gemini', 'duplicate'],
  ['r2g-missing', 'responses', 'gemini', 'missing'],
  ['r2g-cancel', 'responses', 'gemini', 'cancel'],
  ['r2g-slow', 'responses', 'gemini', 'slow'],
  ['r2g-unknown', 'responses', 'gemini', 'unknown'],
].map(([id, client, wire, scenario]) => ({
  id, client, wire, scenario, upstreamModel: `up-${id}`,
  wireName: wire === 'responses' ? 'openai-responses' : 'gemini-generate-content',
  upstreamPath: wire === 'responses' ? '/v1/responses' : `/v1beta/models/up-${id}:streamGenerateContent`,
}));
const byID = new Map(routeSpecs.map(route => [route.id, route]));
const byUpstreamModel = new Map(routeSpecs.map(route => [route.upstreamModel, route]));

function sse(name, data) {
  return `${name ? `event: ${name}\n` : ''}data: ${typeof data === 'string' ? data : JSON.stringify(data)}\n\n`;
}

function responsesUsage(output = 4) {
  return { input_tokens: 8, output_tokens: output, total_tokens: 8 + output,
    input_tokens_details: { cached_tokens: 2 } };
}

function responsesTextEvents(route, { incomplete = false, knownUsage = true, text = ['G2R_A', 'G2R_B'] } = {}) {
  let sequence = 0;
  const responseID = `resp-${route.id}`, itemID = `msg-${route.id}`, combined = text.join('');
  const output = [{ id: itemID, type: 'message', status: 'completed', role: 'assistant',
    content: [{ type: 'output_text', text: combined, annotations: [] }] }];
  const created = { id: responseID, object: 'response', created_at: 1, model: route.upstreamModel,
    status: 'in_progress', output: [], ...(knownUsage ? { usage: responsesUsage(0) } : {}) };
  const terminal = { id: responseID, object: 'response', created_at: 1, completed_at: 2,
    model: route.upstreamModel, status: incomplete ? 'incomplete' : 'completed', output,
    ...(incomplete ? { incomplete_details: { reason: 'max_output_tokens' } } : {}),
    ...(knownUsage ? { usage: responsesUsage(4) } : {}) };
  const events = [
    ['response.created', { type: 'response.created', sequence_number: sequence++, response: created }],
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: sequence++, response_id: responseID,
      output_index: 0, item: { id: itemID, type: 'message', status: 'in_progress', role: 'assistant', content: [] } }],
    ['response.content_part.added', { type: 'response.content_part.added', sequence_number: sequence++, response_id: responseID,
      item_id: itemID, output_index: 0, content_index: 0,
      part: { type: 'output_text', text: '', annotations: [] } }],
  ];
  for (const delta of text) events.push(['response.output_text.delta', { type: 'response.output_text.delta',
    sequence_number: sequence++, response_id: responseID, item_id: itemID, output_index: 0, content_index: 0, delta }]);
  events.push(
    ['response.output_text.done', { type: 'response.output_text.done', sequence_number: sequence++, response_id: responseID,
      item_id: itemID, output_index: 0, content_index: 0, text: combined }],
    ['response.content_part.done', { type: 'response.content_part.done', sequence_number: sequence++, response_id: responseID,
      item_id: itemID, output_index: 0, content_index: 0,
      part: { type: 'output_text', text: combined, annotations: [] } }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: sequence++, response_id: responseID,
      output_index: 0, item: output[0] }],
  );
  const terminalName = incomplete ? 'response.incomplete' : 'response.completed';
  events.push([terminalName, { type: terminalName, sequence_number: sequence++, response: terminal }]);
  return events;
}

function responsesToolEvents(route) {
  let sequence = 0;
  const responseID = `resp-${route.id}`, itemID = `fc-${route.id}`, callID = 'call_rsp', argsText = '{"q":"x"}';
  const item = { id: itemID, type: 'function_call', status: 'completed', call_id: callID,
    name: 'lookup', arguments: argsText };
  const created = { id: responseID, object: 'response', created_at: 1, model: route.upstreamModel,
    status: 'in_progress', output: [], usage: responsesUsage(0) };
  const completed = { id: responseID, object: 'response', created_at: 1, completed_at: 2,
    model: route.upstreamModel, status: 'completed', output: [item], usage: responsesUsage(2) };
  return [
    ['response.created', { type: 'response.created', sequence_number: sequence++, response: created }],
    ['response.output_item.added', { type: 'response.output_item.added', sequence_number: sequence++, response_id: responseID,
      output_index: 0, item: { ...item, status: 'in_progress', arguments: '' } }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: sequence++,
      response_id: responseID, item_id: itemID, output_index: 0, delta: '{"q":' }],
    ['response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', sequence_number: sequence++,
      response_id: responseID, item_id: itemID, output_index: 0, delta: '"x"}' }],
    ['response.function_call_arguments.done', { type: 'response.function_call_arguments.done', sequence_number: sequence++,
      response_id: responseID, item_id: itemID, output_index: 0, arguments: argsText }],
    ['response.output_item.done', { type: 'response.output_item.done', sequence_number: sequence++, response_id: responseID,
      output_index: 0, item }],
    ['response.completed', { type: 'response.completed', sequence_number: sequence++, response: completed }],
  ];
}

function geminiUsage(output = 5) {
  return { promptTokenCount: 9, candidatesTokenCount: output, totalTokenCount: 9 + output,
    cachedContentTokenCount: 3 };
}

function geminiTextFrames(route, { incomplete = false, knownUsage = true, text = ['R2G_A', 'R2G_B'] } = {}) {
  const identity = { responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}` };
  const frames = text.map((value, index) => ({ ...identity,
    candidates: [{ index: 0, content: { role: 'model', parts: [{ text: value }] } }],
    ...(knownUsage && index === 0 ? { usageMetadata: geminiUsage(0) } : {}) }));
  frames.push({ ...identity, candidates: [{ index: 0, finishReason: incomplete ? 'MAX_TOKENS' : 'STOP' }],
    ...(knownUsage ? { usageMetadata: geminiUsage(5) } : {}) });
  return frames;
}

function geminiToolFrames(route, includeID = true) {
  const identity = { responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}` };
  const functionCall = { name: 'lookup', args: { q: 'x' }, ...(includeID ? { id: 'call_gem' } : {}) };
  return [
    { ...identity, candidates: [{ index: 0, content: { role: 'model', parts: [{ functionCall }] } }],
      usageMetadata: geminiUsage(0) },
    { ...identity, candidates: [{ index: 0, finishReason: 'STOP' }], usageMetadata: geminiUsage(2) },
  ];
}

function parseSSE(text) {
  return text.split(/\r?\n\r?\n/).filter(Boolean).map(block => {
    let name = ''; const data = [];
    for (const line of block.split(/\r?\n/)) {
      if (line.startsWith('event:')) name = line.slice(6).trim();
      if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
    }
    const raw = data.join('\n');
    let value;
    try { value = JSON.parse(raw); } catch { value = undefined; }
    return { name, raw, value };
  });
}

function findObjects(value, predicate, found = []) {
  if (!value || typeof value !== 'object') return found;
  if (predicate(value)) found.push(value);
  for (const child of Array.isArray(value) ? value : Object.values(value)) findObjects(child, predicate, found);
  return found;
}

function assertUpstreamIsolation(request, rawBody, route) {
  const urlAndBody = `${request.url}\n${rawBody}`;
  for (const key of employeeKeys) {
    assert.equal(urlAndBody.includes(key), false, `${route.id}: employee key leaked to upstream URL/body`);
    assert.equal(JSON.stringify(request.headers).includes(key), false, `${route.id}: employee key leaked to upstream headers`);
  }
  for (const secret of [openAISecret, geminiSecret]) assert.equal(urlAndBody.includes(secret), false,
    `${route.id}: upstream credential leaked to URL/body`);
  for (const marker of [promptMarker, resultMarker]) {
    assert.equal(request.url.includes(marker), false, `${route.id}: content leaked to upstream URL`);
    assert.equal(JSON.stringify(request.headers).includes(marker), false, `${route.id}: content leaked to upstream headers`);
  }
  if (route.wire === 'responses') {
    assert.equal(request.headers.authorization, `Bearer ${openAISecret}`);
    assert.equal(request.headers['x-goog-api-key'], undefined);
  } else {
    assert.equal(request.headers['x-goog-api-key'], geminiSecret);
    assert.equal(request.headers.authorization, undefined);
  }
}

function newGate(name) {
  let reached, release;
  const hit = new Promise(resolve => { reached = resolve; });
  const wait = new Promise(resolve => { release = resolve; });
  const value = { reached, release, hit, wait };
  controls.set(name, value);
  return value;
}

async function pauseAt(name) {
  const value = controls.get(name);
  if (!value) return;
  value.reached();
  await value.wait;
}

async function writeFrame(response, frame) {
  const split = Math.min(17, frame.length);
  response.write(frame.slice(0, split));
  await delay(1);
  response.write(frame.slice(split));
}

async function writePairs(response, events) {
  for (const [name, data] of events) await writeFrame(response, sse(name, data));
}

async function writeGemini(response, frames) {
  for (const frame of frames) await writeFrame(response, sse('', frame));
}

async function waitForDrainOrClose(response) {
  await new Promise(resolve => {
    const finish = () => {
      response.off('drain', finish); response.off('close', finish); response.off('error', finish); resolve();
    };
    response.once('drain', finish); response.once('close', finish); response.once('error', finish);
  });
}

const upstream = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const rawBody = Buffer.concat(chunks).toString('utf8');
    const body = JSON.parse(rawBody);
    let route;
    if (request.url.split('?')[0] === '/v1/responses') route = byUpstreamModel.get(body.model);
    else {
      const match = request.url.split('?')[0].match(/^\/v1beta\/models\/(.+):streamGenerateContent$/);
      if (match) route = byUpstreamModel.get(decodeURIComponent(match[1]));
    }
    assert.ok(route, `unknown synthetic upstream target ${request.url}`);
    assertUpstreamIsolation(request, rawBody, route);
    assert.equal(rawBody.includes(promptMarker), true, `${route.id}: prompt not represented upstream`);
    if (route.wire === 'responses') {
      assert.equal(request.url, '/v1/responses');
      assert.equal(body.model, route.upstreamModel); assert.equal(body.stream, true); assert.equal(body.store, false);
    } else {
      assert.equal(request.url, `${route.upstreamPath}?alt=sse`);
      assert.equal(Object.hasOwn(body, 'stream'), false, `${route.id}: invented Gemini stream field`);
    }
    if (route.scenario === 'result') {
      assert.equal(rawBody.includes(resultMarker), true, `${route.id}: function result lost`);
      if (route.wire === 'responses') {
        assert.equal(findObjects(body, value => value.type === 'function_call' && value.call_id === 'call_history').length, 1);
        assert.equal(findObjects(body, value => value.type === 'function_call_output' && value.call_id === 'call_history' &&
          value.output === JSON.stringify({ output: resultMarker })).length, 1);
      } else {
        assert.equal(findObjects(body, value => value.functionCall?.id === 'call_history' && value.functionCall.name === 'lookup').length, 1);
        assert.equal(findObjects(body, value => value.functionResponse?.id === 'call_history' &&
          value.functionResponse.name === 'lookup' && value.functionResponse.response?.output === resultMarker).length, 1);
      }
    }
    calls.push({ route: route.id, path: request.url });
    response.writeHead(200, { 'content-type': 'text/event-stream' });
    response.on('close', () => upstreamClosed.set(route.id, Date.now()));

    if (route.wire === 'responses') {
      if (route.scenario === 'failed') {
        const created = responsesTextEvents(route)[0];
        await writePairs(response, [created]);
        await writeFrame(response, sse('error', { type: 'error', sequence_number: 1,
          error: { type: 'api_error', message: privateError } }));
        response.end(); return;
      }
      if (route.scenario === 'cancel' || route.scenario === 'slow') {
        const events = responsesTextEvents(route);
        await writePairs(response, events.slice(0, 3));
        let sequence = 3;
        while (!response.destroyed) {
          const delta = route.scenario === 'slow' ? 'x'.repeat(180_000) : 'CANCEL_G2R';
          const event = { type: 'response.output_text.delta', sequence_number: sequence++,
            response_id: `resp-${route.id}`, item_id: `msg-${route.id}`, output_index: 0, content_index: 0, delta };
          if (!response.write(sse('response.output_text.delta', event))) await waitForDrainOrClose(response);
          if (route.scenario === 'cancel') await delay(10);
        }
        return;
      }
      const events = route.scenario === 'tool' ? responsesToolEvents(route) : responsesTextEvents(route, {
        incomplete: route.scenario === 'incomplete', knownUsage: route.scenario !== 'unknown',
        text: route.scenario === 'result' ? ['G2R_RESULT'] : ['G2R_A', 'G2R_B'],
      });
      if (route.scenario === 'text') {
        await writePairs(response, events.slice(0, -1)); await pauseAt(`${route.id}:before-terminal`);
        await writePairs(response, events.slice(-1)); response.end(); return;
      }
      if (route.scenario === 'drain') {
        await writePairs(response, events); await pauseAt(`${route.id}:after-terminal`); response.end(); return;
      }
      if (route.scenario === 'missing') { await writePairs(response, events.slice(0, -1)); response.end(); return; }
      await writePairs(response, events);
      if (route.scenario === 'duplicate') await writePairs(response, events.slice(-1));
      response.end(); return;
    }

    const frames = route.scenario === 'tool' ? geminiToolFrames(route, true) :
      route.scenario === 'generated-call' ? geminiToolFrames(route, false) : geminiTextFrames(route, {
        incomplete: route.scenario === 'incomplete', knownUsage: route.scenario !== 'unknown',
        text: route.scenario === 'result' ? ['R2G_RESULT'] : ['R2G_A', 'R2G_B'],
      });
    if (route.scenario === 'cancel' || route.scenario === 'slow') {
      const identity = { responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}` };
      let index = 0;
      while (!response.destroyed) {
        const text = route.scenario === 'slow' ? 'y'.repeat(180_000) : `CANCEL_R2G_${index++}`;
        const frame = { ...identity, candidates: [{ index: 0, content: { role: 'model', parts: [{ text }] } }],
          ...(index === 1 ? { usageMetadata: geminiUsage(0) } : {}) };
        if (!response.write(sse('', frame))) await waitForDrainOrClose(response);
        if (route.scenario === 'cancel') await delay(10);
      }
      return;
    }
    if (route.scenario === 'late') {
      await writeGemini(response, [{ candidates: [{ index: 0,
        content: { role: 'model', parts: [{ text: 'LATE_FIRST' }] } }], usageMetadata: geminiUsage(0) }]);
      await pauseAt(`${route.id}:before-identity`);
      await writeGemini(response, [{ responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}`,
        candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'LATE_SECOND' }] } }] }]);
      await pauseAt(`${route.id}:after-identity`);
      await writeGemini(response, [{ responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}`,
        candidates: [{ index: 0, finishReason: 'STOP' }], usageMetadata: geminiUsage(5) }]);
      response.end(); return;
    }
    if (route.scenario === 'missing-identity') {
      await writeGemini(response, [
        { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'NO_IDENTITY' }] } }] },
        { candidates: [{ index: 0, finishReason: 'STOP' }], usageMetadata: geminiUsage(5) },
      ]);
      response.end(); return;
    }
    if (route.scenario === 'failed') {
      await writeGemini(response, [{ responseId: `gem-${route.id}`, modelVersion: `model-${route.upstreamModel}`,
        candidates: [{ index: 0, finishReason: 'SAFETY' }] }]);
      response.end(); return;
    }
    if (route.scenario === 'text') {
      await writeGemini(response, frames.slice(0, 1)); await pauseAt(`${route.id}:before-terminal`);
      await writeGemini(response, frames.slice(1)); response.end(); return;
    }
    if (route.scenario === 'drain') {
      await writeGemini(response, frames); await pauseAt(`${route.id}:after-terminal`); response.end(); return;
    }
    if (route.scenario === 'missing') { await writeGemini(response, frames.slice(0, -1)); response.end(); return; }
    await writeGemini(response, frames);
    if (route.scenario === 'duplicate') await writeGemini(response, frames.slice(-1));
    if (route.scenario === 'badtail') await writeFrame(response, `data: {"postTerminal":"${privateError}"}\n\n`);
    response.end();
  } catch (error) {
    fixtureErrors.push(error.stack ?? String(error));
    if (!response.headersSent) response.writeHead(500, { 'content-type': 'application/json' });
    response.end('{"error":{"message":"synthetic fixture failure"}}');
  }
});

function launch(extra) {
  const child = spawn(args.get('server'), ['--data-dir', dataDir, ...extra], {
    windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
  });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    serviceLogs += chunk;
    if (serviceLogs.length > (2 << 20)) child.kill();
  });
  return child;
}

async function startService() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  assert.notEqual(port, 8787);
  origin = `http://127.0.0.1:${port}`;
  service = launch(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let attempt = 0; attempt < 100; attempt++) {
    if (service.exitCode !== null) throw new Error('service exited before readiness');
    try { if ((await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* retry */ }
    await delay(100);
  }
  throw new Error('service readiness timeout');
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
  return lines.slice(1).filter(Boolean).map(line => Object.fromEntries(line.split(',').map((value, index) => [header[index], value])));
}

async function ledger(route, before, expectedStatus) {
  const prior = new Set(before.map(row => row.id));
  let added = [];
  for (let index = 0; index < 80; index++) {
    added = (await usageRows(route.id)).filter(row => !prior.has(row.id));
    if (added.length === 1 && added[0].status !== 'pending') break;
    await delay(100);
  }
  assert.equal(added.length, 1, `${route.id}: parent count`);
  assert.equal(added[0].status, expectedStatus, `${route.id}: parent status`);
  assert.equal(Number(added[0].attempt_count), 1, `${route.id}: attempt count`);
  const attempts = (await admin(`/usage/requests/${added[0].id}/attempts`)).items;
  assert.equal(attempts.length, 1); assert.equal(attempts[0].status, expectedStatus);
  const exported = await exportRows(route.id); assert.equal(exported.length, 1);
  assert.equal(exported[0].protocol, route.wireName, `${route.id}: raw upstream protocol`);
  return { parent_requests: 1, attempts: 1, upstream_calls: 1, request_status: expectedStatus,
    upstream_protocol: exported[0].protocol,
    usage: { input: attempts[0].input_tokens, output: attempts[0].output_tokens,
      cache_read: attempts[0].cache_read_tokens, cache_write: attempts[0].cache_write_tokens } };
}

function requestFor(route) {
  const declaration = { name: 'lookup', description: 'Synthetic lookup',
    parameters: { type: 'object', properties: { q: { type: 'string' } }, required: ['q'] } };
  if (route.client === 'gemini') {
    const contents = route.scenario === 'result' ? [
      { role: 'user', parts: [{ text: promptMarker }] },
      { role: 'model', parts: [{ functionCall: { id: 'call_history', name: 'lookup', args: { q: 'x' } } }] },
      { role: 'user', parts: [{ functionResponse: { id: 'call_history', name: 'lookup', response: { output: resultMarker } } }] },
    ] : [{ role: 'user', parts: [{ text: promptMarker }] }];
    return { url: `${origin}/v1beta/models/${route.id}:streamGenerateContent?alt=sse`, body: { contents,
      ...(route.scenario === 'tool' ? { tools: [{ functionDeclarations: [declaration] }] } : {}) } };
  }
  const input = route.scenario === 'result' ? [
    { type: 'message', role: 'user', content: [{ type: 'input_text', text: promptMarker }] },
    { type: 'function_call', id: 'fc_history', call_id: 'call_history', name: 'lookup',
      arguments: '{"q":"x"}', status: 'completed' },
    { type: 'function_call_output', call_id: 'call_history', output: JSON.stringify({ output: resultMarker }) },
  ] : promptMarker;
  return { url: `${origin}/v1/responses`, body: { model: route.id, stream: true, store: false,
    max_output_tokens: 64, input,
    ...(route.scenario === 'tool' || route.scenario === 'generated-call' ?
      { tools: [{ type: 'function', name: declaration.name, description: declaration.description,
        parameters: declaration.parameters }] } : {}) } };
}

async function readBody(response, observeText) {
  if (!response.body) return '';
  const reader = response.body.getReader(), decoder = new TextDecoder(); let text = '';
  for (;;) {
    const { value, done } = await reader.read(); if (done) break;
    text += decoder.decode(value, { stream: true }); if (observeText) observeText(text);
  }
  return text + decoder.decode();
}

function keyFor(route) { return route.client === 'gemini' ? employeeKeys[0] : employeeKeys[1]; }

async function send(route, options = {}) {
  const before = await usageRows(route.id), beforeCalls = calls.length, request = requestFor(route);
  const controller = new AbortController(); let response, text = '';
  try {
    response = await fetch(request.url, { method: 'POST', headers: { authorization: `Bearer ${options.key ?? keyFor(route)}`,
      'content-type': 'application/json' }, body: JSON.stringify(request.body), signal: controller.signal });
    text = await readBody(response, current => {
      if (options.observeText) options.observeText(current);
      if (options.abortOn && current.includes(options.abortOn)) controller.abort();
    });
  } catch (error) {
    if (!options.abortOn || error.name !== 'AbortError') throw error;
  }
  assert.equal(calls.length, beforeCalls + 1, `${route.id}: dispatch cardinality`);
  return { response, text, before };
}

async function waitUntil(predicate, message, timeout = 3000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (predicate()) return;
    await delay(20);
  }
  assert.fail(message);
}

function assertUsage(actual, expected, label) { assert.deepEqual(actual, expected, label); }

function assertNoSuccess(route, text) {
  if (route.client === 'gemini') {
    assert.equal(text.includes('"finishReason":"STOP"'), false);
    assert.equal(text.includes('"finishReason":"MAX_TOKENS"'), false);
  } else {
    assert.equal(text.includes('event: response.completed'), false);
    assert.equal(text.includes('event: response.incomplete'), false);
  }
}

async function slowReader(route) {
  const before = await usageRows(route.id), beforeCalls = calls.length;
  const request = requestFor(route), target = new URL(request.url), body = JSON.stringify(request.body);
  const socket = net.createConnection({ host: target.hostname, port: Number(target.port) });
  await once(socket, 'connect');
  socket.write(`POST ${target.pathname}${target.search} HTTP/1.1\r\nHost: ${target.host}\r\n` +
    `Authorization: Bearer ${keyFor(route)}\r\nContent-Type: application/json\r\nContent-Length: ${Buffer.byteLength(body)}\r\n` +
    `Connection: keep-alive\r\n\r\n${body}`);
  socket.pause(); const started = Date.now();
  for (let index = 0; index < 400 && !upstreamClosed.has(route.id); index++) await delay(100);
  const elapsed = Date.now() - started; socket.destroy();
  assert.ok(upstreamClosed.has(route.id), `${route.id}: slow reader did not release upstream`);
  assert.ok(elapsed >= 25_000 && elapsed < 40_000, `${route.id}: slow reader deadline ${elapsed}ms`);
  assert.equal(calls.length, beforeCalls + 1);
  return { ...(await ledger(route, before, 'cancelled')), upstream_released_ms: elapsed };
}

async function filesUnder(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const child = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await filesUnder(child));
    else if (entry.isFile()) result.push(child);
  }
  return result;
}

async function sha256(file) { return createHash('sha256').update(await readFile(file)).digest('hex'); }

const results = [];
try {
  upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
  const init = launch(['--init']); init.stdin.end(`${password}\n`); assert.equal((await once(init, 'exit'))[0], 0);
  await startService();
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  const responsesUpstream = await admin('/upstreams', 'POST', { name: 'Synthetic Responses',
    provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: openAISecret });
  const geminiUpstream = await admin('/upstreams', 'POST', { name: 'Synthetic Gemini', provider_kind: 'gemini-api-key',
    endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: geminiSecret });
  for (const route of routeSpecs) await admin('/models', 'POST', { id: route.id,
    upstream_id: route.wire === 'responses' ? responsesUpstream.id : geminiUpstream.id,
    upstream_model: route.upstreamModel, wire_protocol: route.wireName });
  const employee = await admin('/employees', 'POST', { name: 'Gemini Responses stream acceptance' });
  const geminiIssued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'Gemini-only acceptance',
    operation_id: randomUUID(), policy: { protocol_mode: 'selected', protocols: ['gemini-generate-content'],
      model_mode: 'all', models: [] } });
  const responsesIssued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'Responses-only acceptance',
    operation_id: randomUUID(), policy: { protocol_mode: 'selected', protocols: ['openai-responses'],
      model_mode: 'all', models: [] } });
  employeeKeys.push(geminiIssued.key, responsesIssued.key);

  for (const id of ['g2r-text', 'r2g-text']) {
    const route = byID.get(id), gate = newGate(`${id}:before-terminal`); let observed = '';
    const pending = send(route, { observeText: current => { observed = current; } });
    await gate.hit;
    const marker = route.client === 'gemini' ? '"text":"G2R_A"' : 'event: response.output_text.delta';
    await waitUntil(() => observed.includes(marker), `${id}: no generation-time semantic output`);
    if (route.client === 'gemini') assert.equal(observed.includes('"finishReason":"STOP"'), false);
    else assert.equal(observed.includes('event: response.completed'), false);
    gate.release(); const sent = await pending; controls.delete(`${id}:before-terminal`);
    assert.equal(sent.response.status, 200);
    const frames = parseSSE(sent.text);
    if (route.client === 'gemini') {
      const texts = findObjects(frames.map(frame => frame.value), value => typeof value.text === 'string').map(value => value.text);
      assert.deepEqual(texts, ['G2R_A', 'G2R_B']);
      const terminal = frames.filter(frame => frame.value?.candidates?.[0]?.finishReason === 'STOP'); assert.equal(terminal.length, 1);
      assert.deepEqual(terminal[0].value.usageMetadata,
        { promptTokenCount: 8, candidatesTokenCount: 4, totalTokenCount: 12, cachedContentTokenCount: 2 });
    } else {
      assert.equal((sent.text.match(/"delta":"R2G_A"/g) ?? []).length, 1);
      assert.equal((sent.text.match(/"delta":"R2G_B"/g) ?? []).length, 1);
      assert.equal((sent.text.match(/event: response.completed/g) ?? []).length, 1);
      const terminal = frames.find(frame => frame.name === 'response.completed');
      assert.deepEqual(terminal.value.response.usage, { input_tokens: 9, output_tokens: 5, total_tokens: 14,
        input_tokens_details: { cached_tokens: 3 } });
    }
    const facts = await ledger(route, sent.before, 'succeeded');
    assertUsage(facts.usage, route.wire === 'responses' ?
      { input: null, output: '4', cache_read: '2', cache_write: null } :
      { input: '6', output: '5', cache_read: '3', cache_write: '0' }, `${id}: raw usage`);
    results.push({ scenario: id, status: 'PASS', streaming: 'LIVE', first_semantic_before_upstream_eof: true, ...facts });
  }

  {
    const route = byID.get('r2g-late'), beforeIdentity = newGate(`${route.id}:before-identity`),
      afterIdentity = newGate(`${route.id}:after-identity`); let observed = '';
    const pending = send(route, { observeText: current => { observed = current; } });
    await beforeIdentity.hit; await delay(200);
    assert.equal(observed.includes('response.created'), false); assert.equal(observed.includes('LATE_FIRST'), false);
    beforeIdentity.release(); await afterIdentity.hit;
    await waitUntil(() => observed.includes('LATE_FIRST') && observed.includes('LATE_SECOND'),
      'late identity did not release buffered actions');
    assert.ok(observed.indexOf('LATE_FIRST') < observed.indexOf('LATE_SECOND'));
    assert.equal((observed.match(/LATE_FIRST/g) ?? []).length, 1);
    assert.equal((observed.match(/LATE_SECOND/g) ?? []).length, 1);
    assert.equal(observed.includes('event: response.completed'), false);
    afterIdentity.release(); const sent = await pending;
    controls.delete(`${route.id}:before-identity`); controls.delete(`${route.id}:after-identity`);
    const deltas = parseSSE(sent.text).filter(frame => frame.name === 'response.output_text.delta')
      .map(frame => frame.value?.delta);
    assert.deepEqual(deltas, ['LATE_FIRST', 'LATE_SECOND']);
    assert.equal((sent.text.match(/event: response.completed/g) ?? []).length, 1);
    const facts = await ledger(route, sent.before, 'succeeded');
    assertUsage(facts.usage, { input: '6', output: '5', cache_read: '3', cache_write: '0' }, 'late identity usage');
    results.push({ scenario: route.id, status: 'PASS', pre_identity_semantic_events: 0,
      released_in_wire_order_once: true, terminal_after_clean_eof: true, ...facts });
  }

  for (const id of ['g2r-drain', 'r2g-drain']) {
    const route = byID.get(id), gate = newGate(`${id}:after-terminal`); let observed = '';
    const pending = send(route, { observeText: current => { observed = current; } });
    await gate.hit; await delay(200);
    assert.ok(observed.includes(route.client === 'gemini' ? 'G2R_A' : 'R2G_A'));
    if (route.client === 'gemini') assert.equal(observed.includes('"finishReason":"STOP"'), false);
    else assert.equal(observed.includes('event: response.completed'), false);
    gate.release(); const sent = await pending; controls.delete(`${id}:after-terminal`);
    assert.ok(sent.text.includes(route.client === 'gemini' ? '"finishReason":"STOP"' : 'event: response.completed'));
    results.push({ scenario: id, status: 'PASS', terminal_withheld_until_clean_eof: true,
      ...(await ledger(route, sent.before, 'succeeded')) });
  }

  for (const id of ['g2r-tool', 'r2g-tool', 'r2g-generated-call']) {
    const route = byID.get(id), sent = await send(route); assert.equal(sent.response.status, 200);
    const frames = parseSSE(sent.text);
    if (route.client === 'gemini') {
      const callsFound = findObjects(frames.map(frame => frame.value), value => value.functionCall).map(value => value.functionCall);
      assert.equal(callsFound.length, 1); assert.deepEqual(callsFound[0], { id: 'call_rsp', name: 'lookup', args: { q: 'x' } });
    } else {
      const done = frames.find(frame => frame.name === 'response.output_item.done' && frame.value?.item?.type === 'function_call');
      assert.ok(done); assert.equal(done.value.item.name, 'lookup'); assert.equal(done.value.item.arguments, '{"q":"x"}');
      if (route.scenario === 'tool') assert.equal(done.value.item.call_id, 'call_gem');
      else assert.ok(typeof done.value.item.call_id === 'string' && done.value.item.call_id.length > 0);
      assert.equal((sent.text.match(/event: response.function_call_arguments.delta/g) ?? []).length, 1);
    }
    results.push({ scenario: id, status: 'PASS', complete_function_call_once: true,
      call_id: route.scenario === 'generated-call' ? 'generated_nonempty' : 'preserved',
      ...(await ledger(route, sent.before, 'succeeded')) });
  }

  for (const id of ['g2r-result', 'r2g-result']) {
    const route = byID.get(id), sent = await send(route); assert.equal(sent.response.status, 200);
    assert.ok(sent.text.includes(route.client === 'gemini' ? 'G2R_RESULT' : 'R2G_RESULT'));
    results.push({ scenario: id, status: 'PASS', next_turn_function_response_preserved: true,
      ...(await ledger(route, sent.before, 'succeeded')) });
  }

  for (const id of ['g2r-incomplete', 'r2g-incomplete']) {
    const route = byID.get(id), sent = await send(route); assert.equal(sent.response.status, 200);
    assert.ok(sent.text.includes(route.client === 'gemini' ? '"finishReason":"MAX_TOKENS"' : 'event: response.incomplete'));
    if (route.client === 'gemini') assert.equal(sent.text.includes('"finishReason":"STOP"'), false);
    else assert.equal(sent.text.includes('event: response.completed'), false);
    results.push({ scenario: id, status: 'PASS', terminal_outcome: 'incomplete',
      ...(await ledger(route, sent.before, 'interrupted')) });
  }

  for (const id of ['g2r-unknown', 'r2g-unknown']) {
    const route = byID.get(id), sent = await send(route); assert.equal(sent.response.status, 200);
    if (route.client === 'gemini') assert.equal(sent.text.includes('usageMetadata'), false);
    else {
      const terminal = parseSSE(sent.text).find(frame => frame.name === 'response.completed');
      assert.ok(terminal); assert.equal(Object.hasOwn(terminal.value.response, 'usage'), false);
    }
    const facts = await ledger(route, sent.before, 'succeeded');
    assertUsage(facts.usage, { input: null, output: null, cache_read: null, cache_write: null }, `${id}: unknown usage`);
    results.push({ scenario: id, status: 'PASS', unknown_usage_not_zero: true, ...facts });
  }

  {
    const route = byID.get('r2g-missing-identity'), sent = await send(route);
    assert.equal(sent.response.status, 502); assert.equal(sent.text.includes('response.created'), false);
    assert.equal(sent.text.includes('NO_IDENTITY'), false); assertNoSuccess(route, sent.text);
    results.push({ scenario: route.id, status: 'PASS', synthesized_identity: false,
      ...(await ledger(route, sent.before, 'failed')) });
  }

  for (const id of ['g2r-failed', 'r2g-failed']) {
    const route = byID.get(id), sent = await send(route); assertNoSuccess(route, sent.text);
    assert.equal(sent.text.includes(privateError), false);
    results.push({ scenario: id, status: 'PASS', failure_redacted: true,
      ...(await ledger(route, sent.before, 'failed')) });
  }

  for (const id of ['g2r-duplicate', 'r2g-badtail', 'r2g-duplicate']) {
    const route = byID.get(id), sent = await send(route); assertNoSuccess(route, sent.text);
    assert.equal(sent.text.includes(privateError), false);
    results.push({ scenario: id, status: 'PASS', invalid_tail_failed_closed: true,
      ...(await ledger(route, sent.before, 'failed')) });
  }

  for (const id of ['g2r-missing', 'r2g-missing']) {
    const route = byID.get(id), sent = await send(route); assertNoSuccess(route, sent.text);
    results.push({ scenario: id, status: 'PASS', missing_terminal_failed_closed: true,
      ...(await ledger(route, sent.before, 'interrupted')) });
  }

  for (const id of ['g2r-cancel', 'r2g-cancel']) {
    const route = byID.get(id), sent = await send(route, { abortOn: route.client === 'gemini' ? 'CANCEL_G2R' : 'CANCEL_R2G' });
    for (let index = 0; index < 80 && !upstreamClosed.has(route.id); index++) await delay(100);
    assert.ok(upstreamClosed.has(route.id), `${id}: cancellation did not reach upstream`);
    results.push({ scenario: id, status: 'PASS', client_close_released_upstream: true, no_replay: true,
      ...(await ledger(route, sent.before, 'cancelled')) });
  }

  for (const id of ['g2r-slow', 'r2g-slow']) {
    results.push({ scenario: id, status: 'PASS', no_replay: true, ...(await slowReader(byID.get(id))) });
  }

  for (const [id, deniedKey] of [['g2r-text', responsesIssued.key], ['r2g-text', geminiIssued.key]]) {
    const route = byID.get(id), beforeRows = await usageRows(route.id), beforeCalls = calls.length, request = requestFor(route);
    const response = await fetch(request.url, { method: 'POST', headers: { authorization: `Bearer ${deniedKey}`,
      'content-type': 'application/json' }, body: JSON.stringify(request.body), signal: AbortSignal.timeout(5000) });
    await response.text(); assert.equal(response.status, 403);
    assert.equal(calls.length, beforeCalls); assert.equal((await usageRows(route.id)).length, beforeRows.length);
  }
  results.push({ scenario: 'client-protocol-policy', status: 'PASS', cross_protocol_authority_inherited: false,
    denied_before_parent_attempt_and_dispatch: true });

  assert.deepEqual(fixtureErrors, []);
  for (const secret of [...employeeKeys, openAISecret, geminiSecret, password, promptMarker, resultMarker, privateError]) {
    assert.equal(serviceLogs.includes(secret), false, 'sensitive value appeared in service logs');
  }
  await stopService();
  const persisted = await filesUnder(dataDir);
  for (const file of persisted) {
    const bytes = await readFile(file);
    for (const secret of [...employeeKeys, openAISecret, geminiSecret, password, promptMarker, resultMarker, privateError]) {
      assert.equal(bytes.includes(Buffer.from(secret)), false, `plaintext sensitive value persisted in ${path.basename(file)}`);
    }
  }
  console.log(JSON.stringify({ status: 'PASS', boundary: 'gemini_responses_cross_protocol_sse_real_process',
    source_commit: args.get('source-commit'), server_sha256: await sha256(args.get('server')),
    platform: { os: os.platform(), release: os.release(), arch: os.arch() },
    actual_cli: { status: 'UNTESTED', reason: 'no_installed_gemini_cli_and_no_proven_representable_codex_request_shape' },
    results }, null, 2));
} finally {
  await stopService(); upstream.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  const resolved = path.resolve(root); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-gemini-responses-stream-'));
  await rm(resolved, { recursive: true, force: true, maxRetries: 4, retryDelay: 250 });
}
