// Independently authored real-process AUDIT-01 acceptance using disposable data and synthetic metadata only.
import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { adminClient, initialize, root, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const [server, webDir] = process.argv.slice(2);
assert.ok(server && webDir && path.isAbsolute(server) && path.isAbsolute(webDir), 'Expected absolute server and web paths');
const dataDir = path.join(root, 'admin-audit-data');
const upstreamSecret = 'synthetic-audit-' + randomBytes(20).toString('hex');
const forbidden = [upstreamSecret, 'Authorization', 'Proxy-Authorization', 'prompt', 'response_body', 'payload_digest'];
const allowedEventKeys = ['action', 'actor_id', 'event_id', 'occurred_at', 'result', 'revision', 'source', 'target_id', 'target_type'];
let serviceProcess;
let combinedLogs = '';

function captureLogs(process) {
  if (process) combinedLogs += process.output();
}

function assertAuditPage(page) {
  assert.deepEqual(Object.keys(page).sort(), ['from', 'items', 'next_cursor', 'snapshot_at', 'sources', 'to']);
  assert.ok(Array.isArray(page.items));
  for (const item of page.items) {
    assert.deepEqual(Object.keys(item).sort(), allowedEventKeys);
    assert.equal(item.result, 'succeeded');
    assert.match(item.occurred_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/);
  }
  const encoded = JSON.stringify(page);
  for (const secret of forbidden) assert.ok(!encoded.includes(secret), `Audit response contained forbidden value ${secret}`);
}

try {
  await initialize(server, dataDir);
  serviceProcess = await startServer(server, dataDir, webDir);
  let admin = adminClient(serviceProcess.origin);

  let response = await fetch(serviceProcess.origin + '/admin/api/v1/audit/events');
  assert.equal(response.status, 401, 'Anonymous audit read was not rejected');
  await response.text();
  const login = await admin.login();
  const status = await admin.request('/system/status');
  assert.equal(status.features.admin_audit_overview, true, 'Audit capability was not advertised');

  response = await fetch(serviceProcess.origin + '/admin/api/v1/audit/events', { headers: { Cookie: admin.cookie(), Origin: 'http://wrong-origin.invalid' } });
  assert.equal(response.status, 403, 'Cross-site audit read was not rejected');
  await response.text();
  response = await fetch(serviceProcess.origin + '/admin/api/v1/audit/events?limit=1', { headers: { Cookie: admin.cookie(), Origin: serviceProcess.origin } });
  assert.equal(response.status, 200, 'Read-only audit unexpectedly required CSRF');
  await response.text();

  const poolGroup = await admin.request('/account-groups', 'POST', { name: 'Synthetic audit pool group' }, 201);
  const upstream = await admin.request('/upstreams', 'POST', { name: 'Synthetic audit account', provider_kind: 'openai-compatible', endpoint: 'http://127.0.0.1:9/v1', api_key: upstreamSecret }, 201);
  const model = await admin.request('/models', 'POST', { id: 'synthetic-audit-model', upstream_id: upstream.id, upstream_model: 'synthetic-model' }, 201);
  await admin.request(`/models/${encodeURIComponent(model.id)}`, 'PATCH', { expected_revision: model.revision, enabled: false });
  const governance = await admin.request('/governance/groups', 'POST', { operation_id: randomUUID(), name: 'Synthetic audit governance', employee_ids: [] });
  const employee = await admin.request('/employees', 'POST', { name: 'Synthetic audit employee' }, 201);
  const budget = await admin.request('/budgets', 'POST', {
    operation_id: randomUUID(), scope_kind: 'employee', scope_id: employee.id, protocol: null, model: null,
    enabled: true, token_limit: 100, cost_limit_micro: null, currency: null,
  });

  const first = await admin.request('/audit/events?limit=2');
  assertAuditPage(first);
  assert.equal(first.items.length, 2);
  assert.ok(first.next_cursor, 'Synthetic four-source result did not paginate');
  const afterWatermark = await admin.request('/account-groups', 'POST', { name: 'Must stay outside cursor chain' }, 201);
  const items = [...first.items];
  let cursor = first.next_cursor;
  for (let pageNumber = 2; cursor && pageNumber <= 20; pageNumber++) {
    const page = await admin.request('/audit/events?cursor=' + encodeURIComponent(cursor));
    assertAuditPage(page);
    items.push(...page.items);
    cursor = page.next_cursor;
  }
  assert.equal(cursor, null, 'Audit cursor chain exceeded the bounded synthetic result');
  assert.deepEqual(new Set(items.map(item => item.source)), new Set(['account_pool', 'account_lifecycle', 'governance_management', 'governance_general_budget']));
  assert.ok(items.some(item => item.target_id === poolGroup.id));
  assert.ok(items.some(item => item.target_id === model.id));
  assert.ok(items.some(item => item.target_id === governance.resource_id));
  assert.ok(items.some(item => item.target_id === budget.resource_id));
  assert.ok(!items.some(item => item.target_id === afterWatermark.id), 'Post-watermark fact entered the cursor chain');
  assert.equal(new Set(items.map(item => `${item.source}:${item.event_id}`)).size, items.length, 'Cursor chain duplicated an event');

  const exact = await admin.request('/audit/events?sources=governance_general_budget&target_id=' + encodeURIComponent(budget.resource_id) + '&result=succeeded');
  assertAuditPage(exact);
  assert.equal(exact.items.length, 1);
  assert.equal(exact.items[0].source, 'governance_general_budget');
  const injection = await admin.request('/audit/events?action=' + encodeURIComponent("x' OR 1=1 --"));
  assert.deepEqual(injection.items, [], 'SQL-looking exact filter matched audit facts');
  await admin.request('/audit/events?unknown=value', 'GET', undefined, 400);
  await admin.request('/audit/events?sources=unknown', 'GET', undefined, 400);
  await admin.request('/audit/events?cursor=' + encodeURIComponent(first.next_cursor) + '&limit=2', 'GET', undefined, 400);

  const firstWindow = { from: first.from, to: first.to, snapshot_at: first.snapshot_at };
  captureLogs(serviceProcess);
  await stopServer(serviceProcess); serviceProcess = undefined;
  const databaseBytes = await readFile(path.join(dataDir, 'cpa-cloud.db'));
  assert.ok(!databaseBytes.includes(Buffer.from(upstreamSecret)), 'Plaintext synthetic upstream secret persisted');
  const database = new DatabaseSync(path.join(dataDir, 'cpa-cloud.db'), { readOnly: true });
  try {
    for (const table of ['account_pool_audit', 'account_lifecycle_audit', 'governance_management_audit', 'governance_general_budget_audit']) {
      assert.ok(database.prepare(`SELECT COUNT(*) AS count FROM ${table}`).get().count >= 1, `${table} lost synthetic facts`);
    }
  } finally { database.close(); }

  serviceProcess = await startServer(server, dataDir, webDir);
  admin = adminClient(serviceProcess.origin);
  await admin.login();
  const restarted = await admin.request('/audit/events?target_id=' + encodeURIComponent(budget.resource_id));
  assertAuditPage(restarted);
  assert.equal(restarted.items.length, 1, 'Restart did not recover persisted audit facts');
  assert.notDeepEqual({ from: restarted.from, to: restarted.to, snapshot_at: restarted.snapshot_at }, firstWindow, 'Fresh restart query reused an old page snapshot');
  captureLogs(serviceProcess);
  await stopServer(serviceProcess); serviceProcess = undefined;
  for (const value of [upstreamSecret, login.csrf_token]) assert.ok(!combinedLogs.includes(value), 'Sensitive value appeared in service logs');
  console.log('PASS admin audit: auth/origin/no-CSRF/four-source/filter/cursor-watermark/restart/metadata-only/secret-scan');
} finally {
  captureLogs(serviceProcess);
  await stopServer(serviceProcess);
}
