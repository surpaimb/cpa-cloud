// Independent fixed-binary process verification for Key account-group narrowing.
// Provenance: independently authored from CPA Cloud specifications and public HTTP/JSON protocol behavior.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { access, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, expectedSHA256Argument] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable path required');
assert.match(expectedSHA256Argument ?? '', /^[0-9a-f]{64}$/i, 'Expected SHA-256 required');
const expectedSHA256 = expectedSHA256Argument.toLowerCase();
const actualSHA256 = createHash('sha256').update(await readFile(executable)).digest('hex');
assert.equal(actualSHA256, expectedSHA256);

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-key-group-'));
const dataDir = path.join(root, 'data');
const password = `admin-${randomBytes(24).toString('hex')}`;
const prompt = `group-prompt-${randomUUID()}`;
const credentialA = `group-a-${randomUUID()}`;
const credentialB = `group-b-${randomUUID()}`;
const credentialGeminiA = `gemini-group-a-${randomUUID()}`;
const credentialGeminiB = `gemini-group-b-${randomUUID()}`;
const credentialToAccount = new Map([
  [credentialA, 'openai-A'], [credentialB, 'openai-B'],
  [credentialGeminiA, 'gemini-A'], [credentialGeminiB, 'gemini-B'],
]);
const secrets = [password, prompt, credentialA, credentialB, credentialGeminiA, credentialGeminiB];

let child;
let origin;
let cookie = '';
let csrf = '';
let logs = '';
let blockAccountA = false;
let blockedResolve;
let blockedPromise;
let releaseBlocked;
const calls = [];
const mockErrors = [];

const mock = http.createServer(async (request, response) => {
  try {
    let raw = '';
    for await (const chunk of request) raw += chunk;
    const body = JSON.parse(raw);
    const authorization = request.headers.authorization ?? '';
    const credential = request.headers['x-goog-api-key'] ?? authorization.replace(/^Bearer /, '');
    const account = credentialToAccount.get(credential);
    assert.ok(account, 'Unexpected upstream credential');
    for (const name of ['x-api-key', 'cookie', 'origin', 'x-csrf-token']) {
      assert.equal(request.headers[name], undefined, `Employee/admin header ${name} reached upstream`);
    }
    calls.push({ account, url: request.url });
    if (account === 'openai-A' && blockAccountA) {
      blockAccountA = false;
      blockedResolve();
      await new Promise(resolve => { releaseBlocked = resolve; });
    }
    response.writeHead(200, { 'Content-Type': 'application/json' });
    if (account.startsWith('gemini-')) {
      assert.equal(authorization, '');
      assert.equal(request.url, '/v1beta/models/actual-gemini:generateContent');
      response.end(JSON.stringify({
        candidates: [{ content: { role: 'model', parts: [{ text: 'ok' }] }, finishReason: 'STOP', index: 0 }],
        usageMetadata: { promptTokenCount: 2, candidatesTokenCount: 1, totalTokenCount: 3 },
      }));
      return;
    }
    assert.equal(request.headers['x-goog-api-key'], undefined);
    assert.equal(request.url, '/v1/responses');
    assert.equal(body.model, 'actual-group-model');
    response.end(JSON.stringify({
      id: `resp_group_${calls.length}`,
      object: 'response',
      created_at: 1,
      model: body.model,
      status: 'completed',
      output: [{
        id: `msg_group_${calls.length}`,
        type: 'message',
        status: 'completed',
        role: 'assistant',
        content: [{ type: 'output_text', text: 'ok', annotations: [] }],
      }],
      usage: { input_tokens: 2, output_tokens: 1, total_tokens: 3 },
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
  assert.notEqual(port, 8787);
  return `http://127.0.0.1:${port}`;
}

function launch(args) {
  const proc = spawn(executable, ['--data-dir', dataDir, ...args], {
    windowsHide: true,
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  for (const stream of [proc.stdout, proc.stderr]) {
    stream.on('data', chunk => {
      logs += chunk.toString();
      if (logs.length > (1 << 20)) proc.kill('SIGKILL');
    });
  }
  return proc;
}

async function initialize() {
  child = launch(['--init']);
  const exited = once(child, 'exit');
  child.stdin.end(password + '\n');
  assert.equal((await exited)[0], 0, 'Initialization failed');
  child = undefined;
}

async function start() {
  assert.equal(child, undefined);
  origin = await reserveOrigin();
  child = launch([
    '--listen', new URL(origin).host,
    '--allow-loopback-upstream',
    '--shutdown-on-stdin-eof',
  ]);
  for (let attempt = 0; attempt < 120; attempt++) {
    if (child.exitCode !== null) throw new Error(`Service exited before readiness: ${logs.slice(-2000)}`);
    try {
      const ready = await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) });
      if (ready.ok) {
        cookie = '';
        csrf = '';
        const login = await admin('/sessions', 'POST', { username: 'admin', password });
        csrf = login.csrf_token;
        return;
      }
    } catch { /* isolated service is starting */ }
    await delay(100);
  }
  throw new Error('Service readiness timeout');
}

async function stop() {
  if (!child || child.exitCode !== null || child.signalCode !== null) {
    child = undefined;
    return;
  }
  const current = child;
  child = undefined;
  const exited = once(current, 'exit');
  current.stdin.end();
  const timer = setTimeout(() => current.kill('SIGKILL'), 8000);
  try {
    assert.equal((await exited)[0], 0, 'Service did not stop cleanly');
  } finally {
    clearTimeout(timer);
  }
}

async function admin(route, method = 'GET', body, expected = 200) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method,
    headers: { Cookie: cookie, Origin: origin, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10000),
  });
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  const text = await response.text();
  assert.equal(response.status, expected, `${method} ${route} returned ${response.status}: ${text.slice(0, 500)}`);
  return text ? JSON.parse(text) : {};
}

const protocolRequests = {
  chat: key => ({
    route: '/v1/chat/completions',
    headers: { Authorization: `Bearer ${key}` },
    body: { model: 'group-model', messages: [{ role: 'user', content: prompt }] },
  }),
  responses: key => ({
    route: '/v1/responses',
    headers: { Authorization: `Bearer ${key}` },
    body: { model: 'group-model', input: prompt },
  }),
  messages: key => ({
    route: '/v1/messages',
    headers: { 'X-API-Key': key, 'Anthropic-Version': '2023-06-01' },
    body: { model: 'group-model', max_tokens: 16, messages: [{ role: 'user', content: prompt }] },
  }),
  gemini: key => ({
    route: '/v1beta/models/gemini-model:generateContent',
    headers: { 'X-Goog-Api-Key': key },
    body: { contents: [{ role: 'user', parts: [{ text: prompt }] }] },
  }),
};

async function employeeRequest(protocol, key, timeout = 10000) {
  const input = protocolRequests[protocol](key);
  const response = await fetch(origin + input.route, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...input.headers },
    body: JSON.stringify(input.body),
    signal: AbortSignal.timeout(timeout),
  });
  const text = await response.text();
  let json;
  try { json = JSON.parse(text); } catch { json = undefined; }
  return {
    status: response.status,
    text,
    json,
    requestID: response.headers.get('x-request-id'),
  };
}

async function directory(key, gemini = false) {
  const response = await fetch(origin + (gemini ? '/v1beta/models' : '/v1/models'), {
    headers: gemini ? { 'X-Goog-Api-Key': key } : { Authorization: `Bearer ${key}` },
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(response.status, 200);
  return response.json();
}

function policy(groupMode, groupIDs) {
  return {
    protocol_mode: 'all', protocols: [],
    model_mode: 'all', models: [],
    source_mode: 'all', source_cidrs: [],
    account_group_mode: groupMode, account_group_ids: groupIDs,
  };
}

function poolItem(upstreamID, channelID, priority) {
  return {
    upstream_id: upstreamID,
    upstream_model: 'actual-group-model',
    wire_protocol: 'openai-responses',
    priority,
    weight: 1,
    max_concurrency: 1,
    channel_id: channelID,
  };
}

function geminiPoolItem(upstreamID, channelID, priority) {
  return {
    upstream_id: upstreamID,
    upstream_model: 'actual-gemini',
    wire_protocol: 'gemini-generate-content',
    priority,
    weight: 1,
    max_concurrency: 1,
    channel_id: channelID,
  };
}

function database() {
  return new DatabaseSync(path.join(dataDir, 'cpa-cloud.db'), { readOnly: true });
}

function attemptCount(db, requestID) {
  return db.prepare('SELECT COUNT(*) AS n FROM accounting_attempts WHERE request_id=?').get(requestID).n;
}

function assertDenied(result, label) {
  assert.equal(result.status, 503, `${label} status=${result.status} body=${result.text.slice(0, 300)}`);
  assert.ok(result.requestID, `${label} missing request ID`);
  assert.ok(!result.text.includes(prompt), `${label} echoed prompt`);
}

async function scanFiles(directoryPath, needles) {
  for (const entry of await readdir(directoryPath, { withFileTypes: true })) {
    const filename = path.join(directoryPath, entry.name);
    if (entry.isDirectory()) {
      await scanFiles(filename, needles);
    } else if (entry.isFile()) {
      const bytes = await readFile(filename);
      for (const value of needles) assert.ok(!bytes.includes(Buffer.from(value)), `Plaintext ${value.slice(0, 12)} persisted in ${filename}`);
    }
  }
}

let keyAllowed;
let keyCross;
let keyEmpty;
let groupA;
let groupB;
let groupC;
let channelA;
let channelB;
let upstreamA;
let upstreamB;
let upstreamGeminiA;
let upstreamGeminiB;
let poolRevision = 0;
let geminiPoolRevision = 0;
const deniedRequestIDs = [];
const allowedRequestIDs = [];

try {
  await access(executable);
  mock.listen(0, '127.0.0.1');
  await once(mock, 'listening');
  await initialize();
  await start();

  const status = await admin('/system/status');
  assert.equal(status.features.key_account_group_policy, true);
  const endpointBase = `http://127.0.0.1:${mock.address().port}`;
  const endpoint = endpointBase + '/v1';
  upstreamA = await admin('/upstreams', 'POST', {
    name: 'Group A account', provider_kind: 'openai-compatible', endpoint, api_key: credentialA,
  }, 201);
  upstreamB = await admin('/upstreams', 'POST', {
    name: 'Group B account', provider_kind: 'openai-compatible', endpoint, api_key: credentialB,
  }, 201);
  upstreamGeminiA = await admin('/upstreams', 'POST', {
    name: 'Gemini group A account', provider_kind: 'gemini-api-key', endpoint: endpointBase, api_key: credentialGeminiA,
  }, 201);
  upstreamGeminiB = await admin('/upstreams', 'POST', {
    name: 'Gemini group B account', provider_kind: 'gemini-api-key', endpoint: endpointBase, api_key: credentialGeminiB,
  }, 201);
  await admin('/models', 'POST', {
    id: 'group-model', upstream_id: upstreamA.id, upstream_model: 'actual-group-model', wire_protocol: 'openai-responses',
  }, 201);
  await admin('/models', 'POST', {
    id: 'gemini-model', upstream_id: upstreamGeminiA.id, upstream_model: 'actual-gemini', wire_protocol: 'gemini-generate-content',
  }, 201);
  groupA = await admin('/account-groups', 'POST', { name: 'Group A' }, 201);
  groupB = await admin('/account-groups', 'POST', { name: 'Group B' }, 201);
  groupC = await admin('/account-groups', 'POST', { name: 'Group C no route' }, 201);
  channelA = await admin('/channels', 'POST', { name: 'Channel A', group_id: groupA.id }, 201);
  channelB = await admin('/channels', 'POST', { name: 'Channel B', group_id: groupB.id }, 201);
  const itemsAllowed = [poolItem(upstreamA.id, channelA.id, 10), poolItem(upstreamB.id, channelB.id, 1)];
  const pool = await admin('/models/group-model/accounts', 'PUT', { expected_revision: poolRevision, items: itemsAllowed });
  poolRevision = pool.revision;
  assert.equal(poolRevision, 1);
  const geminiItemsAllowed = [geminiPoolItem(upstreamGeminiA.id, channelA.id, 10), geminiPoolItem(upstreamGeminiB.id, channelB.id, 1)];
  const geminiPool = await admin('/models/gemini-model/accounts', 'PUT', { expected_revision: geminiPoolRevision, items: geminiItemsAllowed });
  geminiPoolRevision = geminiPool.revision;
  assert.equal(geminiPoolRevision, 1);

  const employee = await admin('/employees', 'POST', { name: 'Key group process verification' }, 201);
  async function issue(name, groupMode, groupIDs) {
    const issued = await admin(`/employees/${employee.id}/keys`, 'POST', {
      name,
      operation_id: randomUUID(),
      policy: policy(groupMode, groupIDs),
    }, 201);
    secrets.push(issued.key);
    return issued;
  }
  keyAllowed = await issue('selected A', 'selected', [groupA.id]);
  keyCross = await issue('selected C', 'selected', [groupC.id]);
  keyEmpty = await issue('selected empty', 'selected', []);
  assert.deepEqual(keyAllowed.policy.account_group_ids, [groupA.id]);
  assert.equal(keyAllowed.policy.account_group_mode, 'selected');
  assert.ok(keyAllowed.policy.effective_models.includes('group-model'));
  assert.ok(keyAllowed.policy.effective_models.includes('gemini-model'));
  assert.deepEqual(keyCross.policy.effective_models, []);
  assert.deepEqual(keyEmpty.policy.effective_models, []);

  for (const [name, key, visible] of [
    ['allowed', keyAllowed.key, true],
    ['cross', keyCross.key, false],
    ['empty', keyEmpty.key, false],
  ]) {
    const openAI = await directory(key);
    const gemini = await directory(key, true);
    assert.equal(openAI.data.some(item => item.id === 'group-model'), visible, `${name} OpenAI directory`);
    assert.equal(gemini.models.length > 0, visible, `${name} Gemini directory`);
  }

  for (const protocol of Object.keys(protocolRequests)) {
    const result = await employeeRequest(protocol, keyAllowed.key);
    assert.equal(result.status, 200, `${protocol} selected A failed: ${result.text.slice(0, 300)}`);
    assert.ok(result.requestID);
    allowedRequestIDs.push(result.requestID);
    assert.ok(calls.at(-1).account.endsWith('-A'), `${protocol} escaped selected group A`);
  }
  assert.equal(calls.length, 4);

  for (const [label, key] of [['cross-group', keyCross.key], ['selected-empty', keyEmpty.key]]) {
    const before = calls.length;
    for (const protocol of Object.keys(protocolRequests)) {
      const result = await employeeRequest(protocol, key);
      assertDenied(result, `${label}/${protocol}`);
      deniedRequestIDs.push(result.requestID);
    }
    assert.equal(calls.length, before, `${label} reached upstream`);
  }

  await admin(`/upstreams/${upstreamA.id}`, 'PATCH', { expected_revision: 1, enabled: false });
  let before = calls.length;
  const noFallback = await employeeRequest('responses', keyAllowed.key);
  assertDenied(noFallback, 'allowed-group preflight unavailable');
  deniedRequestIDs.push(noFallback.requestID);
  assert.equal(calls.length, before, 'Preflight failure fell back to group B');
  await admin(`/upstreams/${upstreamA.id}`, 'PATCH', { expected_revision: 2, enabled: true });

  blockAccountA = true;
  blockedPromise = new Promise(resolve => { blockedResolve = resolve; });
  const activePromise = employeeRequest('responses', keyAllowed.key, 20000);
  await Promise.race([
    blockedPromise,
    delay(10000).then(() => { throw new Error('Active request did not reach group A upstream'); }),
  ]);
  before = calls.length;
  let queuedSettled = false;
  const queuedPromise = employeeRequest('responses', keyAllowed.key, 20000).then(result => {
    queuedSettled = true;
    return result;
  });
  await delay(250);
  assert.equal(queuedSettled, false, 'Second request did not wait for selected group A capacity');
  assert.equal(calls.length, before, 'Queued request reached upstream before capacity was released');

  const tightenedItems = [poolItem(upstreamA.id, channelB.id, 10), poolItem(upstreamB.id, channelB.id, 1)];
  const tightened = await admin('/models/group-model/accounts', 'PUT', {
    expected_revision: poolRevision,
    items: tightenedItems,
  });
  poolRevision = tightened.revision;
  const queued = await Promise.race([
    queuedPromise,
    delay(10000).then(() => { throw new Error('Queued request did not fail after route→group tightening'); }),
  ]);
  assertDenied(queued, 'final-dispatch mapping tightening');
  deniedRequestIDs.push(queued.requestID);
  assert.equal(calls.length, before, 'Tightened queued request fell back to group B');
  releaseBlocked();
  const active = await activePromise;
  assert.equal(active.status, 200, `Already-dispatched request failed after mapping change: ${active.text.slice(0, 300)}`);
  allowedRequestIDs.push(active.requestID);
  assert.equal(calls.length, before, 'Mapping change replayed the already-dispatched request');

  const restored = await admin('/models/group-model/accounts', 'PUT', {
    expected_revision: poolRevision,
    items: itemsAllowed,
  });
  poolRevision = restored.revision;
  assert.equal(poolRevision, 3);

  await stop();
  await start();
  const persistedPolicy = await admin(`/keys/${keyAllowed.id}/policy`);
  assert.equal(persistedPolicy.account_group_mode, 'selected');
  assert.deepEqual(persistedPolicy.account_group_ids, [groupA.id]);
  assert.ok(persistedPolicy.effective_models.includes('group-model'));
  assert.ok(persistedPolicy.effective_models.includes('gemini-model'));
  assert.ok((await directory(keyAllowed.key)).data.some(item => item.id === 'group-model'));
  assert.ok((await directory(keyAllowed.key, true)).models.length > 0);
  before = calls.length;
  for (const protocol of Object.keys(protocolRequests)) {
    const result = await employeeRequest(protocol, keyAllowed.key);
    assert.equal(result.status, 200, `restart ${protocol} failed: ${result.text.slice(0, 300)}`);
    allowedRequestIDs.push(result.requestID);
    assert.ok(calls.at(-1).account.endsWith('-A'), `restart ${protocol} escaped selected group A`);
  }
  assert.equal(calls.length, before + 4);
  await stop();

  const db = database();
  try {
    for (const requestID of deniedRequestIDs) assert.equal(attemptCount(db, requestID), 0, `Denied request ${requestID} created an attempt`);
    for (const requestID of allowedRequestIDs) assert.equal(attemptCount(db, requestID), 1, `Allowed request ${requestID} attempt count`);
  } finally {
    db.close();
  }
  assert.equal(calls.filter(call => call.account.endsWith('-B')).length, 0, 'Any request reached out-of-policy group B');
  assert.equal(calls.length, 9, 'Unexpected total upstream calls');
  assert.deepEqual(mockErrors, [], `Mock assertions failed:\n${mockErrors.join('\n')}`);
  for (const value of secrets) assert.ok(!logs.includes(value), 'Secret or body appeared in service logs');
  await scanFiles(dataDir, secrets);

  console.log(`PASS key account-group fixed-binary verification sha256=${actualSHA256}`);
  console.log(`PASS allowed=9 denied_zero_attempt=${deniedRequestIDs.length} upstream_A=${calls.length} upstream_B=0 pool_revision=${poolRevision}`);
} finally {
  releaseBlocked?.();
  await stop().catch(() => {});
  mock.closeAllConnections();
  if (mock.listening) await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-key-group-'));
  await rm(resolved, { recursive: true, force: true });
}
