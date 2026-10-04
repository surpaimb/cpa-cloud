// Independently authored raw-wire acceptance for
// docs/employee-self-plan-catalog-route-boundary-contract.md.
// Usage: node scripts/smoke-employee-self-plan-catalog-route-boundary.mjs <absolute service executable>
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { initialize, root, startServer, stopServer, adminClient, forbiddenPort } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const sourceRoot = path.resolve(import.meta.dirname, '..');
await mkdir(root, { recursive: true });
const scratch = await mkdtemp(path.join(root, 'plan-catalog-route-'));
assert.ok(scratch.startsWith(root + path.sep), 'scratch must be within the explicit acceptance root');
const dataDir = path.join(scratch, 'data');
const webDir = path.join(scratch, 'web');
const marker = `catalog-route-web-${randomUUID()}`;
const P = '/self/api/v1/billing/plans';
const Q = '/self/api/v1/billing/plan-purchase-quotes';
const S = '/self/api/v1/billing/subscriptions';
const rows = [];
const processes = [];
let server;
let cookie = '';
let csrf = '';

function targetLabel(target) {
  if (target.length <= 160) return target;
  return { bytes: Buffer.byteLength(target), first: target.slice(0, 55), last: target.slice(-35),
    sha256: createHash('sha256').update(target).digest('hex') };
}

async function fileSHA256(filename) {
  return createHash('sha256').update(await readFile(filename)).digest('hex');
}

function firstHeader(head, name) {
  return head.match(new RegExp(`^${name}:\\s*(.*)$`, 'im'))?.[1]?.trim() ?? '';
}

async function wire(method, target, { auth = 'anonymous', origin, duplicateOrigin = false } = {}) {
  const destination = new URL(server.origin);
  const port = Number(destination.port);
  assert.notEqual(port, forbiddenPort);
  const body = method === 'POST' ? '{}' : '';
  const usedOrigin = origin ?? server.origin;
  const request = `${method} ${target} HTTP/1.1\r\nHost: ${destination.host}\r\nConnection: close\r\n` +
    `Origin: ${usedOrigin}\r\n${duplicateOrigin ? `Origin: ${usedOrigin}\r\n` : ''}` +
    `${auth === 'valid' ? `Cookie: ${cookie}\r\n` : ''}` +
    `${auth === 'admin' ? `Cookie: ${adminCookie}\r\n` : ''}` +
    `${auth === 'bearer' ? 'Authorization: Bearer synthetic-not-a-self-session\r\n' : ''}` +
    `${auth === 'valid' && method === 'POST' ? `X-Self-Request: 1\r\nX-CSRF-Token: ${csrf}\r\n` : ''}` +
    `${body ? 'Content-Type: application/json\r\n' : ''}` +
    `Content-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`;
  const response = await new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: destination.hostname, port });
    const parts = [];
    const timer = setTimeout(() => socket.destroy(new Error('raw TCP timeout')), 10000);
    socket.on('connect', () => socket.write(request));
    socket.on('data', chunk => parts.push(chunk));
    socket.on('error', error => { clearTimeout(timer); reject(error); });
    socket.on('end', () => { clearTimeout(timer); resolve(Buffer.concat(parts).toString('utf8')); });
  });
  const cut = response.indexOf('\r\n\r\n');
  assert.ok(cut >= 0, 'first HTTP response lacks headers');
  const head = response.slice(0, cut);
  const payload = response.slice(cut + 4);
  const status = Number(head.match(/^HTTP\/1\.1 (\d{3})/)?.[1]);
  assert.ok(status >= 100 && status <= 599, 'invalid first wire status');
  let bodySample = status >= 300 ? payload.slice(0, 200) : '';
  for (const secret of [cookie, csrf, adminCookie]) {
    if (secret) bodySample = bodySample.replaceAll(secret, '[REDACTED]');
  }
  return {
    status, location: firstHeader(head, 'Location'), allow: firstHeader(head, 'Allow'),
    cache: firstHeader(head, 'Cache-Control'), contentType: firstHeader(head, 'Content-Type'),
    code: payload.match(/"code"\s*:\s*"([^"]+)"/)?.[1] ?? '',
    bodyKind: payload.length === 0 ? 'empty' : payload.startsWith('{') ? 'json' : 'other',
    bodyBytes: Buffer.byteLength(payload), bodySample, marker: payload.includes(marker),
  };
}

let adminCookie = '';
async function establishSession() {
  const admin = adminClient(server.origin);
  await admin.login();
  adminCookie = admin.cookie();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic catalog route employee' }, 201);
  const issued = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrollment = await fetch(server.origin + '/self/api/v1/enroll', {
    method: 'POST', redirect: 'manual', signal: AbortSignal.timeout(10000),
    headers: { Origin: server.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
    body: JSON.stringify({ employee_id: employee.id, enrollment_secret: issued.enrollment_secret,
      password: `synthetic-catalog-password-${randomUUID()}` }),
  });
  assert.equal(enrollment.status, 200, 'synthetic self enrollment failed');
  cookie = enrollment.headers.get('set-cookie')?.split(';')[0] ?? '';
  csrf = (await enrollment.json()).csrf_token;
  assert.ok(cookie && csrf, 'self session or CSRF was not issued');
}

function expected(mode, auth, kind) {
  if (kind === 'neighbor') return 404;
  if (mode === 'off') return 404;
  if (auth !== 'valid') return 401;
  return kind === 'canonical' ? 200 : 400;
}

async function check(mode, name, method, target, auth, want, options = {}) {
  const result = await wire(method, target, { auth, ...options });
  rows.push({ mode, name, method, auth, target: targetLabel(target), ...result });
  assert.equal(result.status, want, `${mode}/${name}/${method}/${auth}: ${JSON.stringify(result)}`);
  assert.equal(result.cache, 'no-store');
  assert.equal(result.location, '', `${mode}/${name} escaped through Location`);
  assert.equal(result.allow, '', `${mode}/${name} gained Allow`);
  assert.equal(result.marker, false, `${mode}/${name} exposed WebDir`);
  if (method === 'HEAD') assert.equal(result.bodyBytes, 0, `${mode}/${name} HEAD emitted a body`);
  if (mode !== 'off' && auth === 'valid' && want === 400 && method === 'GET') {
    assert.equal(result.code, 'invalid_request');
  }
  return result;
}

async function portClosed(origin) {
  const destination = new URL(origin);
  return new Promise(resolve => {
    const socket = net.createConnection({ host: destination.hostname, port: Number(destination.port) });
    const timer = setTimeout(() => { socket.destroy(); resolve(false); }, 500);
    socket.on('connect', () => { clearTimeout(timer); socket.destroy(); resolve(false); });
    socket.on('error', () => { clearTimeout(timer); resolve(true); });
  });
}

async function stopCurrent() {
  if (!server) return;
  const current = server;
  server = undefined;
  await stopServer(current);
  const item = processes.find(entry => entry.pid === current.child.pid);
  item.closed = await portClosed(current.origin);
  assert.ok(item.closed, `listener remained open: ${current.origin}`);
}

try {
  await mkdir(webDir);
  await writeFile(path.join(webDir, 'index.html'), marker);
  await initialize(executable, dataDir);
  const baseFlags = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
    '--employee-self-subscription-status-enabled'];
  const shapes = [
    ['canonical', P + '?currency=USD', 'canonical'],
    ['double-slash', '/self/api/v1/billing//plans?currency=USD', 'malformed'],
    ['dot-segment', '/self/api/v1/billing/./plans?currency=USD', 'malformed'],
    ['descendant', P + '/extra?currency=USD', 'malformed'],
    ['case-alias', '/self/api/v1/billing/Plans?currency=USD', 'malformed'],
    ['encoded-letter', '/self/api/v1/billing/%70lans?currency=USD', 'malformed'],
    ['encoded-slash-child', P + '%2Fextra?currency=USD', 'malformed'],
    ['complete-P-cross-to-plans-other', P + '/../plans-other?currency=USD', 'malformed'],
    ['complete-P-cross-to-plansx', P + '/../plansx?currency=USD', 'malformed'],
    ['plansx', P + 'x', 'neighbor'],
    ['plans-other', P + '-other', 'neighbor'],
    ['plans-percent-dash-other', P + '%2Dother', 'neighbor'],
    ['encoded-question-neighbor', P + '%3Ffoo', 'neighbor'],
    ['encoded-hash-neighbor', P + '%23foo', 'neighbor'],
  ];
  for (const mode of ['off', 'catalog-only']) {
    server = await startServer(executable, dataDir, webDir,
      mode === 'off' ? baseFlags : [...baseFlags, '--employee-self-plan-catalog-enabled']);
    processes.push({ mode, pid: server.child.pid, port: Number(new URL(server.origin).port) });
    if (!cookie) await establishSession();
    for (const [name, target, kind] of shapes) {
      for (const method of ['GET', 'HEAD']) {
        for (const auth of ['anonymous', 'valid']) {
          await check(mode, name, method, target, auth, expected(mode, auth, kind));
        }
      }
    }
    const oneShotOverlap = P + '/../subscriptions/id/one-shot-renewal';
    for (const method of ['GET', 'HEAD']) {
      for (const auth of ['anonymous', 'valid']) {
        const got = await wire(method, oneShotOverlap, { auth });
        rows.push({ mode, name: 'outer-one-shot-overlap', method, auth,
          target: oneShotOverlap, ...got });
        assert.equal(got.status, 404, `${mode}/${method}/${auth}: one-shot guard lost ownership`);
        assert.equal(got.location, '');
        assert.equal(got.allow, '');
        assert.equal(got.cache, 'no-store');
        assert.equal(got.contentType, 'text/plain; charset=utf-8');
        assert.equal(got.marker, false);
        if (method === 'HEAD') assert.equal(got.bodyBytes, 0);
      }
    }
    const parent = '/self/api/v1/billing/';
    const hidden = parent + '/'.repeat(8192 - parent.length - 'plans'.length) + 'plans';
    assert.equal(hidden.length, 8192);
    const deep = depth => P + '%' + '25'.repeat(depth - 1) + '2Fextra';
    for (const [name, target, kind] of [
      // Long relative to the route classifier, but below this server's 32 KiB header cap.
      ['8192-path-query', hidden + '?' + 'x'.repeat(4096), 'malformed'],
      ['raw-depth-17', deep(17), 'malformed'],
      ['long-RawPath-proved', P + '/' + '%41'.repeat(3000), 'malformed'],
      ['raw-depth-18-unowned', deep(18), 'neighbor'],
      ['long-RawPath-neighbor', P + '-other/' + '%41'.repeat(3000), 'neighbor'],
    ]) {
      await check(mode, name, 'GET', target, 'valid', expected(mode, 'valid', kind));
    }
    const overBudget = await wire('GET', hidden + 'x', { auth: 'valid' });
    rows.push({ mode, name: '8193-path-unowned', method: 'GET', auth: 'valid',
      target: targetLabel(hidden + 'x'), ...overBudget });
    assert.equal(overBudget.status, 307, 'an unowned 8193-byte path should retain ServeMux cleaning');
    assert.equal(overBudget.location, P + 'x');
    assert.equal(overBudget.cache, 'no-store');
    for (const auth of ['admin', 'bearer']) {
      const got = await wire('GET', '/self/api/v1/billing//plans?currency=USD', { auth });
      rows.push({ mode, name: 'non-self-role', method: 'GET', auth, ...got });
      assert.equal(got.status, mode === 'off' ? 404 : 401);
      assert.equal(got.location, '');
    }
    for (const options of [{ origin: 'http://wrong.invalid' }, { duplicateOrigin: true }]) {
      const got = await wire('GET', '/self/api/v1/billing//plans?currency=USD', { auth: 'valid', ...options });
      rows.push({ mode, name: 'Origin-priority', method: 'GET', auth: 'valid', ...got });
      assert.equal(got.status, mode === 'off' ? 404 : 403);
      assert.equal(got.location, '');
      assert.equal(got.cache, 'no-store');
    }
    await check(mode, 'POST-P-original-owner', 'POST', P, 'valid', 404);
    await check(mode, 'purchase-Q-disabled', 'POST', Q, 'valid', 404);
    await check(mode, 'purchase-S-disabled', 'POST', S, 'valid', 404);
    await check(mode, 'purchase-Q-double-slash-disabled', 'POST', '/self/api/v1/billing//plan-purchase-quotes', 'valid', 404);
    const subscription = await wire('GET', S, { auth: 'valid' });
    rows.push({ mode, name: 'subscription-GET-owner', method: 'GET', auth: 'valid', ...subscription });
    assert.equal(subscription.status, 200);
    assert.equal(subscription.location, '');
    const subscriptionDot = await wire('GET', '/self/api/v1/billing/./subscriptions', { auth: 'valid' });
    rows.push({ mode, name: 'subscription-dot-owner', method: 'GET', auth: 'valid', ...subscriptionDot });
    assert.equal(subscriptionDot.status, 307);
    assert.equal(subscriptionDot.location, S);
    assert.equal(subscriptionDot.cache, 'no-store');
    await stopCurrent();
  }

  server = await startServer(executable, dataDir, webDir,
    ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
      '--employee-self-subscription-status-enabled', '--employee-self-plan-catalog-enabled',
      '--employee-self-plan-purchase-enabled']);
  processes.push({ mode: 'catalog-and-purchase', pid: server.child.pid, port: Number(new URL(server.origin).port) });
  for (const [name, target] of [['purchase-Q', Q], ['purchase-S', S],
    ['purchase-Q-double-slash', '/self/api/v1/billing//plan-purchase-quotes']]) {
    await check('catalog-and-purchase', name, 'POST', target, 'valid', 400);
  }
  const purchaseEnabledOverlap = await wire('GET', P + '/../subscriptions/id/one-shot-renewal', { auth: 'valid' });
  rows.push({ mode: 'catalog-and-purchase', name: 'outer-one-shot-overlap', method: 'GET',
    auth: 'valid', ...purchaseEnabledOverlap });
  assert.equal(purchaseEnabledOverlap.status, 404);
  assert.equal(purchaseEnabledOverlap.location, '');
  assert.equal(purchaseEnabledOverlap.allow, '');
  assert.equal(purchaseEnabledOverlap.cache, 'no-store');
  await stopCurrent();
  const red = rows.filter(row => row.name === 'double-slash' || row.name === 'dot-segment');
  assert.equal(red.length, 16);
  assert.ok(red.every(row => row.status !== 307 && row.location === ''));
  const sourceIdentity = {
    head: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim(),
    executableSHA256: await fileSHA256(executable),
    appGoSHA256: await fileSHA256(path.join(sourceRoot, 'internal/service/app.go')),
    catalogGuardGoSHA256: await fileSHA256(path.join(sourceRoot, 'internal/service/self_plan_catalog_route.go')),
  };
  const evidencePath = path.join(scratch, 'evidence.json');
  await writeFile(evidencePath, JSON.stringify({ source: executable, sourceIdentity, processes, rows }, null, 2) + '\n');
  console.log(JSON.stringify({ source: executable, sourceIdentity, evidencePath, processes, probeCount: rows.length,
    formerRedCases: red.length, firstHop: red.slice(0, 4),
    neighbors: rows.filter(row => row.name.startsWith('plans') && row.auth === 'valid' && row.method === 'GET'),
    purchase: rows.filter(row => row.name.includes('purchase-Q-double-slash')) }, null, 2));
} finally {
  await stopCurrent();
}
