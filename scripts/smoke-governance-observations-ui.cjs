// Independently authored acceptance: real disposable Go process, synthetic
// upstream traffic, and the built administrator UI.
// Usage: node smoke-governance-observations-ui.cjs <exe> <web/dist> <playwright module> <output dir>
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { randomBytes, randomUUID } = require('node:crypto');
const { once } = require('node:events');
const { setTimeout: delay } = require('node:timers/promises');

const [executable, web, playwrightPath, output] = process.argv.slice(2);
for (const value of [executable, web, playwrightPath, output]) {
  assert.ok(value && path.isAbsolute(value), 'Absolute paths required');
}
const { chromium } = require(playwrightPath);

(async () => {
  const dataDirectory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-governance-observation-ui-'));
  const password = randomBytes(24).toString('hex');
  const upstreamSecret = randomBytes(24).toString('hex');
  const prompt = `synthetic-observation-prompt-${randomUUID()}`;
  const answer = `synthetic-observation-answer-${randomUUID()}`;
  const secrets = [password, upstreamSecret, prompt, answer];
  const upstreamErrors = [];
  const upstreamModes = ['known', 'unknown', 'known'];
  let child;
  let browser;
  let logs = '';
  let upstreamCalls = 0;

  const upstream = http.createServer(async (request, response) => {
    try {
      upstreamCalls += 1;
      assert.equal(request.headers.authorization, `Bearer ${upstreamSecret}`);
      for await (const ignored of request) { void ignored; }
      const mode = upstreamModes.shift();
      assert.ok(mode, 'Unexpected extra upstream request');
      const usage = mode === 'unknown'
        ? { prompt_tokens: 100, completion_tokens: 50 }
        : {
            prompt_tokens: 100,
            completion_tokens: 50,
            total_tokens: 150,
            prompt_tokens_details: { cached_tokens: 20, cache_write_tokens: 10 },
          };
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({
        id: `synthetic-observation-${upstreamCalls}`,
        object: 'chat.completion',
        choices: [{ index: 0, message: { role: 'assistant', content: answer }, finish_reason: 'stop' }],
        usage,
      }));
    } catch (error) {
      upstreamErrors.push(error.message);
      if (!response.headersSent) response.writeHead(500);
      response.end();
    }
  });

  const launch = args => {
    const process = spawn(executable, ['--data-dir', dataDirectory, ...args], {
      windowsHide: true,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    for (const stream of [process.stdout, process.stderr]) {
      stream.on('data', chunk => {
        logs += chunk;
        if (logs.length > (1 << 20)) process.kill();
      });
    }
    return process;
  };

  const assertRedacted = value => {
    const bytes = Buffer.from(String(value));
    for (const secret of secrets) {
      assert.ok(!bytes.includes(Buffer.from(secret)), 'Synthetic secret entered logs, DOM, or storage');
    }
  };

  try {
    child = launch(['--init']);
    const initialized = once(child, 'exit');
    child.stdin.end(`${password}\n`);
    assert.equal((await initialized)[0], 0, 'Initialization failed');

    upstream.listen(0, '127.0.0.1');
    await once(upstream, 'listening');
    const upstreamOrigin = `http://127.0.0.1:${upstream.address().port}`;

    const reservation = http.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const origin = `http://127.0.0.1:${reservation.address().port}`;
    await new Promise(resolve => reservation.close(resolve));

    child = launch([
      '--listen', new URL(origin).host,
      '--web-dir', web,
      '--allow-loopback-upstream',
      '--shutdown-on-stdin-eof',
    ]);
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt += 1) {
      if (child.exitCode !== null) throw new Error('Service stopped during startup');
      try {
        ready = (await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok;
      } catch { /* readiness retry */ }
      if (ready) break;
      await delay(100);
    }
    assert.ok(ready, 'Readiness timeout');
    await fs.mkdir(output, { recursive: true });

    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const login = await context.request.post(`${origin}/admin/api/v1/sessions`, {
      headers: { Origin: origin },
      data: { username: 'admin', password },
    });
    assert.equal(login.status(), 200, 'Browser session creation failed');
    const session = await login.json();

    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    const pageErrors = [];
    const consoleErrors = [];
    const observationRequests = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()); });
    page.on('request', request => {
      const url = new URL(request.url());
      if (url.origin === origin && url.pathname === '/admin/api/v1/governance/observations') {
        observationRequests.push(url);
      }
    });
    await page.route('**/*', route => {
      if (!route.request().url().startsWith(`${origin}/`)) return route.abort();
      return route.continue();
    });
    await page.goto(origin);
    await page.getByRole('heading', { name: '员工与 Key', exact: true }).waitFor();

    const api = async (method, pathname, data, expected = 200) => {
      const response = await context.request.fetch(`${origin}/admin/api/v1${pathname}`, {
        method,
        headers: { Origin: origin, 'X-CSRF-Token': session.csrf_token },
        data,
      });
      const raw = await response.text();
      assertRedacted(raw);
      assert.equal(response.status(), expected, `${method} ${pathname}: ${response.status()} ${raw}`);
      return raw ? JSON.parse(raw) : {};
    };

    const account = await api('POST', '/upstreams', {
      name: 'Synthetic observation account',
      provider_kind: 'openai-compatible',
      endpoint: `${upstreamOrigin}/v1`,
      api_key: upstreamSecret,
    }, 201);
    await api('POST', '/models', {
      id: 'observation-browser-model',
      upstream_id: account.id,
      upstream_model: 'synthetic-observation-model',
    }, 201);
    const employee = await api('POST', '/employees', { name: 'Synthetic observation employee' }, 201);
    const key = await api('POST', `/employees/${employee.id}/keys`, {
      name: 'Synthetic observation browser key',
      operation_id: randomUUID(),
    }, 201);
    secrets.push(key.key);

    const noHard = { rpm: null, concurrency: null };
    const noShadow = { tpm: null, cost_micro: null, currency: null, window: null };
    const shadow = (tpm, cost) => ({ tpm, cost_micro: cost, currency: 'USD', window: 'rolling_24h' });
    await api('POST', '/governance/policies', {
      operation_id: randomUUID(), scope_kind: 'employee', scope_id: employee.id,
      enabled: true, hard: noHard, shadow: shadow(200, '100'),
    });
    await api('POST', '/governance/policies', {
      operation_id: randomUUID(), scope_kind: 'key', scope_id: key.id,
      enabled: true, hard: noHard, shadow: shadow(9007199254740991, '9223372036854775807'),
    });

    const mainGroup = await api('POST', '/governance/groups', {
      operation_id: randomUUID(), name: 'Synthetic observation main group', employee_ids: [employee.id],
    });
    await api('POST', '/governance/policies', {
      operation_id: randomUUID(), scope_kind: 'group', scope_id: mainGroup.resource_id,
      enabled: true, hard: noHard, shadow: shadow(1000, '1000'),
    });

    // Twenty-one immutable snapshot rows force the real UI through cursor pagination.
    for (let index = 1; index <= 18; index += 1) {
      const group = await api('POST', '/governance/groups', {
        operation_id: randomUUID(),
        name: `Synthetic pagination group ${String(index).padStart(2, '0')}`,
        employee_ids: [employee.id],
      });
      await api('POST', '/governance/policies', {
        operation_id: randomUUID(), scope_kind: 'group', scope_id: group.resource_id,
        enabled: true, hard: { rpm: 100, concurrency: null }, shadow: noShadow,
      });
    }

    const settings = await api('GET', '/governance/settings');
    await api('PUT', '/governance/settings', {
      operation_id: randomUUID(), expected_revision: settings.revision, enabled: true,
    });
    const rates = currency => ({
      currency,
      input_per_million_micro: '1000000',
      output_per_million_micro: '2000000',
      cache_read_per_million_micro: '100000',
      cache_write_per_million_micro: '1500000',
    });
    const setPrice = (revision, currency) => api('POST', `/upstreams/${account.id}/prices`, {
      operation_id: randomUUID(), expected_revision: revision,
      upstream_model: 'synthetic-observation-model', price: rates(currency),
    });
    await setPrice(0, 'USD');

    const generate = async () => {
      const response = await fetch(`${origin}/v1/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${key.key}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'observation-browser-model', messages: [{ role: 'user', content: prompt }] }),
        signal: AbortSignal.timeout(15000),
      });
      assert.equal(response.status, 200, `Generation failed: ${response.status}`);
      const body = await response.json();
      assert.equal(body.choices[0].message.content, answer);
    };
    await generate();
    await generate();
    await setPrice(1, 'EUR');
    await generate();
    assert.equal(upstreamCalls, 3);
    assert.equal(upstreamModes.length, 0);

    await page.getByRole('button', { name: '请求治理', exact: true }).click();
    await page.getByRole('heading', { name: '请求治理', exact: true }).waitFor();
    await page.getByRole('button', { name: '查看用量观测', exact: true }).click();
    await page.getByText('第 1 页', { exact: true }).waitFor();
    assert.equal(await page.locator('.observation-card').count(), 20, 'First page did not use the UI limit');
    assert.ok(await page.getByRole('button', { name: '下一页', exact: true }).isEnabled());
    await page.getByRole('button', { name: '下一页', exact: true }).click();
    await page.getByText('第 2 页', { exact: true }).waitFor();
    assert.equal(await page.locator('.observation-card').count(), 1, 'Second page did not contain the remaining snapshot');
    assert.ok(observationRequests.at(-1).searchParams.has('cursor'), 'Next page omitted its cursor');
    await page.getByRole('button', { name: '刷新首屏', exact: true }).click();
    await page.getByText('第 1 页', { exact: true }).waitFor();
    assert.equal(await page.locator('.observation-card').count(), 20);
    assert.ok(!observationRequests.at(-1).searchParams.has('cursor'), 'First-page refresh retained a cursor');

    const filter = async (scopeKind, scopeID = '', expectedScopeID = scopeID || employee.id) => {
      await page.getByLabel('范围类型', { exact: true }).selectOption(scopeKind);
      await page.getByLabel('范围 ID（可选）', { exact: true }).fill(scopeID);
      await page.getByLabel('策略 ID（可选）', { exact: true }).fill('');
      await page.getByRole('button', { name: '应用筛选', exact: true }).click();
      await page.getByText('第 1 页', { exact: true }).waitFor();
      await page.locator('.observation-card > header strong').getByText(expectedScopeID, { exact: true }).waitFor();
      assert.equal(await page.locator('.observation-card').count(), 1);
    };

    await filter('employee');
    assert.equal(observationRequests.at(-1).searchParams.get('scope_kind'), 'employee');
    assert.equal(observationRequests.at(-1).searchParams.has('scope_id'), false, 'Kind-only filter sent an empty scope ID');
    let card = page.locator('.observation-card');
    await card.getByText('300', { exact: true }).waitFor();
    assert.equal(await card.getByText('未知尝试', { exact: false }).count(), 2);
    assert.equal(await card.getByText('1', { exact: true }).count(), 2, 'Unknown token and cost attempts were not both shown');
    await card.getByText('0.000187 EUR', { exact: true }).waitFor();
    await card.getByText('0.000187 USD', { exact: true }).waitFor();
    assert.equal(await card.getByText('已超过', { exact: true }).count(), 2);
    assert.equal(await page.getByText(/同一请求可同时计入员工、Key 与治理组/).count(), 1);
    assert.equal(await page.getByText(/请勿把不同卡片的 Token 或金额相加/).count(), 1);
    await page.screenshot({
      path: path.join(output, 'governance-observations-desktop.png'),
      animations: 'disabled',
      fullPage: true,
    });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'Desktop page overflowed horizontally');

    await filter('key', key.id);
    card = page.locator('.observation-card');
    await card.getByText('9,007,199,254,740,991 Token', { exact: false }).waitFor();
    await card.getByText('9,223,372,036,854.775807 USD 阈值', { exact: true }).waitFor();
    assert.equal(await card.getByText('无法判断', { exact: true }).count(), 2);

    await filter('group', mainGroup.resource_id);
    card = page.locator('.observation-card');
    assert.equal(await card.getByText('无法判断', { exact: true }).count(), 2);
    await page.setViewportSize({ width: 390, height: 844 });
    await card.scrollIntoViewIfNeeded();
    assert.ok(await page.getByRole('button', { name: '刷新首屏', exact: true }).isVisible());
    assert.ok(await page.getByRole('button', { name: '应用筛选', exact: true }).isVisible());
    assert.ok(await page.getByRole('button', { name: '上一页', exact: true }).isVisible());
    assert.ok(await page.getByRole('button', { name: '下一页', exact: true }).isVisible());
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'Mobile page overflowed horizontally');
    await page.screenshot({
      path: path.join(output, 'governance-observations-mobile.png'),
      animations: 'disabled',
      fullPage: true,
    });

    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.getByLabel('范围类型', { exact: true }).selectOption('');
    await page.getByLabel('范围 ID（可选）', { exact: true }).fill('');
    await page.getByLabel('策略 ID（可选）', { exact: true }).fill(`missing-policy-${randomUUID()}`);
    await page.getByRole('button', { name: '应用筛选', exact: true }).click();
    await page.getByText('窗口内无观测', { exact: true }).waitFor();
    assert.equal(await page.locator('.observation-card').count(), 0);
    assert.equal(await page.locator('.observation-state--below').count(), 0, 'Empty observations fabricated a below state');

    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0, 'Observation data entered browser storage');
    assert.deepEqual(pageErrors, []);
    assert.deepEqual(consoleErrors, []);
    assert.deepEqual(upstreamErrors, []);
    assertRedacted(logs);
    assertRedacted(consoleErrors.join('\n'));
    assertRedacted(await page.locator('body').innerText());

    const result = {
      status: 'PASS',
      upstreamCalls,
      snapshotRows: 21,
      firstPageRows: 20,
      secondPageRows: 1,
      knownTokens: '300',
      unknownTokenAttempts: '1',
      currencies: { EUR: '187', USD: '187' },
      pageErrors: 0,
      consoleErrors: 0,
      screenshots: ['governance-observations-desktop.png', 'governance-observations-mobile.png'],
    };
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify(result, null, 2));
    console.log('PASS: real Go + browser governance observations, three scopes, known/unknown usage, currencies, cursor refresh, empty state, desktop/mobile, redaction');
  } finally {
    await browser?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      const stopped = once(child, 'exit');
      child.stdin.end();
      const timer = setTimeout(() => child.kill(), 5000);
      try { await stopped; } finally { clearTimeout(timer); }
    }
    upstream.closeAllConnections();
    if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
    const resolved = path.resolve(dataDirectory);
    assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()), 'Unexpected cleanup parent');
    assert.ok(path.basename(resolved).startsWith('cpac-governance-observation-ui-'), 'Unexpected cleanup directory');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => {
  console.error(error.stack || error.message);
  process.exitCode = 1;
});
