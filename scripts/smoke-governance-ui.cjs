// Real isolated Go process + browser acceptance for governance administration.
// Usage: node smoke-governance-ui.cjs <exe> <web/dist> <playwright module> <output dir>
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
for (const value of [executable, web, playwrightPath, output]) assert.ok(value && path.isAbsolute(value), 'Absolute paths required');
const { chromium } = require(playwrightPath);

(async () => {
  const dataDirectory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-governance-ui-'));
  const password = randomBytes(24).toString('hex');
  const secrets = [password];
  let child, browser, logs = '';
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

    const reservation = http.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const origin = `http://127.0.0.1:${reservation.address().port}`;
    await new Promise(resolve => reservation.close(resolve));

    child = launch(['--listen', new URL(origin).host, '--web-dir', web, '--shutdown-on-stdin-eof']);
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
    page.setDefaultTimeout(12000);
    const pageErrors = [], consoleErrors = [], visited = [];
    const groupWrites = [], policyWrites = [];
    let loseCommittedGroup = true;
    let dropNextPolicyBeforeSend = false;
    page.on('pageerror', error => pageErrors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()); });
    await page.route('**/*', async route => {
      const request = route.request();
      const url = request.url();
      if (!url.startsWith(origin + '/')) return route.abort();
      const pathname = new URL(url).pathname;
      visited.push(`${request.method()} ${pathname}`);
      if (pathname === '/admin/api/v1/governance/groups' && request.method() === 'POST') {
        groupWrites.push(request.postData());
        if (loseCommittedGroup) {
          loseCommittedGroup = false;
          const response = await route.fetch();
          assert.equal(response.status(), 200, 'Lost governance group write did not commit');
          await response.dispose();
          return route.abort('failed');
        }
      }
      if (pathname === '/admin/api/v1/governance/policies' && request.method() === 'POST') {
        policyWrites.push(request.postData());
        if (dropNextPolicyBeforeSend) {
          dropNextPolicyBeforeSend = false;
          return route.abort('failed');
        }
      }
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

    const alice = await api('POST', '/employees', { name: 'Alice Governance' }, 201);
    const bob = await api('POST', '/employees', { name: 'Bob Governance' }, 201);
    const accessKey = await api('POST', `/employees/${alice.id}/keys`, { operation_id: randomUUID(), name: 'Governance Laptop', expires_at: null }, 201);
    if (accessKey.key) secrets.push(accessKey.key);

    await page.getByRole('button', { name: '请求治理', exact: true }).click();
    await page.getByRole('heading', { name: '请求治理', exact: true }).waitFor();
    await page.getByText('默认关闭 / 当前关闭', { exact: true }).waitFor();
    assert.equal(await page.getByText(/TPM 与成本目前只保存 shadow 配置/).count(), 1);
    assert.equal(await page.getByText(/治理组与上游账号组彼此独立/).count(), 1);
    await page.getByRole('button', { name: '启用治理', exact: true }).click();
    await page.getByText('已启用', { exact: true }).waitFor();

    await page.getByRole('button', { name: '新建治理组', exact: true }).click();
    let dialog = page.getByRole('dialog');
    await dialog.getByLabel('治理组名称', { exact: true }).fill('Browser Governance Group');
    await dialog.getByRole('checkbox', { name: /Alice Governance/ }).check();
    await dialog.getByRole('checkbox', { name: /Bob Governance/ }).check();
    await dialog.getByRole('button', { name: '保存治理组', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });
    assert.equal(groupWrites.length, 1, 'Committed response loss replayed the group POST');
    const groupWrite = JSON.parse(groupWrites[0]);
    assert.deepEqual(groupWrite.employee_ids, [alice.id, bob.id].sort());
    assert.ok(visited.includes(`GET /admin/api/v1/governance/operations/${groupWrite.operation_id}`), 'Committed response loss did not query the original receipt');
    const groupPage = await api('GET', '/governance/groups?limit=100');
    const governanceGroup = groupPage.items.find(item => item.name === 'Browser Governance Group');
    assert.ok(governanceGroup, 'Governance group missing after receipt recovery');

    // Employee policy: hard RPM only.
    await page.getByRole('button', { name: '新建策略', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByLabel('作用对象', { exact: true }).selectOption(alice.id);
    await dialog.getByLabel('RPM 硬限制', { exact: true }).fill('5');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });

    // Key policy: first POST never reaches the server. A 404 receipt lookup must
    // stay unknown until the administrator retries the exact frozen request.
    dropNextPolicyBeforeSend = true;
    await page.getByRole('button', { name: '新建策略', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByLabel('作用范围', { exact: true }).selectOption('key');
    await dialog.getByLabel('作用对象', { exact: true }).selectOption(accessKey.id);
    await dialog.getByLabel('并发硬限制', { exact: true }).fill('2');
    await dialog.getByLabel('成本 shadow 阈值（micro）', { exact: true }).fill('9223372036854775807');
    await dialog.getByLabel('成本币种', { exact: true }).fill('USD');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).click();
    await dialog.getByText('写入结果尚未确认', { exact: true }).waitFor();
    await dialog.getByText(/服务暂未查到原操作/).waitFor();
    const keyUnknown = JSON.parse(policyWrites.at(-1));
    await dialog.getByText(keyUnknown.operation_id, { exact: true }).waitFor();
    await dialog.getByRole('button', { name: '用原操作重试', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });
    const keyWrites = policyWrites.map(value => JSON.parse(value)).filter(value => value.scope_kind === 'key');
    assert.equal(keyWrites.length, 2);
    assert.deepEqual(keyWrites[1], keyWrites[0], 'Unknown write retry changed operation UUID or payload');
    assert.equal(typeof keyWrites[0].shadow.cost_micro, 'string');
    assert.equal(keyWrites[0].shadow.cost_micro, '9223372036854775807');

    // Group policy: shadow TPM is configuration only.
    await page.getByRole('button', { name: '新建策略', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByLabel('作用范围', { exact: true }).selectOption('group');
    await dialog.getByLabel('作用对象', { exact: true }).selectOption(governanceGroup.id);
    await dialog.getByLabel('RPM 硬限制', { exact: true }).fill('8');
    await dialog.getByLabel('TPM shadow 阈值', { exact: true }).fill('1000');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });

    let policyPage = await api('GET', '/governance/policies?limit=100');
    assert.equal(policyPage.items.length, 3, 'Expected employee, key and group governance policies');
    const employeePolicy = policyPage.items.find(item => item.scope_kind === 'employee');
    const keyPolicy = policyPage.items.find(item => item.scope_kind === 'key');
    const groupPolicy = policyPage.items.find(item => item.scope_kind === 'group');
    assert.ok(employeePolicy && keyPolicy && groupPolicy);
    assert.equal(keyPolicy.shadow.cost_micro, '9223372036854775807');
    assert.equal(keyPolicy.shadow.currency, 'USD');
    assert.equal(keyPolicy.shadow.window, 'rolling_24h');

    // Open a stale editor, then mutate the same policy through the real API.
    const employeeRow = page.locator('tr').filter({ hasText: '员工 · Alice Governance' });
    await employeeRow.getByRole('button', { name: '编辑策略', exact: true }).click();
    dialog = page.getByRole('dialog');
    await api('PUT', `/governance/policies/${employeePolicy.id}`, {
      operation_id: randomUUID(), expected_revision: employeePolicy.revision, enabled: true,
      hard: { rpm: 6, concurrency: null }, shadow: { tpm: null, cost_micro: null, currency: null, window: null },
    });
    await dialog.getByLabel('RPM 硬限制', { exact: true }).fill('7');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).click();
    await dialog.getByText('服务器当前策略 r2', { exact: true }).waitFor();
    assert.ok(await dialog.getByRole('button', { name: '保存策略', exact: true }).isDisabled(), 'CAS conflict left stale write enabled');
    await dialog.getByRole('button', { name: '按最新状态重新编辑', exact: true }).click();
    assert.equal(await dialog.getByLabel('RPM 硬限制', { exact: true }).inputValue(), '6');
    await dialog.getByLabel('RPM 硬限制', { exact: true }).fill('7');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).click();
    await dialog.waitFor({ state: 'detached' });

    policyPage = await api('GET', '/governance/policies?limit=100');
    assert.equal(policyPage.items.find(item => item.id === employeePolicy.id).hard.rpm, 7);
    assert.equal(policyPage.items.find(item => item.id === keyPolicy.id).revision, keyPolicy.revision, 'Employee policy update changed Key policy revision');
    assert.equal(await page.getByText(/^余额充足$|^低于阈值$|^将会拦截$/).count(), 0, 'UI fabricated a shadow observation');

    await page.screenshot({ path: path.join(output, 'governance-desktop.png'), animations: 'disabled', fullPage: true });
    await page.getByRole('button', { name: '关闭治理', exact: true }).click();
    await page.getByText('默认关闭 / 当前关闭', { exact: true }).waitFor();
    const finalSettings = await api('GET', '/governance/settings');
    assert.equal(finalSettings.enabled, false);

    await page.setViewportSize({ width: 390, height: 844 });
    const keyRow = page.locator('tr').filter({ hasText: 'Key · Alice Governance · Governance Laptop' });
    await keyRow.getByRole('button', { name: '编辑策略', exact: true }).scrollIntoViewIfNeeded();
    await keyRow.getByRole('button', { name: '编辑策略', exact: true }).click();
    dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: '保存策略', exact: true }).scrollIntoViewIfNeeded();
    assert.ok(await dialog.getByRole('button', { name: '保存策略', exact: true }).isVisible(), 'Mobile policy controls are unreachable');
    await page.screenshot({ path: path.join(output, 'governance-mobile-policy.png'), animations: 'disabled' });
    await dialog.getByRole('button', { name: '取消', exact: true }).click();

    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0, 'Governance payload entered browser storage');
    assert.deepEqual(pageErrors, []);
    const expectedConsoleFailures = consoleErrors.filter(message => message.includes('401 (Unauthorized)') || message.includes('net::ERR_FAILED') || message.includes('status of 404 (Not Found)') || message.includes('status of 409 (Conflict)'));
    assert.equal(expectedConsoleFailures.length, consoleErrors.length, `Unexpected browser console errors: ${consoleErrors.join(' | ')}`);
    assert.equal(consoleErrors.filter(message => message.includes('net::ERR_FAILED')).length, 2, 'Unknown-write fixtures emitted unexpected console failures');
    assert.equal(consoleErrors.filter(message => message.includes('status of 404 (Not Found)')).length, 1, 'Receipt-unknown fixture emitted unexpected 404s');
    assert.equal(consoleErrors.filter(message => message.includes('status of 409 (Conflict)')).length, 1, 'CAS fixture emitted unexpected conflicts');
    assertRedacted(logs);
    assertRedacted(consoleErrors.join('\n'));
    assertRedacted(await page.locator('body').innerText());
    const result = {
      status: 'PASS', groups: groupPage.items.length, policies: policyPage.items.length,
      committedLossGroupPosts: groupWrites.length, unknownKeyPolicyPosts: keyWrites.length,
      costMicro: keyPolicy.shadow.cost_micro, finalEnabled: finalSettings.enabled,
      pageErrors: 0, expectedConsoleFailures: consoleErrors.length,
      screenshots: ['governance-desktop.png', 'governance-mobile-policy.png'],
    };
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify(result, null, 2));
    console.log('PASS: real Go + browser governance settings, explicit group, three scopes, receipt recovery, CAS confirmation, decimal cost and mobile controls');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const stopped = once(child, 'exit');
      child.stdin.end();
      const timer = setTimeout(() => child.kill(), 5000);
      try { await stopped; } finally { clearTimeout(timer); }
    }
    const resolved = path.resolve(dataDirectory);
    assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-governance-ui-'), 'Unexpected cleanup target');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
