// Independently authored process probe for
// docs/employee-self-monthly-renewal-route-boundary-contract.md.
// Usage: CPA_CLOUD_ACCEPTANCE_TEMP_ROOT=<absolute path> node this-file <absolute service exe> baseline|fixed
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdir, rm } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const executable = process.argv[2];
const mode = process.argv[3];
assert.ok(path.isAbsolute(executable) && ['baseline', 'fixed'].includes(mode));
const scratch = path.join(root, `employee-self-monthly-boundary-${randomUUID()}`);
const enabledFlags = ['--employee-self-service-enabled', '--employee-self-subscription-status-enabled',
  '--employee-self-wallet-balance-enabled', '--employee-self-subscription-renewal-enabled'];
const operations = ['renewal-quotes', 'renew'];
let server;

async function rawRequest(target, headers = {}) {
  const url = new URL(server.origin);
  return new Promise((resolve, reject) => {
    const request = http.request({ hostname: url.hostname, port: url.port, path: target, method: 'POST', headers }, response => {
      let body = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { body += chunk; });
      response.on('end', () => resolve({ status: response.statusCode, headers: response.headers, body }));
    });
    request.on('error', reject);
    request.setTimeout(10000, () => request.destroy(new Error('probe timed out')));
    request.end();
  });
}

try {
  await mkdir(scratch, { recursive: true });
  await initialize(executable, scratch);
  for (const enabled of [false, true]) {
    server = await startServer(executable, scratch, path.resolve('web/dist'), enabled ? enabledFlags : [
      '--employee-self-service-enabled', '--employee-self-subscription-status-enabled', '--employee-self-wallet-balance-enabled',
    ]);
    let auth = {};
    if (enabled) {
      const admin = adminClient(server.origin);
      await admin.login();
      const employee = await admin.request('/employees', 'POST', { name: 'Monthly boundary synthetic employee' }, 201);
      const enrollment = await admin.request(`/employees/${employee.id}/self-enrollment`, 'POST', {}, 201);
      const session = await fetch(server.origin + '/self/api/v1/enroll', {
        method: 'POST', headers: { Origin: server.origin, 'X-Self-Request': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ employee_id: employee.id, enrollment_secret: enrollment.enrollment_secret,
          password: 'synthetic-monthly-boundary-password' }),
      });
      assert.equal(session.status, 200);
      auth = { Cookie: session.headers.get('set-cookie').split(';')[0], Origin: server.origin,
        'X-Self-Request': '1', 'X-CSRF-Token': (await session.json()).csrf_token };
    }
    for (const operation of operations) {
      for (const target of [
        `/self//api/v1/billing/subscriptions/id/${operation}`,
        `/self/api/./v1/billing/subscriptions/id/${operation}`,
        `/self/api/v1/billing/subscriptions/id/../id/${operation}`,
      ]) {
        const result = await rawRequest(target, auth);
        const observed = { enabled, target, status: result.status, Location: result.headers.location ?? '',
          Allow: result.headers.allow ?? '', noStore: result.headers['cache-control'] ?? '',
          contentType: result.headers['content-type'] ?? '' };
        console.log(JSON.stringify(observed));
        if (mode === 'fixed') {
          assert.equal(result.status, enabled ? 400 : 404);
          assert.equal(observed.Location, '');
          assert.equal(observed.Allow, '');
          assert.equal(observed.noStore, 'no-store');
        } else {
          if (target.includes('/self//api/') || target.includes('/api/./v1/')) {
            assert.equal(result.status, 307);
            assert.ok(observed.Location);
          }
          assert.equal(observed.noStore, 'no-store');
        }
      }
    }
    await stopServer(server);
    server = undefined;
  }
} finally {
  await stopServer(server);
  if (path.dirname(scratch) === root && path.basename(scratch).startsWith('employee-self-monthly-boundary-')) {
    await rm(scratch, { recursive: true, force: true });
  }
}
