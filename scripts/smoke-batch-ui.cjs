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
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-batch-ui-'));
  const password = randomBytes(24).toString('hex');
  const secret = 'synthetic-batch-key-' + randomBytes(24).toString('hex');
  let child, browser, logs = '', providerCalls = 0;
  const mock = http.createServer((_, response) => { providerCalls++; response.writeHead(500); response.end(); });
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
    let loseNextResult = false;
    await page.route('**/*', async route => {
      const request = route.request();
      if (!request.url().startsWith(origin + '/')) return route.abort();
      if (request.url().endsWith('/upstreams/batch-import') && request.method() === 'POST') {
        requests.push(request.postDataJSON());
        if (loseNextResult) { loseNextResult = false; const response = await route.fetch(); assert.equal(response.status(), 200); await response.dispose(); return route.abort('failed'); }
      }
      return route.continue();
    });
    await page.goto(origin);
    await page.getByLabel('用户名', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();
    await page.getByRole('button', { name: '上游连接', exact: true }).click();
    await page.getByRole('button', { name: '批量导入', exact: true }).click();
    const payload = { items: [
      { item_id: 'good', name: 'Synthetic OpenAI', provider_kind: 'openai-compatible', endpoint, api_key: secret },
      { item_id: 'repair', name: 'Synthetic Claude', provider_kind: 'anthropic-api-key', endpoint: 'http://192.0.2.1', api_key: secret },
    ] };
    await page.getByLabel('选择 JSON 文件', { exact: true }).setInputFiles({ name: 'synthetic-batch.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(payload)) });
    await page.getByRole('button', { name: '检查并预览', exact: true }).click();
    cleanSecrets(await page.getByRole('dialog').innerText());
    await page.getByRole('button', { name: '确认批量导入', exact: true }).click();
    await page.getByRole('button', { name: '修复失败项', exact: true }).waitFor();
    assert.equal(await page.getByRole('dialog').getByText('已创建', { exact: true }).count(), 1);
    assert.equal(await page.getByRole('dialog').getByText('失败', { exact: true }).count(), 1);
    await page.getByRole('button', { name: '修复失败项', exact: true }).click();
    const editor = page.getByLabel('或粘贴 JSON', { exact: true });
    const repair = JSON.parse(await editor.inputValue());
    assert.deepEqual(repair.items.map(item => item.item_id), ['repair']);
    repair.items[0].endpoint = endpoint;
    await editor.fill(JSON.stringify(repair));
    await page.getByRole('button', { name: '检查并预览', exact: true }).click();
    await page.getByRole('button', { name: '确认批量导入', exact: true }).click();
    await page.getByRole('button', { name: '新建批次', exact: true }).waitFor();
    assert.equal(requests[0].operation_id, requests[1].operation_id);
    assert.deepEqual(requests[1].items.map(item => item.item_id), ['repair']);
    await page.getByRole('button', { name: '新建批次', exact: true }).click();
    await editor.fill(JSON.stringify({ items: [{ ...payload.items[0], item_id: 'lost', name: 'Synthetic Retry' }] }));
    await page.getByRole('button', { name: '检查并预览', exact: true }).click();
    loseNextResult = true;
    await page.getByRole('button', { name: '确认批量导入', exact: true }).click();
    await page.getByRole('button', { name: '使用同一操作编号重试', exact: true }).waitFor();
    await page.screenshot({ path: path.join(output, 'batch-retry-desktop.png'), animations: 'disabled' });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: '使用同一操作编号重试', exact: true }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(output, 'batch-retry-mobile.png'), animations: 'disabled' });
    await page.getByRole('button', { name: '使用同一操作编号重试', exact: true }).click();
    await page.getByRole('button', { name: '新建批次', exact: true }).waitFor();
    assert.deepEqual(requests[2], requests[3], 'Uncertain retry changed content or operation ID');
    assert.equal(await page.getByRole('dialog').getByText('已存在', { exact: true }).count(), 1);
    cleanSecrets(await page.getByRole('dialog').innerText());
    const upstreamsResponse = await context.request.get(origin + '/admin/api/v1/upstreams');
    const upstreamsRaw = await upstreamsResponse.text(); cleanSecrets(upstreamsRaw);
    assert.equal(JSON.parse(upstreamsRaw).items.length, 3, 'Duplicate upstream created');
    const models = await (await context.request.get(origin + '/admin/api/v1/models')).json();
    assert.equal(models.items.length, 0, 'Import widened employee permissions');
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0, 'Credentials persisted in browser storage');
    assert.deepEqual(pageErrors, []); assert.equal(providerCalls, 0);
    cleanSecrets(logs);
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify({ status: 'PASS', created: 3, batchCalls: 4, providerCalls, pageErrors: 0, screenshots: ['batch-retry-desktop.png', 'batch-retry-mobile.png'] }, null, 2));
    console.log('PASS: real Go + browser batch upload, mixed result, failed-only repair, response-loss retry, no duplicates, desktop/mobile, no provider calls');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const stopped = once(child, 'exit'); child.stdin.end(); const timer = setTimeout(() => child.kill(), 5000);
      try { await stopped; } finally { clearTimeout(timer); }
    }
    mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
    const resolved = path.resolve(dir);
    assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-batch-ui-'), 'Unexpected cleanup target');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
