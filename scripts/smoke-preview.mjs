// Independent process-level acceptance test; uses generated test credentials only.
// Usage: node scripts/smoke-preview.mjs <absolute executable path> [absolute web directory]
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'Provide an absolute executable path');
const webDirectory = process.argv[3] ?? path.resolve('web/dist');
assert.ok(path.isAbsolute(webDirectory), 'Provide an absolute web directory');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-smoke-'));
const password = randomBytes(24).toString('hex');
const upstreamSecret = randomBytes(24).toString('hex');
let calls = 0, upstreamValid = true, child;
let cookie = '', csrf = '';
const mock = http.createServer(async (req, res) => {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  let body;
  try { body = JSON.parse(Buffer.concat(chunks)); } catch { res.writeHead(400).end(); return; }
  calls++;
  upstreamValid &&= req.url === '/v1/chat/completions' &&
    req.headers.authorization === `Bearer ${upstreamSecret}` && body.model === 'mock-upstream';
  if (body.stream) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    res.write('data: {"id":"mock","choices":[{"delta":{"content":"ok"}}]}\n\n');
    await delay(30);
    res.end('data: [DONE]\n\n');
  } else {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ id: 'mock', object: 'chat.completion', choices: [], usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 } }));
  }
});
mock.listen(0, '127.0.0.1');
await once(mock, 'listening');
const probe = http.createServer();
probe.listen(0, '127.0.0.1');
await once(probe, 'listening');
const port = probe.address().port;
await new Promise(resolve => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
const baseArgs = ['--data-dir', directory];
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = once(child, 'exit'); child.kill(); await ended;
  }
}
async function start() {
  child = spawn(executable, [...baseArgs, '--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--web-dir', webDirectory], { stdio: 'ignore', windowsHide: true });
  let launchError;
  child.on('error', error => { launchError = error; });
  for (let attempt = 0; attempt < 100; attempt++) {
    if (launchError || child.exitCode !== null) throw new Error('Service failed to start');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* Wait for startup. */ }
    await delay(100);
  }
  throw new Error('Service readiness timeout');
}
async function admin(route, method = 'GET', body) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000),
  });
  assert.ok(response.ok, `Admin ${method} ${route}: HTTP ${response.status}`);
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  return response.status === 204 ? {} : response.json();
}
async function model(key, stream = false) {
  return fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: 'smoke-model', messages: [{ role: 'user', content: 'synthetic smoke' }], stream }), signal: AbortSignal.timeout(5000) });
}
try {
  const init = spawn(executable, [...baseArgs, '--init'], { stdio: ['pipe', 'ignore', 'ignore'], windowsHide: true });
  const initialized = once(init, 'exit');
  init.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0, 'Initialization failed');
  await start();
  assert.equal((await fetch(origin + '/')).status, 200, 'Web entry missing');
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  assert.ok(csrf);
  const upstream = await admin('/upstreams', 'POST', { name: 'smoke', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}`, api_key: upstreamSecret });
  await admin('/models', 'POST', { id: 'smoke-model', upstream_id: upstream.id, upstream_model: 'mock-upstream' });
  const employee = await admin('/employees', 'POST', { name: 'Smoke employee' });
  const issued = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'smoke', operation_id: randomUUID() });
  assert.ok(issued.key); assert.equal(issued.expires_at, null);
  const listing = await admin(`/employees/${employee.id}/keys`);
  assert.ok(!JSON.stringify(listing).includes(issued.key), 'Key leaked in listing');
  const first = await model(issued.key); assert.equal(first.status, 200); await first.text();
  const stream = await model(issued.key, true); assert.equal(stream.status, 200); assert.ok((await stream.text()).includes('[DONE]'));
  assert.ok(upstreamValid, 'Upstream credential or route mismatch');
  await stop(); await start();
  const restored = await model(issued.key); assert.equal(restored.status, 200); await restored.text();
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  await admin(`/keys/${issued.id}/revoke`, 'POST', {});
  const previousCalls = calls;
  const denied = await model(issued.key); assert.equal(denied.status, 401); await denied.text();
  assert.equal(calls, previousCalls, 'Revoked request reached upstream');
  await stop(); await start();
  const stillDenied = await model(issued.key); assert.equal(stillDenied.status, 401); await stillDenied.text();
  console.log('PASS: CLI initialization, web entry, admin APIs, permanent key, nonstream/SSE, credential isolation, restart and revocation persistence');
} finally {
  await stop();
  mock.closeAllConnections(); await new Promise(resolve => mock.close(resolve));
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-smoke-')) throw new Error('Unexpected cleanup path');
  await rm(resolved, { recursive: true, force: true });
}
