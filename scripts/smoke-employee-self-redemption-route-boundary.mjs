// Independently authored process acceptance for
// docs/employee-self-redemption-route-boundary-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute parent> node this-file <absolute service exe>
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
assert.ok(executable && path.isAbsolute(executable));
const scratch = path.join(root, `employee-self-redemption-route-boundary-${randomUUID()}`);
const webDir = path.join(scratch, 'web');
const marker = `route-boundary-marker-${randomUUID()}`;
const selfPassword = 'synthetic-boundary-employee-password-123';
const route = '/self/api/v1/billing/redemptions';
let processHandle;
const evidence = { source: 'uncommitted-route-boundary-implementation', origins: [], probes: [] };

function unchunk(body) {
  const chunks = [];
  let offset = 0;
  for (;;) {
    const end = body.indexOf('\r\n', offset);
    assert.ok(end >= 0, 'invalid chunked response');
    const size = Number.parseInt(body.subarray(offset, end).toString('ascii').split(';')[0], 16);
    assert.ok(Number.isFinite(size), 'invalid chunk size');
    offset = end + 2;
    if (size === 0) return Buffer.concat(chunks).toString('utf8');
    chunks.push(body.subarray(offset, offset + size));
    offset += size + 2;
  }
}

async function rawRequest(origin, method, target, { cookie = '', csrf = '', requestOrigin = origin,
  writeHeaders = true, extraHeaders = [], body = '', label = '' } = {}) {
  const url = new URL(origin);
  const payload = Buffer.from(body);
  const headerLines = [`Host: ${url.host}`, ...(requestOrigin ? [`Origin: ${requestOrigin}`] : []),
    ...(writeHeaders ? ['X-Self-Request: 1', `X-CSRF-Token: ${csrf}`] : []),
    ...(cookie ? [`Cookie: ${cookie}`] : []), ...extraHeaders, 'Content-Type: application/json',
    `Content-Length: ${payload.length}`, 'Connection: close'];
  const requestLine = `${method} ${target} HTTP/1.1`;
  const bytes = await new Promise((resolve, reject) => {
    const socket = net.connect({ host: url.hostname, port: Number(url.port) });
    const chunks = [];
    socket.setTimeout(10000, () => socket.destroy(new Error('raw HTTP timeout')));
    socket.on('connect', () => socket.write(Buffer.concat([
      Buffer.from(requestLine + '\r\n' + headerLines.join('\r\n') + '\r\n\r\n'), payload,
    ])));
    socket.on('data', chunk => chunks.push(chunk));
    socket.on('end', () => resolve(Buffer.concat(chunks)));
    socket.on('error', reject);
  });
  const split = bytes.indexOf('\r\n\r\n');
  assert.ok(split > 0, 'missing raw response headers');
  const head = bytes.subarray(0, split).toString('latin1').split('\r\n');
  const headers = {};
  for (const line of head.slice(1)) {
    const cut = line.indexOf(':');
    if (cut > 0) headers[line.slice(0, cut).toLowerCase()] = line.slice(cut + 1).trim();
  }
  const responseBody = bytes.subarray(split + 4);
  const text = /chunked/i.test(headers['transfer-encoding'] ?? '')
    ? unchunk(responseBody) : responseBody.toString('utf8');
  let code = null;
  if ((headers['content-type'] ?? '').includes('application/json')) {
    try { code = JSON.parse(text)?.error?.code ?? null; } catch { /* recorded as invalid JSON below */ }
  }
  const result = { label, request_line: requestLine, status: Number(head[0].split(' ')[1]),
    location: headers.location ?? null, allow: headers.allow ?? null,
    cache_control: headers['cache-control'] ?? null, content_type: headers['content-type'] ?? null,
    error_code: code, body_preview: text.slice(0, 256), web_marker_seen: text.includes(marker) };
  evidence.probes.push(result);
  return result;
}

function expect(result, status, code = null, allow = null) {
  assert.equal(result.status, status, `${result.request_line}: status`);
  assert.equal(result.cache_control, 'no-store', `${result.request_line}: cache`);
  assert.equal(result.location, null, `${result.request_line}: redirect`);
  assert.equal(result.allow, allow, `${result.request_line}: allow`);
  assert.equal(result.web_marker_seen, false, `${result.request_line}: WebDir fallback`);
  if (code) {
    assert.equal(result.error_code, code, `${result.request_line}: error code`);
    assert.match(result.content_type ?? '', /^application\/json/, `${result.request_line}: content type`);
  }
}

async function selfCall(origin, target, cookie = '') {
  const response = await fetch(origin + target, { headers: { Origin: origin, ...(cookie ? { Cookie: cookie } : {}) },
    redirect: 'manual', signal: AbortSignal.timeout(10000) });
  assert.equal(response.status, 200, `GET ${target}`);
  return response.json();
}

try {
  await mkdir(webDir, { recursive: true });
  await writeFile(path.join(webDir, 'index.html'), `<!doctype html><title>${marker}</title><main>${marker}</main>`);
  await initialize(executable, scratch);

  processHandle = await startServer(executable, scratch, webDir,
    ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled']);
  evidence.origins.push(processHandle.origin);
  for (const target of [route, route + '%252fextra', route + '/%252e%252e',
    '/self/api/v1/billing//redemptions']) {
    for (const method of ['POST', 'GET', 'HEAD', 'PUT']) {
      expect(await rawRequest(processHandle.origin, method, target, { writeHeaders: false }), 404);
    }
  }
  for (const depth of [16, 17, 18]) {
    const target = '/self/api/v1/billing/%' + '25'.repeat(depth) + '72edemptions';
    for (const method of ['POST', 'GET', 'HEAD', 'PUT']) {
      expect(await rawRequest(processHandle.origin, method, target, { writeHeaders: false }), 404);
    }
  }
  await stopServer(processHandle);
  processHandle = undefined;

  processHandle = await startServer(executable, scratch, webDir,
    ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled', '--employee-self-redemption-enabled']);
  evidence.origins.push(processHandle.origin);
  const admin = adminClient(processHandle.origin);
  await admin.login();
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic boundary employee' }, 201);
  const key = await admin.request(`/employees/${employee.id}/keys`, 'POST',
    { name: 'Synthetic boundary key', operation_id: randomUUID() }, 201);
  const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
  const signedIn = await fetch(processHandle.origin + '/self/api/v1/enroll', { method: 'POST', redirect: 'manual',
    headers: { Origin: processHandle.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
    body: JSON.stringify({ employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret,
      password: selfPassword }), signal: AbortSignal.timeout(10000) });
  assert.equal(signedIn.status, 200);
  const cookie = signedIn.headers.get('set-cookie')?.split(';')[0] ?? '';
  const csrf = (await signedIn.json()).csrf_token;
  assert.ok(cookie && csrf);
  const origin = processHandle.origin;
  const beforeBalance = await selfCall(origin, '/self/api/v1/billing/balance?currency=USD', cookie);
  const beforeCodes = await admin.request('/billing/redemption-codes');
  assert.equal((await selfCall(origin, '/self/api/v1/session', cookie)).features.employee_self_redemption, true);

  expect(await rawRequest(origin, 'GET', route, { cookie, writeHeaders: false }), 405, 'method_not_allowed', 'POST');
  expect(await rawRequest(origin, 'GET', route, { writeHeaders: false }), 401, 'authentication_required');
  for (const sibling of ['/self/api/v1/billing/redemption-credits',
    '/self/api/v1/billing/redemptions-other', '/self/api/v1/billing/redemptions%252dother',
    route + '%25other', route + '%25zz', route + '%2525other']) {
    expect(await rawRequest(origin, 'GET', sibling, { cookie, writeHeaders: false }), 404);
    expect(await rawRequest(origin, 'POST', sibling, { cookie, csrf, body: '{}' }), 404);
  }
  const alias = route + '%252fextra';
  expect(await rawRequest(origin, 'POST', alias, { csrf, body: '{}' }), 401, 'authentication_required');
  expect(await rawRequest(origin, 'POST', alias, { cookie: admin.cookie(), csrf, body: '{}', label: 'admin-cookie' }),
    401, 'authentication_required');
  expect(await rawRequest(origin, 'POST', alias, { csrf, extraHeaders: [`Authorization: Bearer ${key.key}`],
    body: '{}', label: 'employee-bearer-without-self-session' }),
    401, 'authentication_required');
  expect(await rawRequest(origin, 'POST', alias, { cookie, csrf, requestOrigin: '', body: '{}', label: 'missing-origin' }),
    403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, csrf, extraHeaders: [`Origin: ${origin}`],
    body: '{}', label: 'duplicate-origin' }),
    403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, csrf, requestOrigin: 'http://wrong.invalid', body: '{}' }),
    403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, csrf: 'wrong', body: '{}' }),
    403, 'request_rejected');
  expect(await rawRequest(origin, 'GET', alias, { cookie, writeHeaders: false }),
    403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, writeHeaders: false,
    extraHeaders: [`X-CSRF-Token: ${csrf}`], body: '{}', label: 'missing-x-self-request' }),
  403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, writeHeaders: false,
    extraHeaders: ['X-Self-Request: 2', `X-CSRF-Token: ${csrf}`], body: '{}', label: 'wrong-x-self-request' }),
  403, 'request_rejected');
  expect(await rawRequest(origin, 'POST', alias, { cookie, writeHeaders: false,
    extraHeaders: ['X-Self-Request: 1'], body: '{}', label: 'missing-csrf' }),
  403, 'request_rejected');
  for (const depth of [16, 17, 18]) {
    const encoded = '%' + '25'.repeat(depth);
    for (const method of ['POST', 'GET', 'HEAD', 'PUT']) {
      const result = await rawRequest(origin, method, '/self/api/v1/billing/' + encoded + '72edemptions');
      expect(result, 401, method === 'HEAD' ? null : 'authentication_required');
    }
    for (const target of [
      '/self/api/v1/billing/' + encoded + '52' + encoded + '65demptions',
      '/self/api/v1/billing' + encoded + '2fRedemptions',
      route + encoded + '2Fextra',
      '/self/api/v1/billing/./' + encoded + '72edemptions',
    ]) {
      expect(await rawRequest(origin, 'POST', target), 401, 'authentication_required');
    }
    for (const target of [
      '/self/api/v1/billing/' + encoded + '72edemptions-other',
      route + encoded + '2dother',
      '/self/api/v1/billing/' + encoded + '72edemptionsx',
    ]) {
      expect(await rawRequest(origin, 'POST', target), 404);
    }
  }
  const cap = 8192;
  const parent = '/self/api/v1/billing/';
  const padding = '/'.repeat(cap - parent.length - 'redemptions'.length - 1);
  const visibleBoundary = parent + padding + 'redemptions/';
  assert.equal(Buffer.byteLength(visibleBoundary), cap);
  expect(await rawRequest(origin, 'POST', visibleBoundary + 'extra'), 401, 'authentication_required');
  const visibleNeighbor = await rawRequest(origin, 'POST', parent + padding + 'redemptionsx');
  assert.notEqual(visibleNeighbor.error_code, 'authentication_required', 'neighbor at byte cap was claimed');
  const cleanHead = '/self/api/v1/billing/foo/redemptions/../../';
  const cleanPrefix = cleanHead + '/'.repeat(cap - cleanHead.length - 'redemptions'.length - 1) + 'redemptions';
  assert.equal(Buffer.byteLength(cleanPrefix) + 1, cap);
  expect(await rawRequest(origin, 'POST', cleanPrefix + '/'), 401, 'authentication_required');
  const cleanNeighbor = await rawRequest(origin, 'POST', cleanPrefix + 'x');
  assert.notEqual(cleanNeighbor.error_code, 'authentication_required', 'cleaned neighbor at byte cap was claimed');
  const hiddenBoundary = route + '%' + '25'.repeat(cap / 2 + 40);
  const hiddenFamily = await rawRequest(origin, 'POST', hiddenBoundary + '2fother');
  const hiddenNeighbor = await rawRequest(origin, 'POST', hiddenBoundary + '2dother');
  for (const result of [hiddenFamily, hiddenNeighbor]) {
    assert.notEqual(result.error_code, 'authentication_required', 'unproven over-limit target was claimed');
    assert.equal(result.web_marker_seen, false, 'over-limit target reached WebDir');
  }
  for (const [method, target] of [
    ['POST', alias], ['GET', route + '%25252fextra'], ['POST', route + '%255cextra'],
    ['POST', route + '/%252e%252e'], ['PUT', route + '%253fquery=1'],
  ]) {
    expect(await rawRequest(origin, method, target, { cookie, csrf, body: method === 'POST' ? '{}' : '' }),
      400, 'invalid_request');
  }
  expect(await rawRequest(origin, 'POST', alias, { cookie, csrf, body: '{}' }), 429, 'login_limited');
  assert.deepEqual(await selfCall(origin, '/self/api/v1/billing/balance?currency=USD', cookie), beforeBalance);
  assert.deepEqual(await admin.request('/billing/redemption-codes'), beforeCodes);
  assert.ok(!processHandle.output().includes(selfPassword), 'synthetic password appeared in service log');
  assert.ok(!processHandle.output().includes(key.key), 'synthetic key appeared in service log');
  await stopServer(processHandle);
  processHandle = undefined;
  for (const depth of [16, 17, 18]) {
    processHandle = await startServer(executable, scratch, webDir,
      ['--employee-self-service-enabled', '--employee-self-wallet-balance-enabled', '--employee-self-redemption-enabled']);
    evidence.origins.push(processHandle.origin);
    const target = '/self/api/v1/billing/%' + '25'.repeat(depth) + (depth === 18 ? '52edemptions' : '72edemptions');
    for (const method of ['POST', 'GET', 'HEAD', 'PUT']) {
      const result = await rawRequest(processHandle.origin, method, target,
        { cookie, csrf, body: method === 'POST' ? '{}' : '', label: `depth-${depth}-${method}-write-gate` });
      expect(result, 400, method === 'HEAD' ? null : 'invalid_request');
    }
    assert.ok(!processHandle.output().includes(selfPassword) && !processHandle.output().includes(key.key),
      'synthetic secret appeared in service log');
    if (depth !== 18) {
      await stopServer(processHandle);
      processHandle = undefined;
    }
  }
  const finalOrigin = processHandle.origin;
  const finalAdmin = adminClient(finalOrigin);
  await finalAdmin.login();
  assert.deepEqual(await selfCall(finalOrigin, '/self/api/v1/billing/balance?currency=USD', cookie), beforeBalance);
  assert.deepEqual(await finalAdmin.request('/billing/redemption-codes'), beforeCodes);
  evidence.log_secret_free = true;
  evidence.finance_unchanged = true;
  evidence.dynamic_non_8787 = evidence.origins.every(value => new URL(value).port !== '8787');
  assert.equal(evidence.dynamic_non_8787, true);
  const serialized = JSON.stringify(evidence, null, 2) + '\n';
  for (const secret of [selfPassword, key.key, cookie, csrf, admin.cookie()]) {
    assert.ok(secret && !serialized.includes(secret), 'secret appeared in process evidence');
  }
  await writeFile(path.join(root, 'green-evidence.json'), serialized);
  console.log(`employee self redemption route boundary process acceptance passed: ${evidence.probes.length} raw probes`);
} finally {
  await stopServer(processHandle);
}
