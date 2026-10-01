// Synthetic real-service browser acceptance for docs/subscription-one-shot-renewal-contract.md.
// Usage: node script ABS_SERVER ABS_BUILT_WEB ABS_PLAYWRIGHT_MODULE ABS_OUTPUT_DIR
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const http = require('node:http');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { randomBytes, randomUUID } = require('node:crypto');
const { setTimeout: delay } = require('node:timers/promises');

const [executable, web, playwrightPath, output] = process.argv.slice(2);
for (const value of [executable, web, playwrightPath, output]) assert.ok(value && path.isAbsolute(value), 'absolute paths required');
const { chromium } = require(playwrightPath);

async function freeOrigin() {
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const origin = `http://127.0.0.1:${probe.address().port}`;
  await new Promise(resolve => probe.close(resolve));
  assert.notEqual(new URL(origin).port, '8787');
  return origin;
}

(async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-one-shot-ui-'));
  const password = randomBytes(24).toString('hex');
  let child, browser, logs = '';
  const launch = args => {
    const process = spawn(executable, ['--data-dir', directory, ...args], { windowsHide: true, stdio: ['pipe', 'ignore', 'pipe'] });
    process.stderr.on('data', chunk => { logs = (logs + chunk.toString()).slice(-4000); });
    return process;
  };
  try {
    child = launch(['--init']); const initialized = once(child, 'exit'); child.stdin.end(password + '\n');
    assert.equal((await initialized)[0], 0, 'initialization failed');
    const origin = await freeOrigin();
    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--shutdown-on-stdin-eof']);
    let ready = false;
    for (let attempt = 0; attempt < 120; attempt++) {
      if (child.exitCode !== null) throw new Error(`service stopped before ready: ${logs}`);
      try { ready = (await fetch(origin + '/healthz', { signal: AbortSignal.timeout(500) })).ok; } catch { /* startup */ }
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'service readiness timeout');
    await fs.mkdir(output, { recursive: true });
    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1365, height: 900 } });
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
    const session = await (await context.request.get(origin + '/admin/api/v1/session')).json();
    const post = async (route, data, expected = 200) => {
      const response = await context.request.post(origin + '/admin/api/v1' + route, { headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token }, data });
      assert.equal(response.status(), expected, `${route}: ${await response.text()}`);
      return response.json();
    };
    const employee = await post('/employees', { name: 'Synthetic one-shot browser employee' }, 201);
    const owner = { kind: 'employee', employee_id: employee.id };
    await context.request.put(origin + '/admin/api/v1/billing/settings', { headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token }, data: { operation_id: randomUUID(), expected_revision: 1, enabled: true } }).then(response => assert.equal(response.status(), 200));
    await post('/billing/adjustments', { operation_id: randomUUID(), owner, currency: 'USD', amount_micro: '100' });
    const plan = (await post('/billing/plans', { operation_id: randomUUID(), name: 'Synthetic monthly', currency: 'USD', price_micro: '10', credit_micro: '20', interval: 'monthly', enabled: true })).plan;
    const purchased = (await post('/billing/subscriptions', { operation_id: randomUUID(), owner, plan_id: plan.id })).subscription;
    await page.getByRole('button', { name: '商业管理', exact: true }).click();
    await page.getByRole('button', { name: '套餐与订阅', exact: true }).click();
    await page.getByText(purchased.id, { exact: true }).waitFor();
    await page.getByRole('button', { name: '一次性预约状态' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByText('未预约', { exact: true }).waitFor();
    await dialog.getByRole('button', { name: '预约一次' }).click();
    await dialog.getByText(/后继订阅不会继承预约/).waitFor();
    await dialog.getByRole('button', { name: '确认预约一次' }).click();
    await dialog.getByText('已预约，等待到期尝试', { exact: true }).waitFor();
    const settleDialog = () => page.locator('.dialog-backdrop').evaluate(async element => {
      await Promise.all(element.getAnimations({ subtree: true }).map(animation => animation.finished));
    });
    await settleDialog();
    assert.equal(await page.locator('.vite-error-overlay, vite-error-overlay').count(), 0);
    assert.deepEqual(pageErrors, []);
    await page.screenshot({ path: path.join(output, 'one-shot-desktop-armed.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await settleDialog();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, 'mobile page overflow');
    await page.screenshot({ path: path.join(output, 'one-shot-mobile-armed.png') });
    await dialog.getByRole('button', { name: '解除预约' }).click();
    await dialog.getByText(/无法再次预约/).waitFor();
    await dialog.getByRole('button', { name: '确认解除且不再预约' }).click();
    await dialog.getByText('已解除（不可重新预约同一期）', { exact: true }).waitFor();
    await settleDialog();
    assert.equal(await dialog.getByRole('button', { name: '预约一次' }).count(), 0);
    await page.screenshot({ path: path.join(output, 'one-shot-mobile-disarmed.png') });
    assert.deepEqual(pageErrors, []);
    console.log(`one-shot UI browser acceptance passed: ${origin}, desktop 1365px and mobile 390px`);
  } finally {
    if (browser) await browser.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const exit = once(child, 'exit'); child.stdin.end();
      const deadline = setTimeout(() => child.kill(), 5000);
      try { await exit; } finally { clearTimeout(deadline); }
    }
    const resolved = path.resolve(directory);
    if (path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-one-shot-ui-')) await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
