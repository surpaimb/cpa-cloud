// Independently authored for docs/employee-self-estimated-cost-route-boundary-contract.md.
// Synthetic data, isolated local processes and raw first-response TCP only.
import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable), 'absolute service executable required');
const scratch = path.join(root, `estimated-cost-route-boundary-${randomUUID()}`);
const data = path.join(scratch, 'data');
const web = path.join(scratch, 'web');
const marker = `synthetic-cost-boundary-web-${randomUUID()}`;
const p = '/self/api/v1/usage/estimated-cost-summary';
const baseFlags = ['--employee-self-service-enabled'];
const costFlag = '--employee-self-upstream-estimated-cost-summary-enabled';
const siblingFlags = [
  '--employee-self-wallet-balance-enabled', '--employee-self-wallet-activity-enabled',
  '--employee-self-wallet-entry-classification-enabled',
  '--employee-self-topup-credit-history-enabled',
  '--employee-self-admin-adjustment-history-enabled',
  '--employee-self-redemption-credit-history-enabled',
  '--employee-self-redemption-enabled',
];
let server;
const evidence = { kind: 'synthetic-estimated-cost-route-boundary', binarySha256: '', ports: [], cases: [] };

async function raw(target, cookie = '', origin = server.origin, method = 'GET') {
  const destination = new URL(server.origin);
  const port = Number(destination.port);
  assert.notEqual(port, 8787, 'protected service port');
  const request = `${method} ${target} HTTP/1.1\r\nHost: ${destination.host}\r\nConnection: close\r\nOrigin: ${origin}\r\n${cookie ? `Cookie: ${cookie}\r\n` : ''}Content-Length: 0\r\n\r\n`;
  const response = await new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: destination.hostname, port });
    const chunks = [];
    const timer = setTimeout(() => socket.destroy(new Error('raw TCP timeout')), 10000);
    socket.on('connect', () => socket.write(request));
    socket.on('data', chunk => chunks.push(chunk));
    socket.on('error', error => { clearTimeout(timer); reject(error); });
    socket.on('end', () => { clearTimeout(timer); resolve(Buffer.concat(chunks).toString('utf8')); });
  });
  const [head, body = ''] = response.split('\r\n\r\n', 2);
  assert.ok(head?.startsWith('HTTP/1.1 '), 'missing response status');
  const header = name => head.match(new RegExp(`^${name}:\\s*(.*)$`, 'im'))?.[1]?.trim() ?? '';
  return {
    status: Number(head.match(/^HTTP\/1\.1 (\d{3})/)?.[1]),
    contentType: header('Content-Type'), cache: header('Cache-Control'),
    location: header('Location'), allow: header('Allow'),
    code: body.match(/"code"\s*:\s*"([^"]+)"/)?.[1] ?? '',
    webMarker: body.includes(marker),
  };
}

async function check(mode, name, target, cookie, expected, origin, method = 'GET') {
  const result = await raw(target, cookie, origin, method);
  assert.equal(result.status, expected, `${mode}/${name}: ${JSON.stringify(result)}`);
  assert.equal(result.cache, 'no-store', `${mode}/${name} cache`);
  assert.equal(result.location, '', `${mode}/${name} redirect`);
  assert.equal(result.webMarker, false, `${mode}/${name} web fallback`);
  evidence.cases.push({ mode, name, method, target: target.length > 1024 ? target.slice(0, 100) + '…' : target,
    targetLength: target.length, ...result });
}

try {
  evidence.binarySha256 = createHash('sha256').update(await readFile(executable)).digest('hex');
  await mkdir(web, { recursive: true });
  await writeFile(path.join(web, 'index.html'), marker);
  await initialize(executable, data);
  server = await startServer(executable, data, web, [...baseFlags, costFlag]);
  evidence.ports.push(Number(new URL(server.origin).port));
  const admin = adminClient(server.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic cost boundary employee' }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const enrolled = await fetch(server.origin + '/self/api/v1/enroll', {
    method: 'POST', signal: AbortSignal.timeout(10000),
    headers: { Origin: server.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
    body: JSON.stringify({ employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret,
      password: `synthetic-boundary-password-${randomUUID()}` }),
  });
  assert.equal(enrolled.status, 200, 'enrollment failed');
  const cookie = enrolled.headers.get('set-cookie')?.split(';')[0];
  assert.ok(cookie, 'missing self cookie');

  const siblings = {
    topup: p + '/../../billing/topup-credits',
    adjustment: p + '/../../billing/admin-adjustments',
    redemptionCredit: p + '/../../billing/redemption-credits',
    classification: p + '/../../billing/entry-classifications',
    redemption: p + '/../../billing/redemptions',
    reverse: '/self/api/v1/billing/redemptions/../../usage/estimated-cost-summary',
    upper: '/SELF/API/V1/USAGE/ESTIMATED-COST-SUMMARY/../../BILLING/REDEMPTIONS',
    duplicateSlash: p + '/..//../billing//redemptions',
    backslash: p + '/..\\..\\billing\\redemptions',
    encodedQuestion: p + '/../../billing/redemptions%3Fx',
    encodedHash: p + '/../../billing/redemptions%23x',
  };
  for (const [name, target] of Object.entries(siblings)) await check('cost-on-siblings-off', name, target, cookie, 404);
  await check('cost-on-siblings-off', 'redemptionPostOff', siblings.redemption, cookie, 404, server.origin, 'POST');
  await check('cost-on-siblings-off', 'outerCancel', p + '/../../billing/subscriptions/id/cancel', cookie, 404);
  await check('cost-on-siblings-off', 'classificationBackslashNegative', p + '/..\\..\\billing\\entry-classifications', cookie, 400);
  await check('cost-on-siblings-off', 'neighbor', p + '-other', cookie, 404);
  await check('cost-on-siblings-off', 'encodedNeighbor', p + '%2Dother', cookie, 404);
  await check('cost-on-siblings-off', 'costOnly', p + '/extra', cookie, 400);
  await check('cost-on-siblings-off', 'anonymousCost', p + '/extra', '', 401);
  await check('cost-on-siblings-off', 'wrongOriginCost', p + '/extra', cookie, 403, 'http://wrong.invalid');
  const deep = depth => {
    const dot = '%' + '25'.repeat(depth - 1) + '2E';
    return p + '/' + dot + dot + '/' + dot + dot + '/billing/redemptions';
  };
  for (const depth of [17, 18]) await check('cost-on-siblings-off', `deep${depth}`, deep(depth), cookie, 400);
  const d18 = '%' + '25'.repeat(17) + '2F';
  const duplicateFamily = '/self' + d18 + d18 + 'api/v1/usage/estimated-cost-summary';
  await check('cost-on-siblings-off', 'deepDoubleSeparatorFamily', duplicateFamily, cookie, 400);
  const duplicateNeighbor = await raw(duplicateFamily + '-other', cookie);
  assert.equal(duplicateNeighbor.status, 200, 'deep duplicate separator neighbor should remain ordinary WebDir fallback');
  assert.equal(duplicateNeighbor.webMarker, true);
  evidence.cases.push({ mode: 'cost-on-siblings-off', name: 'deepDoubleSeparatorNeighbor',
    target: duplicateFamily + '-other', ...duplicateNeighbor });
  await check('cost-on-siblings-off', 'longRawCostChild', p + '/' + 'x'.repeat(20000), cookie, 400);
  const near = p + '/' + '/'.repeat(8192 - p.length - '/../../billing/redemptions'.length) + '../../billing/redemptions';
  assert.equal(near.length, 8192);
  await check('cost-on-siblings-off', 'innerAtBudget', near, cookie, 404);
  await check('cost-on-siblings-off', 'innerOverBudget', '/' + near, cookie, 400);
  await stopServer(server);
  server = await startServer(executable, data, web, [...baseFlags, ...siblingFlags]);
  evidence.ports.push(Number(new URL(server.origin).port));
  for (const [name, target] of Object.entries(siblings)) {
    const result = await raw(target, cookie);
    const redemptionFamily = !['topup', 'adjustment', 'redemptionCredit', 'classification'].includes(name);
    assert.equal(result.status, redemptionFamily ? 403 : 400, `cost-off-siblings-on/${name} status`);
    assert.equal(result.code, redemptionFamily ? 'request_rejected' : 'invalid_request', `cost-off-siblings-on/${name} code`);
    assert.equal(result.cache, 'no-store');
    assert.equal(result.location, '');
    assert.equal(result.webMarker, false);
    evidence.cases.push({ mode: 'cost-off-siblings-on', name, target, ...result });
  }
  const postRedemption = await raw(siblings.redemption, cookie, server.origin, 'POST');
  assert.equal(postRedemption.status, 403);
  assert.equal(postRedemption.code, 'request_rejected');
  assert.equal(postRedemption.location, '');
  evidence.cases.push({ mode: 'cost-off-siblings-on', name: 'redemptionPostGate', method: 'POST',
    target: siblings.redemption, ...postRedemption });
  for (const depth of [17, 18]) await check('cost-off-siblings-on', `deep${depth}`, deep(depth), cookie, 404);
  await check('cost-off-siblings-on', 'deepDoubleSeparatorFamily', duplicateFamily, cookie, 404);
  await check('cost-off-siblings-on', 'classificationBackslashNegative', p + '/..\\..\\billing\\entry-classifications', cookie, 404);
  await writeFile(path.join(scratch, 'evidence.json'), JSON.stringify(evidence, null, 2) + '\n');
  console.log(`employee estimated-cost route boundary isolated process acceptance passed (${evidence.cases.length} cases)`);
} finally {
  await stopServer(server);
}
