// Independently authored for docs/employee-self-plan-purchase-route-boundary-contract.md.
// Synthetic credentials stay in memory; only sanitized first-response facts are printed.
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { createHash, randomUUID } from 'node:crypto';
import net from 'node:net';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
await mkdir(root, { recursive: true });
const scratch = await mkdtemp(path.join(root, 'plan-purchase-route-'));
assert.ok(scratch.startsWith(root + path.sep), 'scratch must be under acceptance root');
const data = path.join(scratch, 'data');
const web = path.join(scratch, 'web');
const marker = `synthetic-plan-purchase-web-${randomUUID()}`;
const q = '/self/api/v1/billing/plan-purchase-quotes';
const s = '/self/api/v1/billing/subscriptions';
const baseFlags = ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled',
  '--employee-self-plan-catalog-enabled', '--employee-self-subscription-status-enabled'];
let server;
const evidence = [];
const ports = [];
const neighbors = [q + '-other', s + 'x', s + '//id',
  s + '/id/cancel/../..', '/self/api/v1/billing/redemptions/../plan-purchase-quotes',
  q + '/../subscriptions/id/cancel'];

const targetEvidence = target => target.length <= 256 ? target : {
  length: target.length, first: target.slice(0, 32), last: target.slice(-32),
  sha256: createHash('sha256').update(target).digest('hex'),
};

async function wire(method, target, { cookie = '', csrf = '', origin = server.origin, self = true, selfValues } = {}) {
  const destination = new URL(server.origin);
  const port = Number(destination.port);
  assert.notEqual(port, 8787, 'protected service port');
  const body = method === 'POST' ? '{}' : '';
  const request = `${method} ${target} HTTP/1.1\r\nHost: ${destination.host}\r\nConnection: close\r\n` +
    `Origin: ${origin}\r\n${(selfValues ?? (self ? ['1'] : [])).map(value => `X-Self-Request: ${value}\r\n`).join('')}` +
    `${cookie ? `Cookie: ${cookie}\r\n` : ''}${csrf ? `X-CSRF-Token: ${csrf}\r\n` : ''}` +
    `Content-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`;
  const response = await new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: destination.hostname, port });
    const chunks = [];
    const timer = setTimeout(() => socket.destroy(new Error('raw TCP timeout')), 10000);
    socket.on('connect', () => socket.write(request));
    socket.on('data', chunk => chunks.push(chunk));
    socket.on('error', error => { clearTimeout(timer); reject(error); });
    socket.on('end', () => { clearTimeout(timer); resolve(Buffer.concat(chunks).toString('utf8')); });
  });
  const [head, payload = ''] = response.split('\r\n\r\n', 2);
  assert.match(head, /^HTTP\/1\.1 \d{3}/, 'missing first response status');
  const header = name => head.match(new RegExp(`^${name}:\\s*(.*)$`, 'im'))?.[1]?.trim() ?? '';
  return {
    status: Number(head.match(/^HTTP\/1\.1 (\d{3})/)?.[1]),
    location: header('Location'), allow: header('Allow'), cache: header('Cache-Control'),
    contentType: header('Content-Type'), code: payload.match(/"code"\s*:\s*"([^"]+)"/)?.[1] ?? '',
    marker: payload.includes(marker),
  };
}

async function check(mode, name, target, auth, status, code = '') {
  const result = await wire('POST', target, auth);
  assert.equal(result.status, status, `${mode}/${name} status: ${JSON.stringify(result)}`);
  assert.equal(result.code, code, `${mode}/${name} code`);
  assert.equal(result.location, '', `${mode}/${name} Location`);
  assert.equal(result.allow, '', `${mode}/${name} Allow`);
  assert.equal(result.cache, 'no-store', `${mode}/${name} cache`);
  assert.equal(result.marker, false, `${mode}/${name} web fallback`);
  evidence.push({ mode, name, method: 'POST', target: targetEvidence(target), ...result });
}

try {
  await mkdir(web);
  await writeFile(path.join(web, 'index.html'), marker);
  await initialize(executable, data);
  server = await startServer(executable, data, web, baseFlags);
  ports.push(Number(new URL(server.origin).port));
  const admin = adminClient(server.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic plan route employee' }, 201);
  const issued = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrollment = await fetch(server.origin + '/self/api/v1/enroll', {
    method: 'POST', signal: AbortSignal.timeout(10000),
    headers: { Origin: server.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
    body: JSON.stringify({ employee_id: employee.id, enrollment_secret: issued.enrollment_secret,
      password: `synthetic-plan-route-password-${randomUUID()}` }),
  });
  assert.equal(enrollment.status, 200, 'synthetic self enrollment failed');
  const cookie = enrollment.headers.get('set-cookie')?.split(';')[0];
  const csrf = (await enrollment.json()).csrf_token;
  assert.ok(cookie && csrf, 'self enrollment did not return session and CSRF');
  const valid = { cookie, csrf };
  const readTargets = [s, s + '/id', '/self/api/v1/billing/plans'];
  const readBefore = [];
  for (const target of readTargets) readBefore.push(await wire('GET', target, valid));
  for (const [name, target] of [['Q canonical', q], ['S canonical', s],
    ['Q double slash', '/self/api/v1/billing//plan-purchase-quotes'],
    ['S dot', '/self/api/v1/billing/./subscriptions'], ['S ID dotdot', s + '/id/..']]) {
    await check('off', name, target, valid, 404);
    await check('off', name + ' anonymous', target, {}, 404);
  }
  const neighborBefore = [];
  for (const target of neighbors) neighborBefore.push(await wire('POST', target, valid));
  await stopServer(server);
  server = await startServer(executable, data, web, [...baseFlags, '--employee-self-plan-purchase-enabled']);
  ports.push(Number(new URL(server.origin).port));
  for (const [name, target] of [['Q canonical', q], ['S canonical', s],
    ['Q double slash', '/self/api/v1/billing//plan-purchase-quotes'],
    ['S dot', '/self/api/v1/billing/./subscriptions'], ['S ID dotdot', s + '/id/..'],
    ['Q encoded letter', '/self/api/v1/billing/%70lan-purchase-quotes'],
    ['S encoded slash', '/self/api/v1/billing%2Fsubscriptions']]) {
    await check('on', name, target, valid, 400, 'invalid_request');
  }
  for (const target of ['/self/api/v1/billing//plan-purchase-quotes', '/self/api/v1/billing/./subscriptions']) {
    await check('on', 'anonymous ' + target, target, {}, 401, 'authentication_required');
    await check('on', 'wrong Origin ' + target, target, { ...valid, origin: 'http://wrong.invalid' }, 403, 'request_rejected');
    await check('on', 'wrong self header ' + target, target, { ...valid, selfValues: ['wrong'] }, 403, 'request_rejected');
    await check('on', 'duplicate self header ' + target, target, { ...valid, selfValues: ['1', '1'] }, 403, 'request_rejected');
  }
  const deep = depth => '%' + '25'.repeat(depth - 1) + '2F';
  for (const depth of [16, 17]) {
    await check('on-budget', `raw ${depth} layers`, q + deep(depth) + 'extra', valid, 400, 'invalid_request');
  }
  await check('on-budget', 'raw 18 layers beyond all views', q + deep(18) + 'extra', valid, 404);
  const parent = '/self/api/v1/billing/';
  const stem = 'plan-purchase-quotes';
  const hidden = parent + '/'.repeat(8192 - parent.length - stem.length) + stem;
  assert.equal(hidden.length, 8192, 'invalid 8192-byte fixture');
  await check('on-budget', '8192 then literal question', hidden + '?x', valid, 400, 'invalid_request');
  const overBudget = await wire('POST', hidden + 'x', valid);
  assert.equal(overBudget.status, 307, '8193rd path byte must retain mux owner');
  assert.equal(overBudget.location, q + 'x');
  assert.equal(overBudget.allow, '');
  assert.equal(overBudget.cache, 'no-store');
  assert.equal(overBudget.marker, false);
  evidence.push({ mode: 'on-budget', name: '8193rd path byte', method: 'POST',
    target: targetEvidence(hidden + 'x'), ...overBudget });
  for (let i = 0; i < readTargets.length; i++) {
    const after = await wire('GET', readTargets[i], valid);
    assert.deepEqual(after, readBefore[i], `purchase flag changed GET owner ${readTargets[i]}`);
    evidence.push({ mode: 'read-owner-unchanged', name: readTargets[i], method: 'GET', ...after });
  }
  for (let i = 0; i < neighbors.length; i++) {
    const target = neighbors[i];
    const result = await wire('POST', target, valid);
    assert.deepEqual(result, neighborBefore[i], `purchase flag changed sibling or neighbor ${target}`);
    assert.equal(result.cache, 'no-store');
    evidence.push({ mode: 'sibling-or-neighbor', name: target, method: 'POST', ...result });
  }
  console.log(JSON.stringify({ kind: 'synthetic-plan-purchase-route-boundary',
    cases: evidence.length, ports, evidence }));
} finally {
  await stopServer(server);
  await rm(scratch, { recursive: true, force: true });
}
