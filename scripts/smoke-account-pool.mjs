// CPA Cloud account-pool process acceptance. Synthetic loopback upstreams only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const [executable] = process.argv.slice(2);
assert.ok(executable && path.isAbsolute(executable), 'Absolute executable required');
const directory = await mkdtemp(path.join(os.tmpdir(), 'cpac-pool-'));
const password = randomBytes(24).toString('hex');
const prompt = 'synthetic-prompt-' + randomUUID(), session = 'synthetic-session-' + randomUUID();
const secrets = [password, prompt, session];
let child, origin, cookie='', csrf='', logs='', rejectPrimary=false, blockNext=false, unblock, entered;
const received=[], mockErrors=[], credentials=new Map();
const mock=http.createServer(async(req,res)=>{
  try {
    let raw=''; for await (const chunk of req) raw+=chunk;
    const input=JSON.parse(raw);
    const key=req.headers['x-goog-api-key'] ?? req.headers.authorization?.replace('Bearer ','');
    const account=credentials.get(key); assert.ok(account,'Unexpected upstream credential');
    for(const name of ['cookie','origin','x-csrf-token','x-cpa-session']) assert.equal(req.headers[name],undefined,'Employee metadata escaped upstream');
    received.push({account,model:input.model,url:req.url});
    if(blockNext){blockNext=false; const enteredNow=entered; await new Promise(resolve=>{unblock=resolve;enteredNow();});}
    if(rejectPrimary && account.endsWith('-a')){res.writeHead(429,{'Content-Type':'application/json'});res.end('{"error":"synthetic-private-error"}');return;}
    res.setHeader('Content-Type','application/json');
    if(req.url.endsWith('/responses'))res.end(JSON.stringify({id:'synthetic-response',object:'response',status:'completed',output:[{type:'message',role:'assistant',content:[{type:'output_text',text:'ok'}]}]}));
    else if(req.url.endsWith('/messages/count_tokens'))res.end('{"input_tokens":3}');
    else if(req.url.endsWith('/messages'))res.end(JSON.stringify({id:'synthetic-message',type:'message',role:'assistant',model:input.model,content:[{type:'text',text:'ok'}],stop_reason:'end_turn'}));
    else if(req.url.startsWith('/v1beta/'))res.end('{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}');
    else res.end(JSON.stringify({id:'synthetic-chat',object:'chat.completion',model:input.model,choices:[{index:0,message:{role:'assistant',content:'ok'},finish_reason:'stop'}]}));
  }catch(error){mockErrors.push(error.message);if(!res.headersSent)res.writeHead(500);res.end();}
});
function launch(args){const proc=spawn(executable,['--data-dir',directory,...args],{windowsHide:true,stdio:['pipe','pipe','pipe']});for(const stream of [proc.stdout,proc.stderr])stream.on('data',b=>{logs+=b;if(logs.length>1<<20)proc.kill();});return proc;}
async function stop(){if(child&&child.exitCode===null&&child.signalCode===null){const exit=once(child,'exit');child.stdin.end();const timer=setTimeout(()=>child.kill(),5000);try{await exit;}finally{clearTimeout(timer);}}}
async function start(){const probe=http.createServer();probe.listen(0,'127.0.0.1');await once(probe,'listening');origin=`http://127.0.0.1:${probe.address().port}`;await new Promise(resolve=>probe.close(resolve));child=launch(['--listen',new URL(origin).host,'--allow-loopback-upstream','--shutdown-on-stdin-eof']);for(let i=0;i<100;i++){if(child.exitCode!==null)throw Error('Service startup failed');try{if((await fetch(origin+'/healthz',{signal:AbortSignal.timeout(500)})).ok){cookie='';csrf=(await admin('/sessions','POST',{username:'admin',password})).csrf_token;return;}}catch{}await delay(100);}throw Error('Readiness timeout');}
async function admin(route,method='GET',body,status=200){const response=await fetch(origin+'/admin/api/v1'+route,{method,headers:{Cookie:cookie,Origin:origin,'X-CSRF-Token':csrf,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(10000)});if(response.headers.get('set-cookie'))cookie=response.headers.get('set-cookie').split(';')[0];const raw=await response.text();assert.equal(response.status,status,method+' '+route+' '+raw);return JSON.parse(raw);}
async function request(protocol,key,extra={}){const model='pool-'+protocol;const native=protocol==='gemini';const url=native?`/v1beta/models/${model}:generateContent`:protocol==='claude'?'/v1/messages':protocol==='responses'?'/v1/responses':'/v1/chat/completions';const body=native?{contents:[{role:'user',parts:[{text:prompt}]}]}:protocol==='responses'?{model,input:prompt}:{model,messages:[{role:'user',content:prompt}],...(protocol==='claude'?{max_tokens:16}:{})};return fetch(origin+url,{method:'POST',headers:{Authorization:'Bearer '+key,'Content-Type':'application/json',...(protocol==='claude'?{'Anthropic-Version':'2023-06-01'}:{}),...(extra.headers??{})},body:JSON.stringify(body),signal:extra.signal??AbortSignal.timeout(10000)});}
function noSecrets(value){const bytes=Buffer.from(value);assert.ok(!secrets.some(secret=>bytes.includes(Buffer.from(secret))),'Sensitive content persisted');}
try{
  child=launch(['--init']);const initialized=once(child,'exit');child.stdin.end(password+'\n');assert.equal((await initialized)[0],0);
  mock.listen(0,'127.0.0.1');await once(mock,'listening');const endpoint=`http://127.0.0.1:${mock.address().port}`;
  await start();assert.equal((await admin('/system/status')).features.account_pool_routing,true);
  const employee=await admin('/employees','POST',{name:'Synthetic employee'},201);
  const issued=await admin(`/employees/${employee.id}/keys`,'POST',{name:'synthetic',operation_id:randomUUID()},201);secrets.push(issued.key);
  const pools={};
  for(const [protocol,provider_kind] of [['chat','openai-compatible'],['responses','openai-compatible'],['claude','anthropic-api-key'],['gemini','gemini-api-key']]){
    const accounts=[];
    for(const suffix of ['a','b']){const name=protocol+'-'+suffix,api_key='synthetic-key-'+randomUUID();secrets.push(api_key);credentials.set(api_key,name);accounts.push(await admin('/upstreams','POST',{name,provider_kind,endpoint,api_key},201));}
    await admin('/models','POST',{id:'pool-'+protocol,upstream_id:accounts[0].id,upstream_model:'original-'+protocol},201);
    const items=accounts.map((account,i)=>({upstream_id:account.id,upstream_model:'mapped-'+protocol,priority:i?1:10,weight:1,max_concurrency:1}));
    await admin('/models/pool-'+protocol+'/accounts','PUT',{expected_revision:0,items});
    await admin('/upstreams/'+accounts[0].id,'PATCH',{expected_revision:1,enabled:false});pools[protocol]={accounts,items};
    const response=await request(protocol,issued.key);assert.equal(response.status,200,protocol+' route failed');await response.text();
    assert.equal(received.at(-1).account,protocol+'-b','Disabled default blocked alternative route');
    if(protocol==='gemini')assert.ok(received.at(-1).url.includes('/mapped-gemini:'));else assert.equal(received.at(-1).model,'mapped-'+protocol);
  }
  const models=await (await fetch(origin+'/v1/models',{headers:{Authorization:'Bearer '+issued.key}})).json();
  assert.equal(models.data.length,4,'Model directory ignored pool alternatives');
  const geminiModels=await (await fetch(origin+'/v1beta/models',{headers:{Authorization:'Bearer '+issued.key}})).json();assert.equal(geminiModels.models.length,1);
  // Two accounts available: one request attempt only on 429, then another
  // independent request may choose an available account after persistent cooldown.
  await admin('/upstreams/'+pools.chat.accounts[0].id,'PATCH',{expected_revision:2,enabled:true});
  rejectPrimary=true;const before=received.length;
  const limited=await request('chat',issued.key);assert.equal(limited.status,502);await limited.text();assert.equal(received.length,before+1,'Unsafe automatic failover');
  const recovered=await request('chat',issued.key);assert.equal(recovered.status,200);await recovered.text();assert.equal(received.at(-1).account,'chat-b');
  await stop();await start();const restarted=await request('chat',issued.key);assert.equal(restarted.status,200);await restarted.text();assert.equal(received.at(-1).account,'chat-b','Cooldown lost at restart');
  // A queued request must re-check key revocation before touching the provider.
  blockNext=true;const firstEntered=new Promise(resolve=>{entered=resolve;});
  const active=request('chat',issued.key,{headers:{'X-CPA-Session':session}});
  await Promise.race([firstEntered,active.then(()=>{throw Error('Capacity fixture did not reach the provider');})]);
  const beforeQueued=received.length;const queued=request('chat',issued.key);await delay(100);
  await admin('/keys/'+issued.id+'/revoke','POST',{});unblock();
  const activeResult=await active;await activeResult.text();const denied=await queued;assert.equal(denied.status,401);await denied.text();assert.equal(received.length,beforeQueued,'Revoked queued request reached provider');
  assert.deepEqual(mockErrors,[]);await stop();noSecrets(logs);
  for(const entry of await readdir(directory,{withFileTypes:true}))if(entry.isFile())noSecrets(await readFile(path.join(directory,entry.name)));
  console.log('PASS: four-protocol pool execution, disabled-primary alternative, directories, mapped models, no unsafe retry, cooldown restart, queued-key revocation, metadata isolation');
}finally{
  unblock?.();await stop();mock.closeAllConnections();if(mock.listening)await new Promise(resolve=>mock.close(resolve));
  const resolved=path.resolve(directory);assert.ok(path.dirname(resolved)===path.resolve(os.tmpdir())&&path.basename(resolved).startsWith('cpac-pool-'));await rm(resolved,{recursive:true,force:true});
}
