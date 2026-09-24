// Real-process scheduled-test acceptance with a synthetic catalog provider.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { adminClient, initialize, root, startServer, stopServer, waitFor } from './next-batch-smoke-lib.mjs';

const [server, webDir] = process.argv.slice(2);
assert.ok(server && webDir && path.isAbsolute(server) && path.isAbsolute(webDir));
const dataDir = path.join(root, 'scheduled-data');
await mkdir(dataDir, { recursive: true });
let catalogCalls = 0, generationCalls = 0, serviceProcess;
const upstream = http.createServer(async (request, response) => {
  for await (const ignored of request) { /* discard synthetic body */ }
  if (request.method === 'GET' && request.url === '/v1/models') {
    catalogCalls++;
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end('{"data":[{"id":"synthetic-model"}]}');
  } else {
    generationCalls++;
    response.writeHead(418).end();
  }
});
async function makeDue(planID) {
  const go = process.env.GO ?? 'go';
  const child = spawn(go, ['run', './scripts/acceptance-set-scheduled-due', '--db', path.join(dataDir, 'cpa-cloud.db'), '--plan', planID], {
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
  const account = await admin.request('/upstreams', 'POST', { name: 'Scheduled synthetic', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${upstream.address().port}`, api_key: 'synthetic-scheduled-key' }, 201);
  const plan = await admin.request('/scheduled-tests', 'POST', { name: 'Synthetic catalog', upstream_id: account.id, scope: 'catalog', interval_seconds: 300, enabled: true }, 201);
  await stopServer(serviceProcess); serviceProcess = undefined;
  await makeDue(plan.id);

  serviceProcess = await startServer(server, dataDir, webDir);
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal(catalogCalls, 0, 'Default-off scheduler made an outbound request');
  await stopServer(serviceProcess); serviceProcess = undefined;

  serviceProcess = await startServer(server, dataDir, webDir, ['--scheduled-tests-enabled']);
  admin = adminClient(serviceProcess.origin); await admin.login();
  await waitFor(async () => {
    const page = await admin.request(`/scheduled-tests/${plan.id}/runs?limit=10`);
    return page.items.length === 1 && page.items[0].state === 'completed' && page.items[0].result_code === 'catalog_ok';
  }, 'Scheduled catalog run did not complete');
  assert.equal(catalogCalls, 1); assert.equal(generationCalls, 0);
  await stopServer(serviceProcess); serviceProcess = undefined;

  serviceProcess = await startServer(server, dataDir, webDir, ['--scheduled-tests-enabled']);
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.equal(catalogCalls, 1, 'Restart replayed a completed scheduled operation');
  await stopServer(serviceProcess); serviceProcess = undefined;
  console.log('PASS: process scheduler default-off, one due catalog call, no generation, and restart no-replay');
} finally {
  await stopServer(serviceProcess);
  upstream.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
}
