// Independently authored synthetic process acceptance for
// docs/employee-self-upstream-estimated-cost-summary-contract.md.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir, rm, stat } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, password as adminPassword, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const holdBrowser = process.argv[3] === '--hold-browser';
assert.ok(executable && path.isAbsolute(executable), 'absolute executable required');
const scratch = path.join(root, `self-estimated-cost-${randomUUID()}`);
const feature = '--employee-self-upstream-estimated-cost-summary-enabled';
const selfFlag = '--employee-self-service-enabled';
const route = '/self/api/v1/usage/estimated-cost-summary';
const selfPassword = 'synthetic-estimated-cost-password-123';
const prompt = `synthetic-cost-prompt-${randomUUID()}`;
const answer = `synthetic-cost-answer-${randomUUID()}`;
const upstreamKey = `synthetic-upstream-${randomUUID()}`;
let processHandle, mock;
let partialNext = false;

async function selfCall(target, { method = 'GET', cookie = '', origin = processHandle.origin, expected = 200 } = {}) {
  const response = await fetch(processHandle.origin + target, { method, redirect: 'manual', signal: AbortSignal.timeout(10000),
    headers: { Origin: origin, ...(cookie ? { Cookie: cookie } : {}) } });
  const raw = await response.text();
  assert.equal(response.status, expected, `${method} ${target} status=${response.status}: ${raw.slice(0, 300)}`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  return { body: raw && (response.headers.get('content-type') ?? '').includes('application/json') ? JSON.parse(raw) : raw,
    allow: response.headers.get('allow'), location: response.headers.get('location') };
}

async function request(key) {
  const response = await fetch(processHandle.origin + '/v1/chat/completions', { method: 'POST', signal: AbortSignal.timeout(10000),
    headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: 'public-cost-model', messages: [{ role: 'user', content: prompt }] }) });
  const result = await response.json();
  assert.equal(response.status, 200, JSON.stringify(result));
  assert.equal(result.choices[0].message.content, answer);
}

const rates = (currency, multiplier) => ({ currency, input_per_million_micro: String(1000000 * multiplier),
  output_per_million_micro: String(2000000 * multiplier), cache_read_per_million_micro: String(100000 * multiplier),
  cache_write_per_million_micro: String(1500000 * multiplier) });

try {
  await mkdir(root, { recursive: true });
  for (const mode of [[], ['--init']]) {
    const rejected = path.join(root, `self-cost-rejected-${randomUUID()}`);
    await run(executable, ['--data-dir', rejected, ...mode, feature], adminPassword + '\n', 1);
    await assert.rejects(stat(rejected), { code: 'ENOENT' });
  }
  await initialize(executable, scratch);
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'));
  await selfCall(route, { expected: 404 });
  await selfCall(route + '%3Fx', { expected: 404 });
  await stopServer(processHandle);

  mock = http.createServer(async (req, res) => {
    try {
      assert.equal(req.headers.authorization, `Bearer ${upstreamKey}`);
      for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(req.headers[name], undefined);
      let raw = ''; for await (const chunk of req) raw += chunk;
      const input = JSON.parse(raw);
      assert.equal(input.model, 'actual-cost-model');
      const usage = partialNext ? { prompt_tokens: 100, completion_tokens: 50 } :
        { prompt_tokens: 100, completion_tokens: 50, total_tokens: 150, prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 10 } };
      partialNext = false;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ id: `synthetic-${randomUUID()}`, object: 'chat.completion', model: input.model,
        choices: [{ index: 0, message: { role: 'assistant', content: answer }, finish_reason: 'stop' }], usage }));
    } catch { res.writeHead(500); res.end(); }
  });
  mock.listen(0, '127.0.0.1');
  await once(mock, 'listening');
  assert.notEqual(mock.address().port, 8787);
  const endpoint = `http://127.0.0.1:${mock.address().port}`;

  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [selfFlag, feature]);
  const admin = adminClient(processHandle.origin);
  const adminSession = await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic cost employee' }, 201);
  const other = await admin.request('/employees', 'POST', { name: 'Other cost employee' }, 201);
  const key = await admin.request(`/employees/${employee.id}/keys`, 'POST', { name: 'cost-test', operation_id: randomUUID() }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await fetch(processHandle.origin + '/self/api/v1/enroll', { method: 'POST', signal: AbortSignal.timeout(10000),
    headers: { Origin: processHandle.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
    body: JSON.stringify({ employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret, password: selfPassword }) });
  assert.equal(enrolled.status, 200, await enrolled.clone().text());
  const cookie = enrolled.headers.get('set-cookie')?.split(';')[0];
  assert.ok(cookie);
  assert.equal((await selfCall('/self/api/v1/session', { cookie })).body.features.employee_self_upstream_estimated_cost_summary, true);
  const empty = (await selfCall(route, { cookie })).body;
  assert.deepEqual(empty.attempts, { total: '0', pending: '0', terminal: '0' });
  assert.deepEqual(empty.costs, []);
  await selfCall(route, { expected: 401 });
  await selfCall(route, { cookie: admin.cookie(), expected: 401 });
  await selfCall(route, { cookie, origin: 'http://evil.invalid', expected: 403 });
  // fetch() normalizes a bare '?' away, so send this one request-target literally.
  const bareQuery = await new Promise((resolve, reject) => {
    const destination = new URL(processHandle.origin);
    const call = http.request({ hostname: destination.hostname, port: destination.port, path: route + '?', method: 'GET',
      headers: { Origin: processHandle.origin, Cookie: cookie } }, response => {
      response.resume(); response.on('end', () => resolve(response));
    });
    call.on('error', reject); call.end();
  });
  assert.equal(bareQuery.statusCode, 400);
  assert.equal(bareQuery.headers['cache-control'], 'no-store');
  await selfCall(route + '?employee_id=' + other.id, { cookie, expected: 400 });
  for (const shaped of ['/self//api/v1/usage/estimated-cost-summary', '/self/api/v1/usage%5Cestimated-cost-summary', route + '/', route + '%3Fx', route + '%3Bextra', route + '%2Eextra']) {
    const blocked = await selfCall(shaped, { cookie, expected: 400 });
    assert.equal(blocked.location, null);
  }
  assert.equal((await selfCall(route, { method: 'POST', cookie, expected: 405 })).allow, 'GET');

  const upstream = await admin.request('/upstreams', 'POST', { name: 'Synthetic cost upstream', provider_kind: 'openai-compatible', endpoint, api_key: upstreamKey }, 201);
  await admin.request('/models', 'POST', { id: 'public-cost-model', upstream_id: upstream.id, upstream_model: 'actual-cost-model' }, 201);
  const pricePath = `/upstreams/${upstream.id}/prices`;
  async function setPrice(revision, price) {
    return admin.request(pricePath, 'POST', { operation_id: randomUUID(), expected_revision: revision, upstream_model: 'actual-cost-model', price });
  }
  await setPrice(0, rates('USD', 1));
  await request(key.key);
  await setPrice(1, rates('EUR', 2));
  await request(key.key);
  partialNext = true;
  await request(key.key);
  await setPrice(2, null);
  await request(key.key);
  const summary = (await selfCall(route, { cookie })).body;
  assert.deepEqual(Object.keys(summary).sort(), ['attempts', 'costs', 'from', 'to']);
  assert.deepEqual(summary.attempts, { total: '4', pending: '0', terminal: '4' });
  assert.deepEqual(summary.costs, [
    { price_currency: 'EUR', attempts: '2', known_estimated_cost_micro: '374', unknown_cost_attempts: '1' },
    { price_currency: 'USD', attempts: '1', known_estimated_cost_micro: '187', unknown_cost_attempts: '0' },
    { price_currency: null, attempts: '1', known_estimated_cost_micro: '0', unknown_cost_attempts: '1' },
  ]);
  assert.equal(Date.parse(summary.to) - Date.parse(summary.from), 24 * 60 * 60 * 1000);
  const firstEnabledProcess = processHandle;
  await stopServer(firstEnabledProcess);
  const firstEnabledOutput = firstEnabledProcess.output();
  processHandle = await startServer(executable, scratch, path.resolve('web/dist'), [selfFlag, feature]);
  assert.deepEqual((await selfCall(route, { cookie })).body.costs, summary.costs);
  const secondEnabledOutput = processHandle.output();
  for (const [phase, logs] of [['first enabled process', firstEnabledOutput], ['restarted process', secondEnabledOutput]]) {
    for (const secret of [adminPassword, admin.cookie(), adminSession.csrf_token, enrollment.enrollment_secret, selfPassword, cookie, key.key, upstreamKey, prompt, answer]) {
      assert.ok(!logs.includes(secret), `${phase} leaked a synthetic secret`);
    }
  }
  console.log('employee self estimated cost isolated process acceptance passed');
  if (holdBrowser) {
    console.log(`browser origin=${processHandle.origin} employee_id=${employee.id}`);
    await new Promise(resolve => setTimeout(resolve, 180000));
  }
} finally {
  await stopServer(processHandle);
  if (mock?.listening) { mock.closeAllConnections(); await new Promise(resolve => mock.close(resolve)); }
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('self-estimated-cost-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
