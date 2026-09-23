// Real-process acceptance with synthetic credentials and loopback endpoints only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomUUID, randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-batch-'));
const password = randomBytes(24).toString('hex');
const key = 'synthetic-' + randomUUID(), account = 'synthetic-account-' + randomUUID();
const encode = object => Buffer.from(JSON.stringify(object)).toString('base64url');
const access = `${encode({alg:'RS256',typ:'JWT'})}.${encode({exp:Math.floor(Date.now()/1000)+3600,sub:account})}.synthetic-signature`;
const refresh = 'synthetic-refresh-' + randomUUID();
const auth = JSON.stringify({auth_mode:'chatgpt',tokens:{access_token:access,refresh_token:refresh,account_id:account}});
const secrets = [password,key,account,access,refresh];
let child, origin, cookie = '', csrf = '', logs = '', upstreamCalls = 0;
const mock = http.createServer((_, response) => { upstreamCalls++; response.writeHead(500); response.end(); });
function noSecrets(value) { const bytes=Buffer.isBuffer(value)?value:Buffer.from(value); assert.ok(!secrets.some(secret=>bytes.includes(Buffer.from(secret))), 'Synthetic credential leaked'); }
function launch(args) {
  const proc=spawn(executable,['--data-dir',directory,...args],{windowsHide:true,stdio:['pipe','pipe','pipe']});
  for (const stream of [proc.stdout,proc.stderr]) stream.on('data',chunk=>{logs+=chunk; if(logs.length>1<<20) proc.kill();});
  return proc;
}
async function stop() {
  if(child && child.exitCode===null && child.signalCode===null) {
    const ended=once(child,'exit'); child.stdin.end(); const timer=setTimeout(()=>child.kill(),5000);
    try { await ended; } finally { clearTimeout(timer); }
  }
}
async function start(enabled=true) {
  const probe=http.createServer(); probe.listen(0,'127.0.0.1'); await once(probe,'listening');
  origin=`http://127.0.0.1:${probe.address().port}`; await new Promise(resolve=>probe.close(resolve));
  child=launch(['--listen',new URL(origin).host,'--allow-loopback-upstream','--shutdown-on-stdin-eof',...(enabled?['--experimental-codex-membership']:[])]);
  for(let i=0;i<100;i++) {
    if(child.exitCode!==null) throw new Error('Service startup failed');
    try { if((await fetch(origin+'/healthz',{signal:AbortSignal.timeout(500)})).ok) { cookie=''; csrf=(await admin('/sessions','POST',{username:'admin',password})).csrf_token; return; } } catch { /* Readiness only. */ }
    await delay(100);
  }
  throw new Error('Readiness timeout');
}
async function admin(route,method='GET',body,status=200,useCSRF=true) {
  const response=await fetch(origin+'/admin/api/v1'+route,{method,headers:{Cookie:cookie,Origin:origin,'Content-Type':'application/json',...(useCSRF?{'X-CSRF-Token':csrf}:{})},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(10000)});
  assert.equal(response.status,status,method+' '+route);
  if(response.headers.get('set-cookie')) cookie=response.headers.get('set-cookie').split(';')[0];
  const raw=await response.text(); noSecrets(raw); return raw?JSON.parse(raw):{};
}
try {
  mock.listen(0,'127.0.0.1'); await once(mock,'listening');
  const endpoint=`http://127.0.0.1:${mock.address().port}`;
  child=launch(['--init']); const initialized=once(child,'exit'); child.stdin.end(password+'\n'); assert.equal((await initialized)[0],0);
  await start();
  const route='/upstreams/batch-import';
  const input={operation_id:randomUUID(),items:[
    {item_id:'openai',name:'Test OpenAI',provider_kind:'openai-compatible',endpoint,api_key:key},
    {item_id:'claude',name:'Test Claude',provider_kind:'anthropic-api-key',endpoint,api_key:key},
    {item_id:'gemini',name:'Test Gemini',provider_kind:'gemini-api-key',endpoint,api_key:key},
    {item_id:'codex',name:'Test Codex',provider_kind:'codex-membership',auth_json:auth},
    {item_id:'invalid',name:'Invalid',provider_kind:'unknown',api_key:key},
  ]};
  await admin(route,'POST',input,403,false);
  await admin(route,'POST',{operation_id:randomUUID(),items:[input.items[0],input.items[0]]},400);
  assert.equal((await admin('/upstreams')).items.length,0);
  const created=(await admin(route,'POST',input)).items;
  assert.deepEqual(created.map(item=>item.status),['created','created','created','created','failed']);
  const repeated=(await admin(route,'POST',input)).items;
  assert.deepEqual(repeated.map(item=>item.status),['existing','existing','existing','existing','failed']);
  assert.deepEqual(repeated.map(item=>item.upstream_id),created.map(item=>item.upstream_id));
  const conflicting=structuredClone(input); conflicting.items[0].name='Changed';
  assert.equal((await admin(route,'POST',conflicting,409)).error.code,'operation_conflict');
  assert.equal((await admin('/upstreams')).items.length,4);
  assert.equal((await admin('/models')).items.length,0,'Batch widened employee access');
  const codex=(await admin('/upstreams')).items.find(item=>item.id===created[3].upstream_id);
  assert.equal(codex.credential_state,'imported_unverified');
  await stop(); await start();
  assert.deepEqual((await admin(route,'POST',input)).items,repeated,'Restart lost receipts');
  await stop(); await start(false);
  const blocked=await admin(route,'POST',{operation_id:randomUUID(),items:[input.items[3]]});
  assert.equal(blocked.items[0].error_code,'feature_disabled');
  assert.equal((await admin('/upstreams')).items.length,4);
  assert.equal(upstreamCalls,0,'Import contacted a model provider');
  await stop(); noSecrets(logs);
  for(const entry of await readdir(directory,{withFileTypes:true})) if(entry.isFile()) noSecrets(await readFile(path.join(directory,entry.name)));
  console.log('PASS: real-process batch import, four providers, per-item failures, CSRF, zero-write validation, idempotency/conflict, restart, feature gate, encrypted persistence, and no provider calls');
} finally {
  await stop(); mock.closeAllConnections(); if(mock.listening) await new Promise(resolve=>mock.close(resolve));
  const resolved=path.resolve(directory);
  if(path.dirname(resolved)!==path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith('cpac-batch-')) throw new Error('Unexpected cleanup path');
  await rm(resolved,{recursive:true,force:true});
}
