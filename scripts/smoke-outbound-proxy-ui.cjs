// Real isolated Go process + browser acceptance for outbound proxy administration.
// Usage: node smoke-outbound-proxy-ui.cjs <exe> <web/dist> <playwright module> <output dir>
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
  const dataDirectory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-egress-ui-'));
  const password = randomBytes(24).toString('hex');
  const upstreamSecret = `synthetic-upstream-${randomBytes(24).toString('hex')}`;
  const proxySecret = `synthetic-proxy-${randomBytes(24).toString('hex')}`;
  const replacementSecret = `synthetic-replacement-${randomBytes(24).toString('hex')}`;
  const secrets = [password, upstreamSecret, proxySecret, replacementSecret];
  let child, browser, logs = '';
  let targetCalls = 0;
  const target = http.createServer((_, response) => { targetCalls += 1; response.writeHead(500); response.end(); });
  const launch = args => {
    const process = spawn(executable, ['--data-dir', dataDirectory, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    for (const stream of [process.stdout, process.stderr]) stream.on('data', chunk => {
      logs += chunk;
      if (logs.length > 1 << 20) process.kill();
    });
    return process;
  };
  const assertRedacted = value => {
    const bytes = Buffer.from(String(value));
    for (const secret of secrets) assert.ok(!bytes.includes(Buffer.from(secret)), 'Synthetic secret leaked');
  };
  try {
    child = launch(['--init']);
    const initialized = once(child, 'exit');
    child.stdin.end(password + '\n');
    assert.equal((await initialized)[0], 0, 'Initialization failed');

    target.listen(0, '127.0.0.1');
    await once(target, 'listening');
    const targetPort = target.address().port;
    const reservation = http.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const origin = `http://127.0.0.1:${reservation.address().port}`;
    await new Promise(resolve => reservation.close(resolve));

    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt += 1) {
      if (child.exitCode !== null) throw new Error('Service stopped during startup');
      try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { /* readiness */ }
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'Readiness timeout');
    await fs.mkdir(output, { recursive: true });

    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const pageErrors = [], consoleErrors = [], writeRequests = [], visited = [];
    let loseCreateResponse = true;
    let losePatchResponse = true;
    page.on('pageerror', error => pageErrors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()); });
    await page.route('**/*', async route => {
      const request = route.request();
      const url = request.url();
      if (!url.startsWith(origin + '/')) return route.abort();
      visited.push(`${request.method()} ${new URL(url).pathname}`);
      const pathname = new URL(url).pathname;
      if (pathname === '/admin/api/v1/outbound-proxies' && request.method() === 'POST') {
        writeRequests.push({ kind: 'create', body: request.postDataJSON() });
        if (loseCreateResponse) {
          loseCreateResponse = false;
          const response = await route.fetch();
          assert.ok([200, 201].includes(response.status()), `Lost create did not commit: HTTP ${response.status()}`);
          await response.dispose();
          return route.abort('failed');
        }
      }
      if (/\/admin\/api\/v1\/outbound-proxies\/[^/]+$/.test(pathname) && request.method() === 'PATCH') {
        writeRequests.push({ kind: 'patch', body: request.postDataJSON() });
        if (losePatchResponse) {
          losePatchResponse = false;
          const response = await route.fetch();
          assert.equal(response.status(), 200, 'Lost patch did not commit');
          await response.dispose();
          return route.abort('failed');
        }
      }
      if (/\/admin\/api\/v1\/upstreams\/[^/]+\/proxy$/.test(pathname) && request.method() === 'PUT') writeRequests.push({ kind: 'binding', body: request.postDataJSON() });
      return route.continue();
    });

    await page.goto(origin);
    await page.getByLabel('用户名', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();
    const session = await (await context.request.get(origin + '/admin/api/v1/session')).json();
    const api = async (method, pathname, data, expected = 200) => {
      const response = await context.request.fetch(origin + '/admin/api/v1' + pathname, { method, headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token }, data });
      const raw = await response.text();
      assertRedacted(raw);
      assert.equal(response.status(), expected, `${method} ${pathname}: ${response.status()} ${raw}`);
      return raw ? JSON.parse(raw) : {};
    };
    const upstream = await api('POST', '/upstreams', { name: 'Synthetic HTTPS Account', provider_kind: 'openai-compatible', endpoint: `https://127.0.0.1:${targetPort}`, api_key: upstreamSecret }, 201);

    await page.getByRole('button', { name: '出站代理', exact: true }).click();
    await page.getByRole('heading', { name: '出站代理', exact: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: /测试|握手/ }).count(), 0, 'Unimplemented handshake action is visible');
    await page.getByRole('button', { name: '添加代理', exact: true }).first().click();
    const createDialog = page.getByRole('dialog');
    await createDialog.getByLabel('显示名称', { exact: true }).fill('Synthetic Proxy');
    await createDialog.getByLabel('代理主机', { exact: true }).fill('127.0.0.1');
    await createDialog.getByLabel('端口', { exact: true }).fill(String(targetPort));
    await createDialog.getByLabel('地址范围', { exact: true }).selectOption('private');
    await createDialog.getByLabel('用户名', { exact: true }).fill('synthetic-user');
    await createDialog.getByLabel('密码', { exact: true }).fill(proxySecret);
    await createDialog.getByRole('button', { name: '保存代理', exact: true }).click();
    await createDialog.getByText('创建结果未知', { exact: true }).waitFor();
    assert.ok(await createDialog.getByLabel('密码', { exact: true }).isDisabled(), 'Unknown create did not freeze secret-bearing payload');
    await createDialog.getByRole('button', { name: '重试原创建操作', exact: true }).click();
    await createDialog.waitFor({ state: 'detached' });
    const creates = writeRequests.filter(item => item.kind === 'create');
    assert.equal(creates.length, 2);
    assert.deepEqual(creates[1].body, creates[0].body, 'Create retry changed UUID or payload');

    let listing = await api('GET', '/outbound-proxies?limit=50');
    assert.equal(listing.items.length, 1, 'Idempotent create made the wrong number of proxies');
    const proxyID = listing.items[0].id;
    assert.equal(listing.items[0].has_credentials, true);
    await page.getByRole('button', { name: '编辑 / 启停', exact: true }).click();
    const editDialog = page.getByRole('dialog');
    await editDialog.getByLabel('显示名称', { exact: true }).fill('Synthetic Proxy Edited');
    await editDialog.getByLabel('代理认证', { exact: true }).selectOption('replace');
    await editDialog.getByLabel('用户名', { exact: true }).fill('replacement-user');
    await editDialog.getByLabel('密码', { exact: true }).fill(replacementSecret);
    await editDialog.getByRole('checkbox').uncheck();
    await editDialog.getByRole('button', { name: '保存变更', exact: true }).click();
    await editDialog.getByText('已读取实际状态，请核对', { exact: true }).waitFor();
    assert.equal(await editDialog.getByLabel('密码', { exact: true }).count(), 0, 'Replacement password remained after uncertain PATCH');
    let actual = await api('GET', `/outbound-proxies/${proxyID}`);
    assert.equal(actual.name, 'Synthetic Proxy Edited');
    assert.equal(actual.enabled, false);
    assert.equal(actual.has_credentials, true);
    assert.equal(writeRequests.filter(item => item.kind === 'patch').length, 1, 'Unknown PATCH was automatically replayed');
    await editDialog.getByRole('button', { name: '按最新状态重新编辑', exact: true }).click();
    await editDialog.getByLabel('代理认证', { exact: true }).selectOption('clear');
    await editDialog.getByRole('checkbox').check();
    await editDialog.getByRole('button', { name: '保存变更', exact: true }).click();
    await editDialog.waitFor({ state: 'detached' });
    actual = await api('GET', `/outbound-proxies/${proxyID}`);
    assert.equal(actual.enabled, true);
    assert.equal(actual.has_credentials, false);

    await page.getByRole('button', { name: '绑定代理', exact: true }).click();
    const bindingDialog = page.getByRole('dialog');
    await bindingDialog.getByLabel('选择已启用代理', { exact: true }).selectOption(proxyID);
    await bindingDialog.getByRole('button', { name: '绑定代理', exact: true }).click();
    await bindingDialog.waitFor({ state: 'detached' });
    let binding = await api('GET', `/upstreams/${upstream.id}/proxy`);
    assert.equal(binding.binding.proxy_id, proxyID);

    await page.getByRole('button', { name: '编辑 / 启停', exact: true }).click();
    await page.getByRole('dialog').getByRole('checkbox').uncheck();
    await page.getByRole('dialog').getByRole('button', { name: '保存变更', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    binding = await api('GET', `/upstreams/${upstream.id}/proxy`);
    assert.equal(binding.binding.proxy_id, proxyID, 'Disabling proxy silently unbound account');
    assert.equal(binding.binding.enabled, false);
    await page.getByText('代理已停用，绑定仍保留', { exact: true }).waitFor();

    await page.screenshot({ path: path.join(output, 'outbound-proxy-desktop.png'), animations: 'disabled', fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole('button', { name: '修改 / 解绑', exact: true }).scrollIntoViewIfNeeded();
    await page.getByRole('button', { name: '修改 / 解绑', exact: true }).click();
    await page.getByRole('dialog').waitFor();
    await page.screenshot({ path: path.join(output, 'outbound-proxy-mobile.png'), animations: 'disabled' });
    await page.getByRole('dialog').getByRole('button', { name: '明确解绑为直连', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    binding = await api('GET', `/upstreams/${upstream.id}/proxy`);
    assert.equal(binding.binding, null, 'Explicit unbind did not restore direct connection');

    listing = await api('GET', '/outbound-proxies?limit=50');
    assert.equal(listing.items.length, 1);
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0, 'Proxy secret entered browser storage');
    assert.equal(targetCalls, 0, 'Smoke unexpectedly sent a model or probe request');
    assert.equal(visited.some(item => /cooldown|recovery/.test(item)), false, 'Proxy UI touched cooldown or recovery state');
    assert.deepEqual(pageErrors, []);
    const expectedConsoleFailures = consoleErrors.filter(message => message.includes('401 (Unauthorized)') || message.includes('net::ERR_FAILED'));
    assert.equal(expectedConsoleFailures.length, consoleErrors.length, `Unexpected browser console errors: ${consoleErrors.join(' | ')}`);
    assert.equal(consoleErrors.filter(message => message.includes('net::ERR_FAILED')).length, 2, 'Response-loss fixtures emitted unexpected console failures');
    assertRedacted(logs);
    assertRedacted(consoleErrors.join('\n'));
    assertRedacted(await page.locator('body').innerText());
    const result = { status: 'PASS', proxies: 1, createRequests: creates.length, patchRequests: writeRequests.filter(item => item.kind === 'patch').length, bindingWrites: writeRequests.filter(item => item.kind === 'binding').length, targetCalls, pageErrors: 0, expectedConsoleFailures: consoleErrors.length, screenshots: ['outbound-proxy-desktop.png', 'outbound-proxy-mobile.png'] };
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify(result, null, 2));
    console.log('PASS: real Go + browser proxy create idempotency, uncertain PATCH recovery, enable/disable, bind/unbind, redaction, desktop/mobile');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const stopped = once(child, 'exit');
      child.stdin.end();
      const timer = setTimeout(() => child.kill(), 5000);
      try { await stopped; } finally { clearTimeout(timer); }
    }
    target.closeAllConnections();
    if (target.listening) await new Promise(resolve => target.close(resolve));
    const resolved = path.resolve(dataDirectory);
    assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-egress-ui-'), 'Unexpected cleanup target');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
