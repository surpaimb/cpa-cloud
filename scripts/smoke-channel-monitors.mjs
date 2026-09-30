// Independently authored OBS-02 process acceptance: isolated service and synthetic catalog only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer, waitFor } from './next-batch-smoke-lib.mjs';

const [server, webDir] = process.argv.slice(2);
assert.ok(server && webDir && path.isAbsolute(server) && path.isAbsolute(webDir), 'Absolute server and Web paths required');
const dataDir = path.join(root, 'channel-monitor-data');
await mkdir(dataDir, { recursive: true });
let serviceProcess, catalogCalls = 0, generationCalls = 0;
const privateBody = 'synthetic-monitor-private-body';
const credential = 'synthetic-monitor-key';
const upstream = http.createServer(async (request, response) => {
  assert.equal(request.headers.authorization, `Bearer ${credential}`);
  for (const name of ['cookie', 'origin', 'x-csrf-token']) assert.equal(request.headers[name], undefined);
  for await (const ignored of request) { /* no provider payload retention */ }
  if (request.method === 'GET' && request.url === '/v1/models') {
    catalogCalls++;
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({ data: [{ id: privateBody }] }));
  } else {
    generationCalls++;
    response.writeHead(418).end();
  }
});

async function makeDue(planID) {
  const go = process.env.GO ?? 'go';
  const child = spawn(go, ['run', './scripts/acceptance-set-channel-monitor-due', '--db', path.join(dataDir, 'cpa-cloud.db'), '--plan', planID], {
    cwd: path.resolve(import.meta.dirname, '..'), windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'], env: process.env,
  });
  let output = '';
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { output += chunk; });
  const [code] = await once(child, 'exit');
  assert.equal(code, 0, `Set-due helper failed: ${output}`);
}

try {
  upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
  await initialize(server, dataDir);
  serviceProcess = await startServer(server, dataDir, webDir);
  let admin = adminClient(serviceProcess.origin); await admin.login();
  const features = (await admin.request('/system/status')).features;
  assert.equal(features.channel_monitor_configuration, true);
  assert.equal(features.channel_monitor_running, false);
  assert.equal(features.channel_monitor_retained_summary, true);
  const account = await admin.request('/upstreams', 'POST', {
    name: 'Synthetic channel account', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: credential,
  }, 201);
  const model = await admin.request('/models', 'POST', { id: 'channel-public-model', upstream_id: account.id, upstream_model: 'channel-provider-model' }, 201);
  const channel = await admin.request('/channels', 'POST', { name: 'Synthetic channel' }, 201);
  await admin.request(`/models/${model.id}/accounts`, 'PUT', {
    expected_revision: 0, items: [{ upstream_id: account.id, upstream_model: 'channel-provider-model', priority: 0, weight: 1, max_concurrency: 2, channel_id: channel.id }],
  });
  const input = { name: 'Synthetic catalog monitor', channel_id: channel.id, model_id: model.id, upstream_id: account.id, scope: 'catalog', interval_seconds: 300, enabled: true };
  const plan = await admin.request('/channel-monitors', 'POST', input, 201);
  assert.equal(plan.binding_state, 'valid');
  assert.ok(Date.parse(plan.next_run_at) > Date.now());
  assert.equal(plan.latest_result, null);
  const emptySummary = await admin.request(`/channel-monitors/${plan.id}/summary`);
  assert.equal(emptySummary.retained_completed, 0);
  assert.equal(emptySummary.running, 0);
  assert.equal(emptySummary.earliest_finished_at, null);
  assert.equal(catalogCalls, 0);
  assert.equal(generationCalls, 0);
  await stopServer(serviceProcess); serviceProcess = undefined;
  await makeDue(plan.id);

  serviceProcess = await startServer(server, dataDir, webDir);
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal((await adminClient(serviceProcess.origin).request('/system/status', 'GET', undefined, 401)).error.code, 'authentication_required');
  assert.equal(catalogCalls, 0, 'Default-off worker reached the provider');
  await stopServer(serviceProcess); serviceProcess = undefined;

  serviceProcess = await startServer(server, dataDir, webDir, ['--channel-monitors-enabled']);
  admin = adminClient(serviceProcess.origin); await admin.login();
  assert.equal((await admin.request('/system/status')).features.channel_monitor_running, true);
  const run = await waitFor(async () => {
    const page = await admin.request(`/channel-monitors/${plan.id}/runs?limit=10`);
    return page.items[0]?.state === 'completed' ? page.items[0] : null;
  }, 'Channel monitor catalog run did not complete');
  assert.equal(run.result_code, 'catalog_ok');
  assert.equal(run.channel_id, channel.id);
  assert.equal(run.model_id, model.id);
  assert.equal(run.upstream_id, account.id);
  assert.equal(run.pool_revision, 1);
  assert.equal(catalogCalls, 1);
  assert.equal(generationCalls, 0);
  const completedSummary = await admin.request(`/channel-monitors/${plan.id}/summary`);
  assert.equal(completedSummary.retained_completed, 1);
  assert.equal(completedSummary.running, 0);
  assert.equal(completedSummary.counts.catalog.catalog_ok, 1);
  assert.equal(completedSummary.retained_window_full, false);
  assert.ok(completedSummary.through_sequence > 0);
  assert.ok(!JSON.stringify(completedSummary).includes(credential) && !JSON.stringify(completedSummary).includes(privateBody));
  const current = await admin.request(`/channel-monitors/${plan.id}`);
  assert.equal(current.latest_result.operation_id, run.operation_id);
  assert.ok(!JSON.stringify(current).includes(privateBody));
  assert.ok(!JSON.stringify(run).includes(credential));
  await stopServer(serviceProcess); serviceProcess = undefined;

  serviceProcess = await startServer(server, dataDir, webDir, ['--channel-monitors-enabled']);
  admin = adminClient(serviceProcess.origin); await admin.login();
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal(catalogCalls, 1, 'Restart replayed a completed monitor operation');
  await admin.request(`/models/${model.id}/accounts`, 'PUT', {
    expected_revision: 1, items: [{ upstream_id: account.id, upstream_model: 'channel-provider-model', priority: 0, weight: 1, max_concurrency: 2, channel_id: channel.id }],
  });
  const stale = await admin.request(`/channel-monitors/${plan.id}`);
  assert.equal(stale.binding_state, 'stale');
  assert.equal(stale.latest_result, null, 'Old result was attributed to a changed route revision');
  await admin.request(`/channel-monitors/${plan.id}`, 'PATCH', { expected_revision: stale.revision, enabled: true }, 409);
  const rebound = await admin.request(`/channel-monitors/${plan.id}`, 'PATCH', { expected_revision: stale.revision, rebind: true });
  assert.equal(rebound.binding_state, 'valid');
  assert.equal(rebound.latest_result, null);
  const history = await admin.request(`/channel-monitors/${plan.id}/runs?limit=10`);
  assert.equal(history.items[0].operation_id, run.operation_id);
  const reboundSummary = await admin.request(`/channel-monitors/${plan.id}/summary`);
  assert.equal(reboundSummary.retained_completed, 1, 'Rebind erased or reattributed old retained history');
  assert.equal(reboundSummary.counts.catalog.catalog_ok, 1);
  await admin.request(`/channel-monitors/${plan.id}`, 'DELETE', { expected_revision: rebound.revision });
  const archivedSummary = await admin.request(`/channel-monitors/${plan.id}/summary`);
  assert.equal(archivedSummary.retained_completed, 1, 'Archive erased retained history');
  assert.equal(generationCalls, 0);
  assert.ok(!serviceProcess.output().includes(credential) && !serviceProcess.output().includes(privateBody), 'Sensitive data reached service logs');
  await stopServer(serviceProcess); serviceProcess = undefined;
  console.log('PASS: channel monitor default-off, bounded synthetic catalog, retained summary through rebind/archive, restart no-replay, no generation or secret leakage');
} finally {
  await stopServer(serviceProcess);
  upstream.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
}
