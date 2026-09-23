// Independent governance acceptance with disposable old/new Go processes.
// Uses synthetic loopback traffic, never the user's running instance or data.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as delay } from 'node:timers/promises';

const [executable, previousExecutable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable));
assert.ok(!previousExecutable || path.isAbsolute(previousExecutable));
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-governance-'));
const password = randomBytes(24).toString('hex'), upstreamSecret = randomBytes(24).toString('hex');
const sensitive = [password, upstreamSecret];
let activeExecutable = previousExecutable ?? executable;
let child, origin, cookie = '', csrf = '', logs = '', calls = 0, block = false, blocked = false, release;
const mock = http.createServer(async (req, res) => {
  calls++;
  if (req.headers.authorization !== 'Bearer ' + upstreamSecret) { res.writeHead(500).end(); return; }
  for await (const chunk of req) { /* no request-body persistence */ }
  if (block) {
    blocked = true;
    await new Promise(resolve => { release = resolve; res.once('close', resolve); });
    blocked = false;
  }
  if (!res.destroyed) {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end('{"id":"synthetic","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}');
  }
});
function launch(args) {
  const proc = spawn(activeExecutable, ['--data-dir', directory, ...args], { windowsHide:true, stdio:['pipe','pipe','pipe'] });
  for (const output of [proc.stdout,proc.stderr]) output.on('data',bytes=>{logs+=bytes;if(logs.length>1<<20)proc.kill();});
  return proc;
}
async function stop() {
  if (child && child.exitCode===null && child.signalCode===null) {
    const done=once(child,'exit');child.stdin.end();const timer=setTimeout(()=>child.kill('SIGKILL'),10000);
    try {assert.equal((await done)[0],0,'Graceful shutdown failed');} finally {clearTimeout(timer);}
  }
}
async function until(condition,label) {
  const end=Date.now()+10000;
  while(Date.now()<end){if(await condition())return;await delay(25);}
  throw Error('Timed out: '+label);
}
async function admin(route,method='GET',body,expected=200) {
  const response=await fetch(origin+'/admin/api/v1'+route,{method,headers:{Origin:origin,'Content-Type':'application/json',...(cookie?{Cookie:cookie}:{}),...(csrf?{'X-CSRF-Token':csrf}:{})},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(10000)});
  if(response.headers.get('set-cookie'))cookie=response.headers.get('set-cookie').split(';')[0];
  const result=await response.json();assert.equal(response.status,expected,route+' status');return result;
}
async function start() {
  const reservation=http.createServer();reservation.listen(0,'127.0.0.1');await once(reservation,'listening');origin=`http://127.0.0.1:${reservation.address().port}`;await new Promise(resolve=>reservation.close(resolve));
  child=launch(['--listen',new URL(origin).host,'--allow-loopback-upstream','--shutdown-on-stdin-eof']);
  await until(async()=>{if(child.exitCode!==null)throw Error('Service startup failed');try{return(await fetch(origin+'/healthz',{signal:AbortSignal.timeout(500)})).ok;}catch{return false;}},'startup');
  cookie='';csrf='';csrf=(await admin('/sessions','POST',{username:'admin',password})).csrf_token;
}
async function employee(key,expected,options={}) {
  const response=await fetch(origin+'/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer '+key,'Content-Type':'application/json',...options.headers},body:JSON.stringify({model:'governance-test',messages:[{role:'user',content:'Synthetic governance acceptance'}]}),signal:options.signal??AbortSignal.timeout(15000)});
  assert.equal(response.status,expected,'Employee status');await response.text();
}
const noShadow={tpm:null,cost_micro:null,currency:null,window:null};
try {
  mock.listen(0,'127.0.0.1');await once(mock,'listening');
  child=launch(['--init']);const done=once(child,'exit');child.stdin.end(password+'\n');assert.equal((await done)[0],0);
  await start();
  const account=await admin('/upstreams','POST',{name:'Synthetic governance account',provider_kind:'openai-compatible',endpoint:`http://127.0.0.1:${mock.address().port}/v1`,api_key:upstreamSecret},201);
  await admin('/models','POST',{id:'governance-test',upstream_id:account.id,upstream_model:'actual'},201);
  const person=await admin('/employees','POST',{name:'Synthetic governance employee'},201);
  const keyRecord=await admin(`/employees/${person.id}/keys`,'POST',{name:'Synthetic key',operation_id:randomUUID()},201);const key=keyRecord.key;sensitive.push(key);
  await stop();
  if(previousExecutable){const old=new DatabaseSync(path.join(directory,'cpa-cloud.db'),{readOnly:true});try{assert.equal(old.prepare("SELECT COUNT(*) AS n FROM sqlite_master WHERE name='governance_settings'").get().n,0);}finally{old.close();}}
  activeExecutable=executable;await start();
  let setting=await admin('/governance/settings');assert.equal(setting.enabled,false);
  const policyInput={operation_id:randomUUID(),scope_kind:'employee',scope_id:person.id,enabled:true,hard:{rpm:1,concurrency:1},shadow:noShadow};
  const receipt=await admin('/governance/policies','POST',policyInput);assert.equal(receipt.resource_kind,'policy');
  assert.deepEqual(await admin('/governance/policies','POST',policyInput),receipt);
  const firstPolicy=await admin('/governance/policies/'+receipt.resource_id);
  await employee(key,200);await employee(key,200); // configured policies do not enable governance
  const enable={operation_id:randomUUID(),expected_revision:setting.revision,enabled:true};
  const enabledReceipt=await admin('/governance/settings','PUT',enable);assert.equal(enabledReceipt.revision,setting.revision+1);
  assert.deepEqual(await admin('/governance/operations/'+enable.operation_id),enabledReceipt);
  const beforeInvalid=calls;
  await employee(key,400,{headers:{'X-CPA-Session':'x'.repeat(257)}});assert.equal(calls,beforeInvalid);
  block=true;const inFlight=employee(key,200);await until(()=>blocked,'blocked first generation');
  await employee(key,429);assert.equal(calls,beforeInvalid+1,'Denied admission dispatched upstream');
  block=false;release();await inFlight;
  await employee(key,429); // terminal release does not refund RPM
  const update={operation_id:randomUUID(),expected_revision:firstPolicy.revision,enabled:true,hard:{rpm:20,concurrency:1},shadow:noShadow};
  const updatedReceipt=await admin('/governance/policies/'+firstPolicy.id,'PUT',update);assert.equal(updatedReceipt.revision,2);
  assert.deepEqual(await admin('/governance/policies','POST',policyInput),receipt,'Replay must precede current CAS');
  await admin('/governance/policies/'+firstPolicy.id,'PUT',{...update,operation_id:randomUUID()},409);
  const groupWrite={operation_id:randomUUID(),name:'Synthetic governance group',employee_ids:[person.id]};
  const groupReceipt=await admin('/governance/groups','POST',groupWrite);
  const groupPolicy=await admin('/governance/policies','POST',{operation_id:randomUUID(),scope_kind:'group',scope_id:groupReceipt.resource_id,enabled:true,hard:{rpm:null,concurrency:1},shadow:noShadow});
  const shadowInput={operation_id:randomUUID(),scope_kind:'key',scope_id:keyRecord.id,enabled:true,hard:{rpm:null,concurrency:null},shadow:{tpm:1,cost_micro:'9007199254740993',currency:'USD',window:'rolling_24h'}};
  const shadowReceipt=await admin('/governance/policies','POST',shadowInput);
  const shadow=await admin('/governance/policies/'+shadowReceipt.resource_id);assert.equal(shadow.shadow.cost_micro,'9007199254740993');
  await employee(key,200); // tiny shadow TPM does not hard-block
  const cancel=new AbortController();block=true;
  const cancelled=employee(key,200,{signal:cancel.signal}).catch(error=>{assert.equal(error.name,'AbortError');});
  await until(()=>blocked,'cancelled generation entered');cancel.abort();await cancelled;block=false;release?.();
  await until(()=>!blocked,'upstream cancellation');
  setting=await admin('/governance/settings');await admin('/governance/settings','PUT',{operation_id:randomUUID(),expected_revision:setting.revision,enabled:false});
  const beforeOff=calls;await employee(key,200);assert.equal(calls,beforeOff+1);
  const denied=await fetch(origin+'/admin/api/v1/governance/settings',{headers:{Authorization:'Bearer '+key}});assert.equal(denied.status,401);
  const noCSRF=await fetch(origin+'/admin/api/v1/governance/settings',{method:'PUT',headers:{Cookie:cookie,Origin:origin,'Content-Type':'application/json'},body:JSON.stringify(enable)});assert.equal(noCSRF.status,403);
  await stop();
  const db=new DatabaseSync(path.join(directory,'cpa-cloud.db'),{readOnly:true});
  try {
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM governance_requests').get().n,3,'Off/invalid/denied requests consumed governance');
    assert.equal(db.prepare("SELECT COUNT(*) AS n FROM governance_requests WHERE status='succeeded'").get().n,2);
    assert.equal(db.prepare("SELECT COUNT(*) AS n FROM governance_requests WHERE status='cancelled'").get().n,1);
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM governance_requests WHERE released_at IS NULL').get().n,0);
    assert.equal(db.prepare("SELECT COUNT(*) AS n FROM governance_request_scopes WHERE scope_kind='group' AND group_revision=1").get().n,2);
    assert.equal(db.prepare('SELECT COUNT(*) AS n FROM governance_management_operations').get().n,db.prepare('SELECT COUNT(*) AS n FROM governance_management_audit').get().n);
  } finally {db.close();}
  await start();assert.equal((await admin('/governance/settings')).enabled,false);
  assert.equal((await admin('/governance/groups/'+groupReceipt.resource_id)).revision,1);
  assert.equal((await admin('/governance/policies/'+groupPolicy.resource_id)).enabled,true);
  await employee(key,200);await admin(`/keys/${keyRecord.id}/revoke`,'POST',{});await employee(key,401);
  await stop();
  for(const name of await readdir(directory)){const bytes=await readFile(path.join(directory,name));for(const secret of sensitive)assert.ok(!bytes.includes(Buffer.from(secret)),'Plaintext credential persisted');}
  for(const secret of sensitive)assert.ok(!logs.includes(secret),'Secret logged');
  console.log('PASS governance: upgrade/default-off/RPM/concurrency/invalid-session/groups/shadow/CAS/receipts/cancel/restart/revocation');
} finally {
  block=false;release?.();await stop();await new Promise(resolve=>mock.close(resolve));
  const resolved=path.resolve(directory);assert.equal(path.dirname(resolved),path.resolve(os.tmpdir()));assert.ok(path.basename(resolved).startsWith('cpac-governance-'));
  await rm(resolved,{recursive:true,force:true});
}
