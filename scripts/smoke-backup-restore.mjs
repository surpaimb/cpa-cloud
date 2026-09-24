// Real-process encrypted backup/restore acceptance with synthetic credentials.
import assert from 'node:assert/strict';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { adminClient, initialize, password, root, run, startServer, stopServer } from './next-batch-smoke-lib.mjs';

const [server, backup, webDir] = process.argv.slice(2);
assert.ok(server && backup && webDir && [server, backup, webDir].every(path.isAbsolute));
const source = path.join(root, 'backup-source');
const target = path.join(root, 'backup-restored');
const packagePath = path.join(root, 'synthetic.cpacb');
const tamperedPath = path.join(root, 'tampered.cpacb');
const backupPassword = 'synthetic-backup-' + randomBytes(18).toString('hex');
const upstreamSecret = 'synthetic-upstream-' + randomBytes(18).toString('hex');
let serviceProcess;
const digest = async file => createHash('sha256').update(await readFile(file)).digest('hex');
try {
  await mkdir(source, { recursive: true });
  await initialize(server, source);
  serviceProcess = await startServer(server, source, webDir);
  let admin = adminClient(serviceProcess.origin); await admin.login();
  const oldCookie = admin.cookie();
  const upstream = await admin.request('/upstreams', 'POST', { name: 'Backup synthetic', provider_kind: 'openai-compatible', endpoint: 'http://127.0.0.1:9', api_key: upstreamSecret }, 201);
  await admin.request('/models', 'POST', { id: 'backup-model', upstream_id: upstream.id, upstream_model: 'synthetic-model' }, 201);
  const employee = await admin.request('/employees', 'POST', { name: 'Backup employee' }, 201);
  const issued = await admin.request(`/employees/${employee.id}/keys`, 'POST', { name: 'backup', operation_id: randomUUID() }, 201);
  await stopServer(serviceProcess); serviceProcess = undefined;

  const sourceDB = path.join(source, 'cpa-cloud.db'), sourceKey = path.join(source, 'master.key');
  const before = [await digest(sourceDB), await digest(sourceKey)];
  await run(backup, ['create', '--data-dir', source, '--output', packagePath], backupPassword + '\n');
  await run(backup, ['verify', '--input', packagePath], backupPassword + '\n');
  await run(backup, ['verify', '--input', packagePath], 'wrong-synthetic-password\n', 1);
  await copyFile(packagePath, tamperedPath);
  const tampered = await readFile(tamperedPath); tampered[tampered.length - 1] ^= 0x01; await writeFile(tamperedPath, tampered);
  await run(backup, ['verify', '--input', tamperedPath], backupPassword + '\n', 1);
  await run(backup, ['restore', '--input', packagePath, '--data-dir', target], backupPassword + '\n');
  await run(backup, ['restore', '--input', packagePath, '--data-dir', target], backupPassword + '\n', 1);
  assert.deepEqual([await digest(sourceDB), await digest(sourceKey)], before, 'Backup/restore mutated durable source files');
  const encrypted = await readFile(packagePath);
  for (const secret of [password, backupPassword, upstreamSecret, issued.key]) assert.ok(!encrypted.includes(Buffer.from(secret)), 'Encrypted package exposed a synthetic secret');

  serviceProcess = await startServer(server, target, webDir);
  let response = await fetch(serviceProcess.origin + '/admin/api/v1/session', { headers: { Cookie: oldCookie }, signal: AbortSignal.timeout(5000) });
  assert.equal(response.status, 401); await response.text();
  response = await fetch(serviceProcess.origin + '/v1/models', { headers: { Authorization: `Bearer ${issued.key}` }, signal: AbortSignal.timeout(5000) });
  assert.equal(response.status, 200); await response.text();
  admin = adminClient(serviceProcess.origin); await admin.login();
  const models = await admin.request('/models');
  assert.ok(models.items.some(item => item.id === 'backup-model'));
  await stopServer(serviceProcess); serviceProcess = undefined;
  console.log('PASS: encrypted create/verify/tamper rejection/new-dir restore/source immutability/session invalidation/employee-key continuity');
} finally {
  await stopServer(serviceProcess);
}
