// Independent browser acceptance using a real, isolated Go process.
// Arguments: absolute executable, built web directory, installed Playwright module, output directory.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const http = require('node:http');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { randomBytes } = require('node:crypto');
const { setTimeout: delay } = require('node:timers/promises');
const [executable, web, playwrightPath, output] = process.argv.slice(2);
for (const value of [executable, web, playwrightPath, output]) assert.ok(value && path.isAbsolute(value), 'Absolute paths required');
const { chromium } = require(playwrightPath);

(async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-pool-ui-'));
  const password = randomBytes(24).toString('hex');
  const secret = 'synthetic-batch-key-' + randomBytes(24).toString('hex');
  let child, browser, logs = '', providerCalls = 0;
  const received=[];
  const mock = http.createServer(async(request, response) => {
    providerCalls++; let raw=''; for await(const part of request) raw+=part;
    const body=JSON.parse(raw); received.push(body.model);
    response.setHeader('Content-Type','application/json');
    response.end(JSON.stringify({id:'synthetic',object:'chat.completion',model:body.model,choices:[{index:0,message:{role:'assistant',content:'ok'},finish_reason:'stop'}]}));
  });
  const launch = args => {
    const proc = spawn(executable, ['--data-dir', dir, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) proc.kill(); });
    return proc;
  };
  const cleanSecrets = value => {
    const bytes = Buffer.from(value);
    assert.ok(!bytes.includes(Buffer.from(secret)) && !bytes.includes(Buffer.from(password)), 'Synthetic secret leaked');
  };
  try {
    child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n');
    assert.equal((await initialized)[0], 0, 'Initialization failed');
    mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
    const endpoint = `http://127.0.0.1:${mock.address().port}`;
    const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
    const origin = `http://127.0.0.1:${probe.address().port}`; await new Promise(resolve => probe.close(resolve));
    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
    let ready = false;
    for (let i = 0; i < 100; i++) {
      if (child.exitCode !== null) throw new Error('Service stopped during startup');
      try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { /* readiness */ }
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'Readiness timeout');
    await fs.mkdir(output, { recursive: true });
    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); const pageErrors = []; const requests = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.route('**/*', async route => {
      if (!route.request().url().startsWith(origin + '/')) return route.abort();
      return route.continue();
    });
    await page.goto(origin);
    await page.getByLabel('用户名', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();
    const session=await (await context.request.get(origin+'/admin/api/v1/session')).json();
    const api=async(method,pathname,data,status=200)=>{
      const response=await context.request.fetch(origin+'/admin/api/v1'+pathname,{method,headers:{Origin:origin,'X-CSRF-Token':session.csrf_token},data});
      assert.equal(response.status(),status,pathname); return response.json();
    };
    const first=await api('POST','/upstreams',{name:'Synthetic Primary',provider_kind:'openai-compatible',endpoint,api_key:secret},201);
    const second=await api('POST','/upstreams',{name:'Synthetic Backup',provider_kind:'openai-compatible',endpoint,api_key:secret},201);
    await api('POST','/upstreams',{name:'Other Protocol',provider_kind:'anthropic-api-key',endpoint,api_key:secret},201);
    await api('POST','/models',{id:'pool-model',upstream_id:first.id,upstream_model:'legacy-model'},201);
    let employee=await api('POST','/employees',{name:'Synthetic restricted'},201);
    employee=await api('PUT',`/employees/${employee.id}/model-policy`,{expected_revision:employee.revision,mode:'selected',models:[]});
    await page.getByRole('button',{name:'模型路由',exact:true}).click();
    await page.getByRole('heading',{name:'账号池路由已启用',exact:true}).waitFor();
    await page.getByRole('button',{name:'分组与渠道',exact:true}).click();
    await page.getByLabel('新分组名称',{exact:true}).fill('Synthetic Group');
    await page.getByRole('button',{name:'创建分组',exact:true}).click();
    await page.getByLabel('所属分组（可选）',{exact:true}).selectOption({label:'Synthetic Group'});
    await page.getByLabel('新渠道名称',{exact:true}).fill('Synthetic Channel');
    await page.getByRole('button',{name:'创建渠道',exact:true}).click();
    await page.getByRole('dialog').getByText('Synthetic Channel',{exact:true}).waitFor();
    await page.getByRole('button',{name:'关闭',exact:true}).last().click();
    await page.getByRole('button',{name:'编辑账号池',exact:true}).click();
    await page.getByText('当前为兼容默认路由',{exact:true}).waitFor();
    await page.getByRole('button',{name:'关闭',exact:true}).last().click();
    assert.equal((await api('GET','/models/pool-model/accounts')).revision,0,'Opening editor saved a pool');
    await page.getByRole('button',{name:'编辑账号池',exact:true}).click();
    await page.getByLabel('添加同服务商账号',{exact:true}).selectOption(second.id);
    assert.equal(await page.getByLabel('添加同服务商账号',{exact:true}).getByRole('option',{name:'Other Protocol',exact:true}).count(),0);
    await page.getByRole('button',{name:'添加账号',exact:true}).click();
    await page.getByLabel('账号 2 上游模型',{exact:true}).fill('backup-mapped');
    await page.getByLabel('账号 2 渠道',{exact:true}).selectOption({label:'Synthetic Group / Synthetic Channel'});
    await page.getByRole('button',{name:'保存账号池',exact:true}).click();
    await page.getByText('账号池已保存。',{exact:true}).waitFor();
    const saved=await api('GET','/models/pool-model/accounts');assert.equal(saved.revision,1);assert.equal(saved.items.length,2);
    await api('PUT','/models/pool-model/accounts',{expected_revision:1,items:saved.items});
    await page.getByLabel('账号 2 权重',{exact:true}).fill('3');
    await page.getByRole('button',{name:'保存账号池',exact:true}).click();
    await page.getByText('账号池已被其他管理员修改。当前编辑已保留，请重新加载后再修改。',{exact:true}).waitFor();
    assert.equal(await page.getByLabel('账号 2 权重',{exact:true}).inputValue(),'3');
    assert.ok(await page.getByRole('button',{name:'保存账号池',exact:true}).isDisabled());
    await page.screenshot({path:path.join(output,'pool-conflict-desktop.png'),animations:'disabled'});
    await page.getByRole('button',{name:'重新加载账号池',exact:true}).click();
    await page.getByLabel('账号 2 权重',{exact:true}).waitFor();
    await page.getByRole('button',{name:'保存账号池',exact:true}).waitFor({state:'visible'});
    await page.setViewportSize({width:390,height:844});
    await page.getByRole('button',{name:'保存账号池',exact:true}).scrollIntoViewIfNeeded();
    await page.screenshot({path:path.join(output,'pool-editor-mobile.png'),animations:'disabled'});
    await page.getByRole('button',{name:'关闭',exact:true}).last().click();
    const employees=await api('GET','/employees');assert.deepEqual(employees.items[0].models,[]);assert.equal(employees.items[0].model_mode,'selected');
    await api('PATCH',`/upstreams/${first.id}`,{expected_revision:first.revision,enabled:false});
    await page.getByRole('button',{name:'编辑账号池',exact:true}).click();
    await page.getByText('已停用，可移除',{exact:true}).waitFor();
    cleanSecrets(await page.getByRole('dialog').innerText());
    const key=await api('POST',`/employees/${employee.id}/keys`,{name:'synthetic',operation_id:require('node:crypto').randomUUID()},201);
    const denied=await fetch(origin+'/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer '+key.key,'Content-Type':'application/json'},body:JSON.stringify({model:'pool-model',messages:[{role:'user',content:'synthetic'}]})});
    assert.equal(denied.status,403);assert.equal(providerCalls,0);
    await api('PUT',`/employees/${employee.id}/model-policy`,{expected_revision:employee.revision,mode:'selected',models:['pool-model']});
    const allowed=await fetch(origin+'/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer '+key.key,'Content-Type':'application/json'},body:JSON.stringify({model:'pool-model',messages:[{role:'user',content:'synthetic'}]})});
    assert.equal(allowed.status,200);await allowed.json();assert.deepEqual(received,['backup-mapped']);
    assert.equal(await page.evaluate(()=>localStorage.length+sessionStorage.length),0);
    assert.deepEqual(pageErrors,[]);cleanSecrets(logs);
    await fs.writeFile(path.join(output,'result.json'),JSON.stringify({status:'PASS',providerCalls,pageErrors:0,poolRevision:2,permissionPreserved:true},null,2));
    console.log('PASS: real Go + browser pool groups/channels, explicit save, revision conflict/reload, disabled account, permission isolation, mapped upstream execution, desktop/mobile');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const stopped = once(child, 'exit'); child.stdin.end(); const timer = setTimeout(() => child.kill(), 5000);
      try { await stopped; } finally { clearTimeout(timer); }
    }
    mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
    const resolved = path.resolve(dir);
    assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-pool-ui-'), 'Unexpected cleanup target');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
