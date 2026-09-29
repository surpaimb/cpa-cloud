// Independent real-process verification for KEY-02 trusted-proxy source resolution.
// Uses only a caller-supplied binary, disposable data, and a synthetic loopback upstream.
// Usage: node scripts/verify-trusted-proxy-xff.mjs <absolute executable> <expected sha256>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { access, mkdtemp, readFile, readdir, rm, stat } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, expectedSHA256] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'An absolute executable path is required');
assert.match(expectedSHA256 ?? '', /^[0-9a-f]{64}$/i, 'An expected SHA-256 is required');
const actualSHA256 = createHash('sha256').update(await readFile(executable)).digest('hex');
assert.equal(actualSHA256, expectedSHA256.toLowerCase(), 'The executable SHA-256 does not match');

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-trusted-proxy-'));
const dataDir = path.join(root, 'data');
const invalidDataDir = path.join(root, 'invalid-startup-must-not-exist');
const password = `admin-${randomBytes(24).toString('hex')}`;
const upstreamSecret = `upstream-${randomUUID()}`;
const prompt = `body-${randomUUID()}`;
const backgroundPromptOne = `background-one-${randomUUID()}`;
const backgroundPromptTwo = `background-two-${randomUUID()}`;
const malformedSource = `malformed-${randomUUID()}`;
const fixedDeniedMessage = 'This request is not allowed for this key.';

let processState;
let allLogs = '';
let cookie = '';
let csrf = '';
let upstreamCalls = [];
let mockErrors = [];
let blockNextResponses = false;
let blockedResolve;
let blockedPromise = new Promise(resolve => { blockedResolve = resolve; });

const mock = http.createServer(async (request, response) => {
  try {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const raw = Buffer.concat(chunks).toString('utf8');
    const body = JSON.parse(raw);
    assert.equal(request.method, 'POST');
    assert.equal(request.headers.authorization, `Bearer ${upstreamSecret}`);
    for (const header of ['x-api-key', 'x-goog-api-key', 'cookie', 'forwarded', 'x-forwarded-for', 'x-real-ip']) {
      assert.equal(request.headers[header], undefined, `Employee/forwarding header ${header} reached upstream`);
    }
    upstreamCalls.push({ url: request.url, body });
    if (request.url === '/v1/responses' && blockNextResponses) {
      blockNextResponses = false;
      blockedResolve();
      await new Promise(resolve => {
        request.once('aborted', resolve);
        request.socket.once('close', resolve);
        response.once('close', resolve);
      });
      return;
    }
    response.writeHead(200, { 'Content-Type': 'application/json' });
    if (request.url === '/v1/chat/completions') {
      assert.equal(body.model, 'native-chat');
      response.end(JSON.stringify({
        id: 'chat_synthetic', object: 'chat.completion', model: body.model,
        choices: [{ index: 0, message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 2, completion_tokens: 1, total_tokens: 3 },
      }));
      return;
    }
    assert.equal(request.url, '/v1/responses');
    assert.equal(body.model, 'native-responses');
    response.end(JSON.stringify({
      id: 'resp_synthetic_upstream', object: 'response', status: 'completed', model: body.model,
      output: [{
        type: 'message', id: 'msg_synthetic', role: 'assistant', status: 'completed',
        content: [{ type: 'output_text', text: 'OK', annotations: [] }],
      }],
      usage: { input_tokens: 2, output_tokens: 1, total_tokens: 3, input_tokens_details: { cached_tokens: 0, cache_write_tokens: 0 } },
    }));
  } catch (error) {
    mockErrors.push(error.stack ?? error.message);
    if (!response.headersSent) response.writeHead(500, { 'Content-Type': 'application/json' });
    response.end('{}');
  }
});

async function reserveOrigin() {
  const probe = http.createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  assert.notEqual(port, 8787, 'The verification selected the forbidden service port');
  return `http://127.0.0.1:${port}`;
}

async function runBinary(args, input = '', expectedCode = 0) {
  const child = spawn(executable, args, { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  let output = '';
  for (const stream of [child.stdout, child.stderr]) {
    stream.on('data', chunk => {
      output += chunk.toString();
      if (output.length > (1 << 20)) child.kill('SIGKILL');
    });
  }
  child.stdin.end(input);
  const [code, signal] = await once(child, 'exit');
  allLogs += output;
  assert.equal(code, expectedCode, `binary exit code=${code} signal=${signal ?? 'none'} output=${output.slice(-2000)}`);
  return output;
}

async function startServer(trustedCIDRs) {
  assert.equal(processState, undefined, 'Server already running');
  const origin = await reserveOrigin();
  const args = [
    '--data-dir', dataDir,
    '--listen', new URL(origin).host,
    '--allow-loopback-upstream',
    '--responses-stateful-resources',
    '--responses-background-tasks',
    '--shutdown-on-stdin-eof',
  ];
  for (const cidr of trustedCIDRs) args.push('--trusted-proxy-cidr', cidr);
  const child = spawn(executable, args, { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  let output = '';
  let launchError;
  child.once('error', error => { launchError = error; });
  for (const stream of [child.stdout, child.stderr]) {
    stream.on('data', chunk => {
      output += chunk.toString();
      if (output.length > (1 << 20)) child.kill('SIGKILL');
    });
  }
  processState = { child, origin, output: () => output };
  cookie = '';
  csrf = '';
  for (let attempt = 0; attempt < 120; attempt++) {
    if (launchError || child.exitCode !== null) throw new Error(`Service failed before readiness: ${launchError ?? output.slice(-2000)}`);
    try {
      const ready = await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) });
      if (ready.ok) return processState;
    } catch { /* isolated service not ready yet */ }
    await delay(100);
  }
  child.kill('SIGKILL');
  throw new Error(`Service readiness timeout: ${output.slice(-2000)}`);
}

async function stopServer() {
  if (!processState) return;
  const current = processState;
  processState = undefined;
  if (current.child.exitCode === null && current.child.signalCode === null) {
    const exited = once(current.child, 'exit');
    current.child.stdin.end();
    const timer = setTimeout(() => current.child.kill('SIGKILL'), 8000);
    try {
      const [code] = await exited;
      assert.equal(code, 0, `Service stopped with code ${code}: ${current.output().slice(-2000)}`);
    } finally {
      clearTimeout(timer);
    }
  }
  allLogs += current.output();
}

async function crashServer() {
  assert.ok(processState, 'No server to crash');
  const current = processState;
  processState = undefined;
  if (current.child.exitCode === null && current.child.signalCode === null) {
    const exited = once(current.child, 'exit');
    current.child.kill('SIGKILL');
    await exited;
  }
  allLogs += current.output();
}

async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(processState.origin + '/admin/api/v1' + route, {
    method,
    headers: { Cookie: cookie, Origin: processState.origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  const text = await response.text();
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}: ${text.slice(0, 500)}`);
  return text ? JSON.parse(text) : {};
}

async function login() {
  const result = await admin('/sessions', 'POST', { username: 'admin', password });
  csrf = result.csrf_token;
  assert.ok(cookie && csrf, 'Admin login did not return session and CSRF state');
}

function employeeHeaders(key, style = 'openai', extra = {}) {
  const headers = { 'Content-Type': 'application/json', ...extra };
  if (style === 'anthropic') {
    headers['X-API-Key'] = key;
    headers['Anthropic-Version'] = '2023-06-01';
  } else if (style === 'gemini') {
    headers['X-Goog-Api-Key'] = key;
  } else {
    headers.Authorization = `Bearer ${key}`;
  }
  return headers;
}

async function employeeRequest({ method = 'POST', route, key, body, style = 'openai', headers = {}, expected }) {
  const response = await fetch(processState.origin + route, {
    method,
    headers: employeeHeaders(key, style, headers),
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  const text = await response.text();
  if (expected !== undefined) assert.equal(response.status, expected, `${method} ${route} returned ${response.status}: ${text.slice(0, 500)}`);
  let json;
  try { json = text ? JSON.parse(text) : undefined; } catch { json = undefined; }
  return { status: response.status, headers: response.headers, text, json, requestID: response.headers.get('x-request-id') };
}

async function rawDuplicateXFF(route, key, first, second) {
  return new Promise((resolve, reject) => {
    const request = http.request(processState.origin + route, {
      method: 'GET',
      headers: {
        Authorization: `Bearer ${key}`,
        'X-Forwarded-For': [first, second],
      },
    }, response => {
      response.resume();
      response.once('end', () => resolve(response.statusCode));
    });
    request.once('error', reject);
    request.end();
  });
}

function assertOpenAIError(result) {
  assert.equal(result.status, 403);
  assert.equal(result.json?.error?.type, 'model_not_allowed');
  assert.equal(result.json?.error?.code, 'model_not_allowed');
  assert.equal(result.json?.error?.message, fixedDeniedMessage);
  assert.ok(result.json?.error?.request_id);
  assert.ok(!result.text.includes(malformedSource));
}

function assertAnthropicError(result) {
  assert.equal(result.status, 403);
  assert.equal(result.json?.type, 'error');
  assert.equal(result.json?.error?.type, 'permission_error');
  assert.equal(result.json?.error?.message, fixedDeniedMessage);
  assert.ok(result.json?.request_id);
  assert.ok(!result.text.includes(malformedSource));
}

function assertGeminiError(result) {
  assert.equal(result.status, 403);
  assert.equal(result.json?.error?.code, 403);
  assert.equal(result.json?.error?.status, 'PERMISSION_DENIED');
  assert.equal(result.json?.error?.message, fixedDeniedMessage);
  assert.ok(!result.text.includes(malformedSource));
}

function openDatabase(readOnly = true) {
  return new DatabaseSync(path.join(dataDir, 'cpa-cloud.db'), { readOnly });
}

function scalar(db, sql, ...parameters) {
  return db.prepare(sql).get(...parameters).n;
}

function assertNoAdmissionRows(db, requestIDs) {
  for (const id of requestIDs) {
    assert.ok(id, 'Rejected request did not expose an X-Request-ID');
    assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM model_requests WHERE id=?', id), 0, `model_requests row exists for ${id}`);
    assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_requests WHERE id=?', id), 0, `accounting_requests row exists for ${id}`);
    assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_attempts WHERE request_id=?', id), 0, `attempt exists for ${id}`);
  }
}

async function waitFor(check, message, timeout = 10000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const value = await check();
    if (value) return value;
    if (Date.now() >= deadline) throw new Error(message);
    await delay(50);
  }
}

async function scanFiles(directory, needles) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const filename = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      await scanFiles(filename, needles);
      continue;
    }
    if (!entry.isFile()) continue;
    const bytes = await readFile(filename);
    for (const needle of needles) {
      assert.ok(!bytes.includes(Buffer.from(needle)), `Sensitive plaintext persisted in ${filename}`);
    }
  }
}

const chatBody = { model: 'chat-model', messages: [{ role: 'user', content: prompt }] };
const responsesBody = { model: 'chat-model', input: prompt };
const messagesBody = { model: 'chat-model', max_tokens: 16, messages: [{ role: 'user', content: prompt }] };
const geminiBody = { contents: [{ role: 'user', parts: [{ text: prompt }] }] };
const trustedSetA = ['127.0.0.1/32', '10.0.0.0/8', '2001:db8:1::/48'];
const trustedSetB = ['127.0.0.1/32', '192.168.0.0/16'];

let directKey;
let clientKey;
let farLeftKey;
let backgroundKey;
let clientPolicyRevision;
let backgroundOne;
let backgroundTwo;
const rejectedRequestIDs = [];

try {
  mock.listen(0, '127.0.0.1');
  await once(mock, 'listening');

  await runBinary([
    '--data-dir', invalidDataDir,
    '--listen', '127.0.0.1:0',
    '--trusted-proxy-cidr', '127.0.0.1/32',
    '--trusted-proxy-cidr', '127.0.0.1',
  ], '', 1);
  await assert.rejects(access(invalidDataDir), 'Invalid trusted-proxy startup mutated its data directory');

  await runBinary(['--data-dir', dataDir, '--init'], password + '\n');
  await startServer([]);
  await login();
  let status = await admin('/system/status');
  assert.equal(status.features.trusted_proxy_source, false);
  const upstream = await admin('/upstreams', 'POST', {
    name: 'Synthetic trusted-proxy upstream', provider_kind: 'openai-compatible',
    endpoint: `http://127.0.0.1:${mock.address().port}/v1`, api_key: upstreamSecret,
  }, 201);
  await admin('/models', 'POST', {
    id: 'chat-model', upstream_id: upstream.id, upstream_model: 'native-chat', wire_protocol: 'openai-chat',
  }, 201);
  await admin('/models', 'POST', {
    id: 'background-model', upstream_id: upstream.id, upstream_model: 'native-responses', wire_protocol: 'openai-responses',
  }, 201);
  const employee = await admin('/employees', 'POST', { name: 'Trusted proxy verification' }, 201);
  async function issueKey(name, sourceCIDRs, sourceMode = 'selected') {
    return admin(`/employees/${employee.id}/keys`, 'POST', {
      name, operation_id: randomUUID(),
      policy: {
        protocol_mode: 'all', protocols: [], model_mode: 'all', models: [],
        source_mode: sourceMode, source_cidrs: sourceCIDRs,
      },
    }, 201);
  }
  directKey = await issueKey('socket peer', ['127.0.0.1/32']);
  clientKey = await issueKey('first untrusted client', ['198.51.100.0/24', '2001:db8:abcd::/48', '192.0.2.0/24']);
  farLeftKey = await issueKey('far-left spoof', ['203.0.113.0/24']);
  backgroundKey = await issueKey('background restart', [], 'all');
  clientPolicyRevision = clientKey.policy.revision;

  let before = upstreamCalls.length;
  for (const headers of [
    { 'X-Forwarded-For': '198.51.100.22' },
    { 'X-Forwarded-For': malformedSource },
    { Forwarded: 'for=198.51.100.22', 'X-Real-IP': '198.51.100.22' },
  ]) {
    const result = await employeeRequest({ route: '/v1/chat/completions', key: directKey.key, body: chatBody, headers, expected: 200 });
    assert.equal(result.json?.choices?.[0]?.message?.content, 'OK');
  }
  assert.equal(upstreamCalls.length, before + 3, 'Default socket-peer requests did not all dispatch');
  await stopServer();

  await startServer(['10.0.0.0/8']);
  await login();
  status = await admin('/system/status');
  assert.equal(status.features.trusted_proxy_source, true);
  before = upstreamCalls.length;
  const untrustedMalformed = await employeeRequest({
    route: '/v1/chat/completions', key: directKey.key, body: chatBody,
    headers: { 'X-Forwarded-For': `${malformedSource}, 198.51.100.22` }, expected: 200,
  });
  assert.equal(untrustedMalformed.json?.choices?.[0]?.message?.content, 'OK');
  assert.equal(await rawDuplicateXFF('/v1/models', directKey.key, malformedSource, '198.51.100.22'), 200);
  assert.equal(upstreamCalls.length, before + 1, 'Untrusted-peer test made an unexpected upstream call');
  await stopServer();

  await startServer(trustedSetA);
  await login();
  status = await admin('/system/status');
  assert.equal(status.features.trusted_proxy_source, true);
  async function chatWith(key, xff, expected = 200, extra = {}) {
    return employeeRequest({
      route: '/v1/chat/completions', key, body: chatBody, expected,
      headers: { ...(xff === undefined ? {} : { 'X-Forwarded-For': xff }), ...extra },
    });
  }

  before = upstreamCalls.length;
  await chatWith(clientKey.key, '198.51.100.22');
  await chatWith(clientKey.key, '203.0.113.66, 198.51.100.22, 10.0.0.7');
  const ignoredFarLeft = await chatWith(farLeftKey.key, '203.0.113.66, 198.51.100.22, 10.0.0.7', 403);
  assertOpenAIError(ignoredFarLeft);
  rejectedRequestIDs.push(ignoredFarLeft.requestID);
  await chatWith(clientKey.key, '198.51.100.22, 2001:db8:1::7');
  await chatWith(clientKey.key, '2001:db8:abcd:ffff::1');
  await chatWith(clientKey.key, '::ffff:192.0.2.44');
  await chatWith(clientKey.key, '198.51.100.255');
  const ipv4Outside = await chatWith(clientKey.key, '198.51.101.0', 403);
  assertOpenAIError(ipv4Outside);
  rejectedRequestIDs.push(ipv4Outside.requestID);
  const ipv6Outside = await chatWith(clientKey.key, '2001:db8:abce::1', 403);
  assertOpenAIError(ipv6Outside);
  rejectedRequestIDs.push(ipv6Outside.requestID);
  await chatWith(clientKey.key, '198.51.100.22', 200, { Forwarded: 'for=203.0.113.9', 'X-Real-IP': '203.0.113.9' });
  assert.equal(upstreamCalls.length, before + 7, 'Trusted single/multi/family-boundary calls did not match expected dispatches');

  const malformedCases = [
    { name: 'missing', headers: {} },
    { name: 'alternatives only', headers: { Forwarded: 'for=198.51.100.22', 'X-Real-IP': '198.51.100.22' } },
    { name: 'malformed', headers: { 'X-Forwarded-For': malformedSource } },
    { name: 'empty member', headers: { 'X-Forwarded-For': '198.51.100.22,,10.0.0.7' } },
    { name: 'too many hops', headers: { 'X-Forwarded-For': Array(65).fill('198.51.100.22').join(',') } },
    { name: 'oversized header', headers: { 'X-Forwarded-For': Array(64).fill(`${' '.repeat(64)}198.51.100.22`).join(',') } },
    { name: 'all trusted', headers: { 'X-Forwarded-For': '10.0.0.7' } },
  ];
  before = upstreamCalls.length;
  for (const test of malformedCases) {
    const result = await employeeRequest({ route: '/v1/chat/completions', key: clientKey.key, body: chatBody, headers: test.headers });
    assert.equal(result.status, 403, `${test.name} trusted chain returned ${result.status}`);
    assertOpenAIError(result);
    rejectedRequestIDs.push(result.requestID);
  }
  assert.equal(await rawDuplicateXFF('/v1/models', clientKey.key, '198.51.100.22', '198.51.100.23'), 403);
  assert.equal(upstreamCalls.length, before, 'Invalid trusted chains reached upstream');

  const protocolCases = [
    { route: '/v1/chat/completions', body: chatBody, style: 'openai', check: assertOpenAIError },
    { route: '/v1/responses', body: responsesBody, style: 'openai', check: assertOpenAIError },
    { route: '/v1/messages', body: messagesBody, style: 'anthropic', check: assertAnthropicError },
    { route: '/v1beta/models/chat-model:generateContent', body: geminiBody, style: 'gemini', check: assertGeminiError },
  ];
  before = upstreamCalls.length;
  for (const test of protocolCases) {
    const result = await employeeRequest({
      route: test.route, key: clientKey.key, body: test.body, style: test.style,
      headers: { 'X-Forwarded-For': malformedSource }, expected: 403,
    });
    test.check(result);
    rejectedRequestIDs.push(result.requestID);
  }
  assert.equal(upstreamCalls.length, before, 'Protocol-specific source failures reached upstream');

  const openAICatalog = await employeeRequest({
    method: 'GET', route: '/v1/models', key: clientKey.key,
    headers: { 'X-Forwarded-For': malformedSource }, expected: 403,
  });
  assertOpenAIError(openAICatalog);
  const geminiCatalog = await employeeRequest({
    method: 'GET', route: '/v1beta/models', key: clientKey.key, style: 'gemini',
    headers: { 'X-Forwarded-For': malformedSource }, expected: 403,
  });
  assertGeminiError(geminiCatalog);
  rejectedRequestIDs.push(openAICatalog.requestID, geminiCatalog.requestID);

  for (const [method, route] of [
    ['GET', '/v1/responses/resp_missing'],
    ['POST', '/v1/responses/resp_missing/cancel'],
    ['DELETE', '/v1/responses/resp_missing'],
  ]) {
    const result = await employeeRequest({
      method, route, key: clientKey.key,
      headers: { 'X-Forwarded-For': malformedSource }, expected: 403,
    });
    assertOpenAIError(result);
    rejectedRequestIDs.push(result.requestID);
  }

  const tightened = await admin(`/keys/${clientKey.id}/policy`, 'PUT', {
    expected_revision: clientPolicyRevision,
    protocol_mode: 'all', protocols: [], model_mode: 'all', models: [],
    source_mode: 'selected', source_cidrs: ['203.0.113.0/24'],
  });
  assert.equal(tightened.revision, clientPolicyRevision + 1);
  assert.deepEqual(tightened.source_cidrs, ['203.0.113.0/24']);
  const stale = await admin(`/keys/${clientKey.id}/policy`, 'PUT', {
    expected_revision: clientPolicyRevision,
    protocol_mode: 'all', protocols: [], model_mode: 'all', models: [],
    source_mode: 'all', source_cidrs: [],
  }, 409);
  assert.equal(stale.error.code, 'revision_conflict');
  before = upstreamCalls.length;
  const tightenedDenied = await chatWith(clientKey.key, '198.51.100.22', 403);
  assertOpenAIError(tightenedDenied);
  rejectedRequestIDs.push(tightenedDenied.requestID);
  assert.equal(upstreamCalls.length, before, 'Tightened source policy reached upstream');

  blockNextResponses = true;
  blockedPromise = new Promise(resolve => { blockedResolve = resolve; });
  const backgroundHeaders = { 'X-Forwarded-For': '198.51.100.22' };
  const firstBackgroundResult = await employeeRequest({
    route: '/v1/responses', key: backgroundKey.key,
    body: { model: 'background-model', input: backgroundPromptOne, background: true },
    headers: backgroundHeaders, expected: 202,
  });
  backgroundOne = firstBackgroundResult.json;
  assert.equal(backgroundOne.status, 'queued');
  await Promise.race([
    blockedPromise,
    delay(10000).then(() => { throw new Error('First background task did not reach the synthetic upstream'); }),
  ]);
  const firstBackgroundCalls = upstreamCalls.filter(call => call.url === '/v1/responses').length;
  assert.equal(firstBackgroundCalls, 1);
  const secondBackgroundResult = await employeeRequest({
    route: '/v1/responses', key: backgroundKey.key,
    body: { model: 'background-model', input: backgroundPromptTwo, background: true },
    headers: backgroundHeaders, expected: 202,
  });
  backgroundTwo = secondBackgroundResult.json;
  assert.equal(backgroundTwo.status, 'queued');
  await delay(250);
  assert.equal(upstreamCalls.filter(call => call.url === '/v1/responses').length, 1, 'The single worker dispatched the queued second task');
  {
    const db = openDatabase();
    try {
      const first = db.prepare('SELECT status,request_id,attempt_id FROM background_tasks WHERE response_id=?').get(backgroundOne.id);
      const second = db.prepare('SELECT status,request_id,attempt_id FROM background_tasks WHERE response_id=?').get(backgroundTwo.id);
      assert.ok(['dispatch_authorized', 'in_progress'].includes(first.status), `first task status=${first.status}`);
      assert.ok(first.attempt_id);
      assert.equal(second.status, 'queued');
      assert.equal(second.attempt_id, null);
    } finally { db.close(); }
  }
  await crashServer();

  await startServer(trustedSetB);
  await waitFor(async () => {
    const first = await employeeRequest({
      method: 'GET', route: `/v1/responses/${backgroundOne.id}`, key: backgroundKey.key,
      headers: backgroundHeaders,
    });
    const second = await employeeRequest({
      method: 'GET', route: `/v1/responses/${backgroundTwo.id}`, key: backgroundKey.key,
      headers: backgroundHeaders,
    });
    return first.status === 200 && second.status === 200
      && first.json?.status === 'interrupted' && second.json?.status === 'interrupted';
  }, 'Background tasks did not become interrupted after the trust revision changed');
  await delay(300);
  assert.equal(upstreamCalls.filter(call => call.url === '/v1/responses').length, 1, 'Restart replayed or newly dispatched a background task');
  await stopServer();

  {
    const db = openDatabase();
    try {
      assertNoAdmissionRows(db, rejectedRequestIDs);
      const first = db.prepare('SELECT status,request_id,attempt_id FROM background_tasks WHERE response_id=?').get(backgroundOne.id);
      const second = db.prepare('SELECT status,request_id,attempt_id FROM background_tasks WHERE response_id=?').get(backgroundTwo.id);
      assert.equal(first.status, 'interrupted');
      assert.ok(first.attempt_id);
      assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_attempts WHERE request_id=?', first.request_id), 1);
      assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_attempt_dispatches d JOIN accounting_attempts a ON a.id=d.attempt_id WHERE a.request_id=?', first.request_id), 1);
      assert.equal(second.status, 'interrupted');
      assert.equal(second.attempt_id, null);
      assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM model_requests WHERE id=?', second.request_id), 0);
      assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_attempts WHERE request_id=?', second.request_id), 0);
      assert.equal(scalar(db, 'SELECT COUNT(*) AS n FROM accounting_attempt_dispatches d JOIN accounting_attempts a ON a.id=d.attempt_id WHERE a.request_id=?', second.request_id), 0);
      const revisions = db.prepare('SELECT pc.source_trust_revision AS revision FROM background_task_policy_contexts pc JOIN background_tasks t ON t.id=pc.task_id WHERE t.response_id IN (?,?) ORDER BY t.response_id').all(backgroundOne.id, backgroundTwo.id);
      assert.equal(revisions.length, 2);
      assert.equal(revisions[0].revision, revisions[1].revision);
      assert.match(revisions[0].revision, /^[0-9a-f]{64}$/);
    } finally { db.close(); }
  }

  assert.deepEqual(mockErrors, [], `Synthetic upstream assertions failed:\n${mockErrors.join('\n')}`);
  const sensitive = [password, upstreamSecret, directKey.key, clientKey.key, farLeftKey.key, backgroundKey.key, prompt, backgroundPromptOne, backgroundPromptTwo];
  for (const value of [...sensitive, malformedSource, '203.0.113.66, 198.51.100.22, 10.0.0.7']) {
    assert.ok(!allLogs.includes(value), 'Sensitive key, credential, body, or forwarding chain appeared in service logs');
  }
  await scanFiles(dataDir, sensitive);

  console.log(`PASS trusted-proxy XFF real-process verification binary_sha256=${actualSHA256}`);
  console.log('PASS default/untrusted ignore, trusted first-untrusted resolution, IPv4/IPv6/mapped boundaries, malformed fail-closed, protocol envelopes, CAS tightening, and restart no-replay');
} finally {
  await stopServer().catch(() => {});
  mock.closeAllConnections();
  if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-trusted-proxy-'));
  await rm(resolved, { recursive: true, force: true });
}
