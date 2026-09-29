// Independent browser acceptance for docs/scheduled-tests-daily-timezone-contract.md.
// Usage: node script ABS_SERVER ABS_BUILT_WEB ABS_PLAYWRIGHT_MODULE ABS_OUTPUT_DIR
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

async function freeOrigin() {
  const probe = http.createServer();
  probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  assert.notEqual(port, 8787, 'Do not use the existing-service port');
  return `http://127.0.0.1:${port}`;
}

(async () => {
  const dataDir = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-scheduled-daily-ui-'));
  const password = randomBytes(24).toString('hex');
  const secret = 'synthetic-daily-key-' + randomBytes(24).toString('hex');
  let child, browser, serviceLogs = '', providerCalls = 0;
  const mock = http.createServer((_, response) => { providerCalls++; response.writeHead(418).end(); });
  const launch = args => {
    const proc = spawn(executable, ['--data-dir', dataDir, ...args], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { serviceLogs += chunk; if (serviceLogs.length > 1 << 20) proc.kill(); });
    return proc;
  };
  try {
    child = launch(['--init']);
    const initialized = once(child, 'exit'); child.stdin.end(password + '\n');
    assert.equal((await initialized)[0], 0, 'Initialization failed');
    mock.listen(0, '127.0.0.1'); await once(mock, 'listening');
    const origin = await freeOrigin();
    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
    let ready = false;
    for (let attempt = 0; attempt < 120; attempt++) {
      if (child.exitCode !== null) throw new Error(`Service stopped before readiness: ${serviceLogs.slice(-1000)}`);
      try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { /* readiness */ }
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'Service readiness timeout');
    await fs.mkdir(output, { recursive: true });
    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    const pageErrors = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    await page.goto(origin);
    assert.match(await page.title(), /CPA Cloud/);
    assert.match(await page.locator('body').innerText(), /用户名|登录/);
    await page.getByLabel('用户名', { exact: true }).fill('admin');
    await page.getByLabel('密码', { exact: true }).fill(password);
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();
    const sessionResponse = await context.request.get(origin + '/admin/api/v1/session');
    assert.equal(sessionResponse.status(), 200);
    const session = await sessionResponse.json();
    const accountResponse = await context.request.post(origin + '/admin/api/v1/upstreams', {
      headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token },
      data: { name: 'Synthetic daily account', provider_kind: 'openai-compatible', endpoint: `http://127.0.0.1:${mock.address().port}`, api_key: secret },
    });
    assert.equal(accountResponse.status(), 201, await accountResponse.text());
    const account = await accountResponse.json();
    await page.getByRole('button', { name: '定时测试', exact: true }).click();
    await page.getByRole('heading', { name: '定时测试', exact: true }).waitFor();
    assert.match(await page.locator('body').innerText(), /按固定间隔或命名时区每日/);
    await page.getByRole('button', { name: '创建计划', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('计划名称').fill('纽约每日凭据检查');
    await dialog.getByLabel('上游账号').selectOption(account.id);
    await dialog.getByLabel('计划方式').selectOption('daily_local');
    await dialog.getByLabel('IANA 命名时区').fill('America/New_York');
    await dialog.getByLabel('当地时间（HH:mm）').fill('01:30');
    await dialog.getByRole('checkbox').check();
    const [saved] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith('/admin/api/v1/scheduled-tests') && response.request().method() === 'POST'),
      dialog.getByRole('button', { name: '保存计划' }).click(),
    ]);
    assert.equal(saved.status(), 201, await saved.text());
    const requestBody = saved.request().postDataJSON();
    assert.equal(requestBody.schedule_mode, 'daily_local');
    assert.equal(requestBody.time_zone, 'America/New_York');
    assert.equal(requestBody.local_time, '01:30');
    assert.ok(!Object.hasOwn(requestBody, 'interval_seconds'));
    const plan = await saved.json();
    assert.equal(plan.schedule_mode, 'daily_local');
    const utc = new Date(plan.next_run_at).toISOString().replace('.000Z', 'Z');
    await page.getByText('每天 01:30 · America/New_York', { exact: true }).waitFor();
    await page.getByText(`下次 UTC：${utc}`, { exact: true }).waitFor();
    assert.equal(await page.locator('.vite-error-overlay, vite-error-overlay').count(), 0);
    await page.screenshot({ path: path.join(output, 'scheduled-daily-desktop.png') });
    await page.getByRole('button', { name: '编辑', exact: true }).click();
    await dialog.getByLabel('计划方式').selectOption('daily_local');
    assert.equal(await dialog.getByLabel('IANA 命名时区').inputValue(), 'America/New_York');
    assert.match(await dialog.innerText(), /已保存的下次运行（UTC）/);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'Mobile page overflow');
    await page.screenshot({ path: path.join(output, 'scheduled-daily-mobile.png') });
    assert.deepEqual(pageErrors, []);
    assert.equal(providerCalls, 0, 'Default-off daily schedule touched the synthetic provider');
    assert.ok(!serviceLogs.includes(password) && !serviceLogs.includes(secret));
    assert.ok(!(await page.locator('body').innerText()).includes(secret));
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify({ origin, plan_id: plan.id, next_run_at: plan.next_run_at, provider_calls: providerCalls, page_errors: pageErrors }, null, 2));
    console.log('PASS: real Go + browser daily schedule create, UTC result, capability UI, desktop/mobile and no provider call');
  } finally {
    if (browser) await browser.close();
    if (child && child.exitCode === null) {
      const exited = once(child, 'exit'); child.stdin.end();
      await Promise.race([exited, delay(5000)]);
      if (child.exitCode === null) child.kill();
    }
    mock.closeAllConnections();
    if (mock.listening) await new Promise(resolve => mock.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
