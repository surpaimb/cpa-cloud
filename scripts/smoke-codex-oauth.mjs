// Independent CLI/admin acceptance. No OAuth callback exchange or model request.
// Only synthetic data in an isolated temporary directory; no provider HTTP calls.
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
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-oauth-'));
const password = randomBytes(24).toString('hex');
const secrets = [password];
let child, origin, cookie = '', csrf = '', logs = '';
const clientA = 'synthetic-cpa-client-a', clientB = 'synthetic-cpa-client-b';

function launch(args) {
  const proc = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => {
    logs += chunk.toString();
    if (logs.length > 1 << 20) proc.kill();
  });
  return proc;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = once(child, 'exit'); child.stdin.end();
    const timer = setTimeout(() => child.kill(), 5000);
    try { await ended; } finally { clearTimeout(timer); }
  }
}
async function start(enabled, client) {
  const address = new URL(origin).host;
  const config = client ? ['--codex-oauth-client-id', client, '--codex-oauth-redirect-uri', origin + '/admin/api/v1/codex/oauth/callback'] : [];
  child = launch(['--listen', address, '--shutdown-on-stdin-eof', ...(enabled ? ['--experimental-codex-membership'] : []), ...config]);
  let launchError;
  child.on('error', error => { launchError = error; });
  for (let i = 0; i < 100; i++) {
    if (launchError || child.exitCode !== null) throw new Error('Service launch failed');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* Wait for this test instance. */ }
    await delay(100);
  }
  throw new Error('Service readiness timeout');
}
async function admin(route, method = 'GET', body, status = 200, useCSRF = true) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', ...(useCSRF ? { 'X-CSRF-Token': csrf } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000),
  });
  assert.equal(response.status, status, `${method} ${route} status`);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  assert.equal(response.headers.get('referrer-policy'), 'no-referrer');
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  const raw = await response.text();
  return raw ? JSON.parse(raw) : {};
}
async function login() { csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token; }
function noSecrets(value, label) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value);
  assert.ok(!secrets.some(secret => bytes.includes(Buffer.from(secret))), `${label} leaked synthetic secret material`);
}

try {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  origin = `http://127.0.0.1:${probe.address().port}`; await new Promise(resolve => probe.close(resolve));
  child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0, 'Initialization failed');
  const input = { name: 'Synthetic OAuth', operation_id: randomUUID() };

  await start(false); await login();
  assert.equal((await admin('/system/status')).features.codex_membership_oauth, false);
  assert.equal((await admin('/upstreams/codex-oauth-sessions', 'POST', input, 403)).error.code, 'feature_disabled');
  await stop(); await start(true); await login();
  assert.equal((await admin('/upstreams/codex-oauth-sessions', 'POST', input, 409)).error.code, 'codex_oauth_not_configured');

  await stop(); await start(true, clientA); await login();
  assert.equal((await admin('/system/status')).features.codex_membership_oauth, true);
  await admin('/upstreams/codex-oauth-sessions', 'POST', input, 403, false);
  const created = await admin('/upstreams/codex-oauth-sessions', 'POST', input, 201);
  const authorize = new URL(created.authorization_url);
  assert.equal(authorize.origin + authorize.pathname, 'https://auth.openai.com/oauth/authorize');
  assert.equal(authorize.searchParams.get('client_id'), clientA);
  assert.equal(authorize.searchParams.get('redirect_uri'), origin + '/admin/api/v1/codex/oauth/callback');
  assert.equal(authorize.searchParams.get('code_challenge_method'), 'S256');
  assert.match(authorize.searchParams.get('code_challenge'), /^[A-Za-z0-9_-]{43}$/);
  const state = authorize.searchParams.get('state'); assert.ok(state && state.length >= 40); secrets.push(state);
  assert.deepEqual(await admin('/upstreams/codex-oauth-sessions', 'POST', input), created);
  for (const body of [{}, { expected_revision: null }, { expected_revision: 0 }, { expected_revision: -1 }]) {
    assert.equal((await admin('/upstreams/synthetic-only/codex-refresh', 'POST', body, 400)).error.code, 'invalid_request');
  }

  await stop(); await start(true, clientA);
  assert.deepEqual(await admin('/upstreams/codex-oauth-sessions', 'POST', input), created, 'Same-config restart changed session');
  await stop(); await start(true, clientB);
  const conflict = await admin('/upstreams/codex-oauth-sessions', 'POST', input, 409);
  assert.equal(conflict.error.code, 'codex_oauth_configuration_changed');
  noSecrets(JSON.stringify(conflict), 'Configuration conflict');
  await stop(); await start(true, clientA);
  assert.deepEqual(await admin('/upstreams/codex-oauth-sessions', 'POST', input), created, 'Mismatch consumed or replaced session');
  assert.equal((await admin('/upstreams')).items.length, 0, 'Session creation unexpectedly created an account');
  await stop(); await start(false, clientA);
  assert.equal((await admin('/system/status')).features.codex_membership_oauth, false);
  assert.equal((await admin('/upstreams/codex-oauth-sessions', 'POST', input, 403)).error.code, 'feature_disabled');
  await stop();
  noSecrets(logs, 'Process logs');
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isFile()) noSecrets(await readFile(path.join(directory, entry.name)), 'Persistent data');
  }
  console.log('PASS: OAuth CLI flags, disabled/unconfigured states, CSRF, PKCE/session idempotency, invalid revision, configuration-change rejection, restart and encrypted state. No provider request performed.');
} finally {
  await stop();
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-oauth-')) throw new Error('Unexpected cleanup path');
  await rm(resolved, { recursive: true, force: true });
}
