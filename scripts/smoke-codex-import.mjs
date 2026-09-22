// Independently authored from docs/codex-membership-preview-contract.md.
// Synthetic credentials only. Never issues a membership model request while enabled.
// Usage: node scripts/smoke-codex-import.mjs <absolute executable> <absolute web dir>
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, webDirectory] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable required');
assert.ok(webDirectory && path.isAbsolute(webDirectory), 'Absolute web directory required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-codex-import-'));
const password = randomBytes(24).toString('hex');
const secrets = [password];
let child, origin, cookie = '', csrf = '', logs = '';

function authFile() {
  const account = `synthetic-account-${randomUUID()}`;
  const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
  const access = `${encode({ alg: 'RS256', typ: 'JWT' })}.${encode({ exp: Math.floor(Date.now() / 1000) + 3600, sub: account })}.synthetic-signature`;
  const refresh = `synthetic-refresh-${randomUUID()}`;
  secrets.push(account, access, refresh);
  return JSON.stringify({ auth_mode: 'chatgpt', tokens: { access_token: access, refresh_token: refresh, account_id: account } });
}
function noSecrets(value, label) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(String(value));
  assert.ok(!secrets.some(secret => bytes.includes(Buffer.from(secret))), `${label} contains synthetic secret material`);
}
function launch(args) {
  const process = spawn(executable, ['--data-dir', directory, ...args], { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  for (const stream of [process.stdout, process.stderr]) stream.on('data', chunk => {
    logs += chunk.toString();
    if (logs.length > 1024 * 1024) process.kill();
  });
  return process;
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = once(child, 'exit');
    child.stdin.end();
    const timer = setTimeout(() => child.kill(), 5000);
    try { await ended; } finally { clearTimeout(timer); }
  }
}
async function start(enabled) {
  const probe = http.createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  origin = `http://127.0.0.1:${port}`;
  cookie = ''; csrf = '';
  child = launch(['--listen', `127.0.0.1:${port}`, '--web-dir', webDirectory, '--shutdown-on-stdin-eof', ...(enabled ? ['--experimental-codex-membership'] : [])]);
  let launchError;
  child.on('error', error => { launchError = error; });
  for (let attempt = 0; attempt < 100; attempt++) {
    if (launchError || child.exitCode !== null) throw new Error('Service failed to start');
    try { if ((await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok) return; } catch { /* Wait for readiness. */ }
    await delay(100);
  }
  throw new Error('Service readiness timeout');
}
async function admin(route, method = 'GET', body, expected = 200, withCSRF = true) {
  const response = await fetch(origin + '/admin/api/v1' + route, {
    method, headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', ...(withCSRF ? { 'X-CSRF-Token': csrf } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000),
  });
  const raw = await response.text();
  noSecrets(raw, 'Admin response');
  assert.equal(response.status, expected, `${method} ${route} status`);
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  return raw ? JSON.parse(raw) : {};
}
async function login(enabled) {
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  assert.ok(csrf, 'CSRF token missing');
  assert.equal((await admin('/system/status')).features.codex_membership_import, enabled);
}
async function scanStorage() {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isFile()) noSecrets(await readFile(path.join(directory, entry.name)), 'Persisted data');
  }
  noSecrets(logs, 'Process logs');
}

try {
  child = launch(['--init']);
  const initialized = once(child, 'exit');
  child.stdin.end(password + '\n');
  assert.equal((await initialized)[0], 0, 'Initialization failed');
  await start(true);
  await login(true);
  const auth = authFile(), replacement = authFile();
  const request = { name: 'Synthetic Codex', auth_json: auth, operation_id: randomUUID() };
  await admin('/upstreams/codex-import', 'POST', request, 403, false);
  assert.equal((await admin('/upstreams')).items.length, 0);
  const imported = await admin('/upstreams/codex-import', 'POST', request, 201);
  assert.equal(imported.provider_kind, 'codex-membership');
  assert.equal(imported.credential_state, 'imported_unverified');
  assert.equal(imported.verified_at, null);
  const retry = await admin('/upstreams/codex-import', 'POST', request);
  assert.equal(retry.id, imported.id);
  assert.equal(retry.revision, imported.revision);
  assert.equal((await admin('/upstreams')).items.length, 1);
  await admin(`/upstreams/${imported.id}/codex-auth`, 'PUT', { expected_revision: imported.revision, auth_json: '{}' }, 400);
  assert.equal((await admin('/upstreams')).items[0].revision, imported.revision);
  const updated = await admin(`/upstreams/${imported.id}/codex-auth`, 'PUT', { expected_revision: imported.revision, auth_json: replacement });
  assert.equal(updated.revision, imported.revision + 1);
  assert.equal(updated.credential_state, 'imported_unverified');
  const conflict = await admin(`/upstreams/${imported.id}/codex-auth`, 'PUT', { expected_revision: imported.revision, auth_json: auth }, 409);
  assert.equal(conflict.error.code, 'revision_conflict');
  await admin('/models', 'POST', { id: 'synthetic-membership-model', upstream_id: imported.id, upstream_model: 'synthetic-only' }, 201);
  const employee = await admin('/employees', 'POST', { name: 'Synthetic employee' }, 201);
  const key = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'Synthetic', operation_id: randomUUID() }, 201);
  assert.ok(key.key);
  secrets.push(key.key);
  await scanStorage();
  await stop();
  await scanStorage();
  await start(false);
  await login(false);
  const restored = (await admin('/upstreams')).items;
  assert.equal(restored.length, 1);
  assert.equal(restored[0].revision, updated.revision);
  assert.equal(restored[0].credential_state, 'imported_unverified');
  assert.equal((await admin('/upstreams/codex-import', 'POST', request, 403)).error.code, 'feature_disabled');
  assert.equal((await admin(`/upstreams/${imported.id}/codex-auth`, 'PUT', { expected_revision: updated.revision, auth_json: auth }, 403)).error.code, 'feature_disabled');
  // Disabled flag is verified above; this request must not execute any upstream.
  const denied = await fetch(origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: `Bearer ${key.key}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'synthetic-membership-model', messages: [{ role: 'user', content: 'Synthetic test' }] }), signal: AbortSignal.timeout(5000) });
  const raw = await denied.text();
  noSecrets(raw, 'Employee response');
  assert.equal(denied.status, 403);
  assert.equal(JSON.parse(raw).error.code, 'feature_disabled');
  await stop();
  await start(true);
  await login(true);
  const persistedRetry = await admin('/upstreams/codex-import', 'POST', request);
  assert.equal(persistedRetry.id, imported.id);
  assert.equal(persistedRetry.revision, updated.revision, 'Retry must not restore replaced credentials');
  await stop();
  await scanStorage();
  console.log('PASS: synthetic Codex import, CSRF, idempotency, replacement/conflict, encrypted persistence, restart and disabled routing. No live membership request performed.');
} finally {
  await stop();
  const resolved = path.resolve(directory);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-codex-import-')) throw new Error('Unexpected cleanup path');
  await rm(resolved, { recursive: true, force: true });
}
