// Real-process lifecycle acceptance using only synthetic loopback traffic.
import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdir } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const [server, webDir] = process.argv.slice(2);
assert.ok(server && webDir && path.isAbsolute(server) && path.isAbsolute(webDir));
const dataDir = path.join(root, 'lifecycle-data');
await mkdir(dataDir, { recursive: true });
const secret = 'synthetic-lifecycle-' + randomBytes(16).toString('hex');
let calls = 0, serviceProcess;
const upstream = http.createServer(async (request, response) => {
  assert.equal(request.headers.authorization, `Bearer ${secret}`);
  for await (const ignored of request) { /* discard synthetic body */ }
  calls++;
  response.writeHead(200, { 'Content-Type': 'application/json' });
  response.end('{"id":"synthetic","object":"chat.completion","choices":[]}');
});
try {
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  await initialize(server, dataDir);
  serviceProcess = await startServer(server, dataDir, webDir);
  const admin = adminClient(serviceProcess.origin);
  await admin.login();
  const account = await admin.request('/upstreams', 'POST', { name: 'Lifecycle synthetic', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: secret }, 201);
  const model = await admin.request('/models', 'POST', { id: 'lifecycle-model', upstream_id: account.id, upstream_model: 'synthetic-upstream-model' }, 201);
  const employee = await admin.request('/employees', 'POST', { name: 'Lifecycle employee' }, 201);
  const issued = await admin.request(`/employees/${employee.id}/keys`, 'POST', { name: 'lifecycle', operation_id: randomUUID() }, 201);
  const requestModel = () => fetch(serviceProcess.origin + '/v1/chat/completions', { method: 'POST', headers: { Authorization: `Bearer ${issued.key}`, 'Content-Type': 'application/json' }, body: '{"model":"lifecycle-model","messages":[{"role":"user","content":"synthetic"}]}', signal: AbortSignal.timeout(5000) });
  let response = await requestModel();
  assert.equal(response.status, 200); await response.text();
  assert.equal(calls, 1);

  const archivedModel = await admin.request('/models/lifecycle-model', 'DELETE', { expected_revision: model.revision });
  assert.equal(archivedModel.archive_result, 'archived');
  response = await requestModel();
  assert.notEqual(response.status, 200); await response.text();
  assert.equal(calls, 1, 'Archived model dispatched synthetic traffic');
  assert.deepEqual((await admin.request('/models')).items, []);
  const tombstones = (await admin.request('/models?include_archived=true')).items;
  assert.equal(tombstones.length, 1); assert.equal(tombstones[0].archived, true);
  await admin.request('/models', 'POST', { id: 'lifecycle-model', upstream_id: account.id, upstream_model: 'synthetic-upstream-model' }, 409);

  const archivedAccount = await admin.request(`/upstreams/${account.id}`, 'DELETE', { expected_revision: account.revision });
  assert.equal(archivedAccount.archive_result, 'archived');
  assert.equal(archivedAccount.enabled, false);
  const archivedList = await admin.request('/upstreams?include_archived=true');
  const tombstone = archivedList.items.find(item => item.id === account.id);
  assert.equal(tombstone.archived, true);
  assert.ok(!JSON.stringify(archivedList).includes(secret), 'Credential escaped through lifecycle API');
  await admin.request('/scheduled-tests', 'POST', { name: 'Must reject tombstone', upstream_id: account.id, scope: 'catalog', interval_seconds: 300, enabled: false }, 400);
  await stopServer(serviceProcess); serviceProcess = undefined;
  console.log('PASS: process lifecycle CAS, tombstones, ID reservation, credential destruction, and zero post-archive dispatch');
} finally {
  await stopServer(serviceProcess);
  upstream.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
}
