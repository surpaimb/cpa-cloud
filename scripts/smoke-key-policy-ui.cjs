// Real isolated CPA Cloud process + browser acceptance for KEY-02 administration.
// Usage: node scripts/smoke-key-policy-ui.cjs <exe> <web/dist> <playwright module> <output dir>
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
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-key-policy-ui-'));
  const password = randomBytes(24).toString('hex');
  const upstreamSecret = randomBytes(24).toString('hex');
  let child, browser, logs = '';
  const mock = http.createServer((request, response) => { response.writeHead(500).end(); });
  const launch = args => {
    const process = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    for (const stream of [process.stdout, process.stderr]) stream.on('data', chunk => { logs += chunk; if (logs.length > 1 << 20) process.kill(); });
    return process;
  };
  try {
    child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n'); assert.equal((await initialized)[0], 0);
    mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
    const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
    const origin = `http://127.0.0.1:${probe.address().port}`; await new Promise(resolve => probe.close(resolve));
    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt++) { try { if ((await fetch(origin + '/healthz')).ok) { ready = true; break; } } catch {} await delay(100); }
    assert.ok(ready, 'Readiness timeout');
    await fs.mkdir(output, { recursive: true });
    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage(); const pageErrors = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.route('**/*', route => route.request().url().startsWith(origin + '/') ? route.continue() : route.abort());
    await page.goto(origin);
    await page.getByLabel('用户名', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();
    const session = await (await context.request.get(origin + '/admin/api/v1/session')).json();
    const api = async (method, route, data, expected = 200) => {
      const response = await context.request.fetch(origin + '/admin/api/v1' + route, { method, headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token }, data });
      assert.equal(response.status(), expected, route); return response.json();
    };
    const upstream = await api('POST', '/upstreams', { name: 'Synthetic UI', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}`, api_key: upstreamSecret }, 201);
    await api('POST', '/models', { id: 'ui-policy-model', upstream_id: upstream.id, upstream_model: 'actual-ui-model' }, 201);
    const employee = await api('POST', '/employees', { name: 'Browser policy employee' }, 201);
    await page.reload();
    await page.getByRole('button', { name: '管理 Key', exact: true }).click();
    const createDialog = page.getByRole('dialog');
    await createDialog.getByLabel('仅指定协议', { exact: true }).click();
    await createDialog.getByLabel('OpenAI Chat Completions', { exact: true }).click();
    await createDialog.getByLabel('仅指定模型', { exact: true }).click();
    await createDialog.getByLabel('ui-policy-model', { exact: true }).click();
    await createDialog.getByRole('button', { name: '生成永久 Key', exact: true }).click();
    const plaintext = await page.getByTestId('created-key').textContent(); assert.ok(plaintext && plaintext.startsWith('cpac_'));
    await page.screenshot({ path: path.join(output, 'key-policy-create-desktop.png'), animations: 'disabled' });
    await page.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
    assert.equal(await page.getByText(plaintext, { exact: true }).count(), 0, 'Plaintext key remained visible');

    await page.getByRole('button', { name: '管理 Key', exact: true }).click();
    await page.getByRole('button', { name: '编辑独立权限', exact: true }).click();
    const dialogs = page.getByRole('dialog'); const editor = dialogs.last();
    await editor.getByLabel('OpenAI Chat Completions', { exact: true }).uncheck();
    const keys = await api('GET', `/employees/${employee.id}/keys`); const key = keys.items[0];
    await api('PUT', `/keys/${key.id}/policy`, { expected_revision: 1, protocol_mode: 'all', protocols: [], model_mode: 'all', models: [] });
    await editor.getByRole('button', { name: '保存独立权限', exact: true }).click();
    await editor.getByText(/策略已被其他操作更新到修订 2/).waitFor();
    assert.equal(await editor.getByLabel('OpenAI Chat Completions', { exact: true }).isChecked(), false, 'Draft was lost after CAS conflict');
    await page.screenshot({ path: path.join(output, 'key-policy-conflict-desktop.png'), animations: 'disabled' });
    await editor.getByRole('button', { name: '使用最新修订重试', exact: true }).click();
    await page.getByText(/修订 3/).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'key-policy-mobile.png'), animations: 'disabled' });
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
    assert.deepEqual(pageErrors, []);
    assert.ok(!logs.includes(password) && !logs.includes(upstreamSecret) && !logs.includes(plaintext), 'Secret leaked to service logs');
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify({ status: 'PASS', revision: 3, draftPreserved: true, plaintextHidden: true, pageErrors: 0 }, null, 2));
    console.log('PASS: real Go + browser key creation policy, one-time plaintext, CAS conflict draft preservation, retry and mobile layout');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) { const stopped = once(child, 'exit'); child.stdin.end(); const timer = setTimeout(() => child.kill(), 5000); try { await stopped; } finally { clearTimeout(timer); } }
    mock.closeAllConnections(); if (mock.listening) await new Promise(resolve => mock.close(resolve));
    const resolved = path.resolve(directory); assert.equal(path.dirname(resolved), path.resolve(os.tmpdir())); assert.ok(path.basename(resolved).startsWith('cpac-key-policy-ui-'));
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
