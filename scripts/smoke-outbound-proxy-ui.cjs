// Real isolated Go process + browser acceptance for outbound proxy administration.
// Usage: node smoke-outbound-proxy-ui.cjs <exe> <web/dist> <playwright module> <output dir>
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const http = require('node:http');
const https = require('node:https');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { randomBytes } = require('node:crypto');
const { setTimeout: delay } = require('node:timers/promises');

const [executable, web, playwrightPath, output] = process.argv.slice(2);
for (const value of [executable, web, playwrightPath, output]) assert.ok(value && path.isAbsolute(value), 'Absolute paths required');
const { chromium } = require(playwrightPath);

// Synthetic certificate for wrong.invalid. It is intentionally untrusted and has
// the wrong hostname so the production proxy TLS verifier must reject it.
const badProxyCertificate = `-----BEGIN CERTIFICATE-----
MIIDCDCCAfCgAwIBAgIPFWGbugxnyTx9kn9lWSmyMA0GCSqGSIb3DQEBCwUAMBgx
FjAUBgNVBAMTDXdyb25nLmludmFsaWQwHhcNMjYwMTAxMDAwMDAwWhcNMzAwMTAx
MDAwMDAwWjAYMRYwFAYDVQQDEw13cm9uZy5pbnZhbGlkMIIBIjANBgkqhkiG9w0B
AQEFAAOCAQ8AMIIBCgKCAQEAqmmj/WjRGearWxCY2/BnR9J1FR6LTG1cHW5RIjHJ
xd93pOJ7E+tfOynzhSg5/HMYmkk5OwFqLykU2ZrATnCBSlbxOQO7XEiao7Ip4fjh
1pBcIoWGw/lXgkytSONrpsa5LHp1OYH4RbNzBEagjsHsBHy8L5vDjpaupWp0UIrC
0wk9JENS2ue3Dn5hQaPg8/gHncvfqMq/kgnN41z8syx5KcUJORGHbVNKAYcd0eAW
xSY4LOWG1AI7ocGleRwu0lg/LIGkPVw2NnGRLZTiQPLFEzQxewQYauBnj1iJbC7+
b8Q1YoCSEVMxSohomT3MZOIaq9uuo+Z+bhwhWDoWl/9BzQIDAQABo08wTTAOBgNV
HQ8BAf8EBAMCBaAwEwYDVR0lBAwwCgYIKwYBBQUHAwEwDAYDVR0TAQH/BAIwADAY
BgNVHREEETAPgg13cm9uZy5pbnZhbGlkMA0GCSqGSIb3DQEBCwUAA4IBAQBf3QCR
P/CtRxXJbryUNImpvjeK1S6xUBHdyMx6yAMLxSwH4dS0fOJQeTLWQybWx/+Gvj/W
KUrrR0Msb97BVU5dUZnoROfmNhsx7UEDMRYXi6iLBrzaZgKEFegV7knpI8FOaFUi
dg+7b8TyILyYRgqw9X7Ubzbc87rduepqUjjZkUKaFOWOSIOWztvxJ7j1yxe42UcD
WdkOd6Dtfp9std+kNRsNQN3etmuYok6HZtQL7sY4oTcwgxZ6TxpbnVamCI+7aX65
IX9STqrFqQbkM8OpoL8zz1N9zDSIOQFMXlmo6b6Mgtip7lzegHHJKT88LB+KwR4s
etnVd6Mb4HGdzGJl
-----END CERTIFICATE-----`;
const badProxyKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAqmmj/WjRGearWxCY2/BnR9J1FR6LTG1cHW5RIjHJxd93pOJ7
E+tfOynzhSg5/HMYmkk5OwFqLykU2ZrATnCBSlbxOQO7XEiao7Ip4fjh1pBcIoWG
w/lXgkytSONrpsa5LHp1OYH4RbNzBEagjsHsBHy8L5vDjpaupWp0UIrC0wk9JENS
2ue3Dn5hQaPg8/gHncvfqMq/kgnN41z8syx5KcUJORGHbVNKAYcd0eAWxSY4LOWG
1AI7ocGleRwu0lg/LIGkPVw2NnGRLZTiQPLFEzQxewQYauBnj1iJbC7+b8Q1YoCS
EVMxSohomT3MZOIaq9uuo+Z+bhwhWDoWl/9BzQIDAQABAoIBAElGa3FHZMISYZQi
qtfHo2FKqXWPUK5oR7eP++sMJYqj8DpB+FI0Xxp9i2yyQ1y90NJmsekhTptAuupm
lFImJjHk+IxfgmzH+1ZwAXpdHh64rCVb7PrPeEVa2xgAUgXAZVcuwMEdlbfC1a39
AITh9a5oRDLkc04YlLgj8ie/ws4i8uUesmP0NBlfbv4nSeMNsMzIf/VY9F5O0rY5
VJKzEdYCvmGDkDMBXd2nHymyRlchEzWsIkyWUpWSuzKg7A+0FneLkCkzIvF2vg5d
JDsWPEYvoinrXLck3+2Nu97qmC4MJdaOiEVnRCvmZmU/Svm7HGwYDkOsrkmL9rID
R+h3SLECgYEA3hnwgKMfkLAutYiB0aR6QNlS1SBpP+jBttwz0KmJKuDmykot2xHx
/6HNQsGkyvSXQZkyw5XIswrlRn4/due49dzh2UusIX0pNCHfqSp9bOVBoTuqOgHa
zmYNRxXhoD5DdEtDGdM/Qm5txClNwv4S8vwt4iW5WFOGfLKhxyOGLwcCgYEAxGwY
Gorc5fakxUpkFdR92badxZ6TMXgNGrmURUF+Df5C5ec0U+MpCBpGgxUD4XDsL0/J
8ktDrejBHFLxqTGpS7++iYdxQGV46kfkAKcrZxxJxWVGHQitA90KyGr0v4ldZgo6
Uab9R7/rPKjdGkoVVxP3iw0Hqq4rGTNeAc2nP4sCgYAIFEl7ZHOxf7czQ1P1nFYW
JdGtjxBFEuJ5FGmOHZyvwp6inTAt1+lFs00UMJceCue1qyz9kGVMngjZF56XZLaF
uxM8JFSOo07sZo8MSE9ntq88fj8i/Q5Ik83H2DPs8Fbj1BkMx3J1qC62BAqgHT3z
ONkycMzdOayavKTF6bTn4QKBgQCjwncqEeHvO/3NmqLs7FbsX2sUaovPb4aFZHlw
cBTXN8ewg11GHxqDbdyhxrCQkSPoof39KrDHWkk+Aw0FgajixX7mjGxoQvFXag52
WOk/sv7yOugEpsoQcYZe54Ub9ztOKnLKxo1d92z5CtQj6eX2zmfQn1FoBINcJE5Y
9Ite1wKBgQC/6NlZSPZyeCQqsLUjxI94P0sSjjocpzOUpU3K+TbIpQQt62BniWHz
wyvlvaOx+b9dhsKPyPAUmNfADZ/iiB8iKFaOs1yrU9MhOa6rRpt66NBvvHWAiusa
GnkF6SbtcCQR+3xc1J+mA1J/VD455PShIJAqOuM2mTdxs8BUR366Yw==
-----END RSA PRIVATE KEY-----`;

(async () => {
  const dataDirectory = await fs.mkdtemp(path.join(os.tmpdir(), 'cpac-egress-ui-'));
  const password = randomBytes(24).toString('hex');
  const upstreamSecret = `synthetic-upstream-${randomBytes(24).toString('hex')}`;
  const proxySecret = `synthetic-proxy-${randomBytes(24).toString('hex')}`;
  const replacementSecret = `synthetic-replacement-${randomBytes(24).toString('hex')}`;
  const secrets = [password, upstreamSecret, proxySecret, replacementSecret];
  let child, browser, logs = '';
  let targetCalls = 0;
  let badProxyConnections = 0;
  const target = http.createServer((_, response) => { targetCalls += 1; response.writeHead(500); response.end(); });
  const badProxy = https.createServer({ cert: badProxyCertificate, key: badProxyKey }, (_, response) => { response.writeHead(500); response.end(); });
  badProxy.on('connection', () => { badProxyConnections += 1; });
  badProxy.on('tlsClientError', () => undefined);
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
    badProxy.listen(0, '127.0.0.1');
    await once(badProxy, 'listening');
    const badProxyPort = badProxy.address().port;
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
    let loseHandshakeResponse = true;
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
      if (/\/admin\/api\/v1\/outbound-proxies\/[^/]+\/tests$/.test(pathname) && request.method() === 'POST') {
        writeRequests.push({ kind: 'handshake', body: request.postDataJSON() });
        if (loseHandshakeResponse) {
          loseHandshakeResponse = false;
          const response = await route.fetch();
          assert.equal(response.status(), 202, 'Lost handshake submission did not commit');
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
    assert.equal(await page.getByRole('button', { name: '仅握手检查', exact: true }).count(), 0, 'Handshake action appeared before a proxy existed');
    await page.getByRole('button', { name: '添加代理', exact: true }).first().click();
    const createDialog = page.getByRole('dialog');
    await createDialog.getByLabel('显示名称', { exact: true }).fill('Synthetic Proxy');
    await createDialog.getByLabel('代理主机', { exact: true }).fill('127.0.0.1');
    await createDialog.getByLabel('端口', { exact: true }).fill(String(badProxyPort));
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

    await page.getByRole('button', { name: '仅握手检查', exact: true }).click();
    const handshakeDialog = page.getByRole('dialog');
    await handshakeDialog.getByText('只建立 HTTPS CONNECT 与目标 TLS，不发送目录、模型请求或上游 API Key。', { exact: true }).waitFor();
    await handshakeDialog.getByText(`代理 r${actual.revision} / 连接 r${actual.connection_revision} / 账号 r${binding.upstream_revision}`, { exact: true }).waitFor();
    await handshakeDialog.getByRole('button', { name: '开始仅握手检查', exact: true }).click();
    await handshakeDialog.getByText('握手检查未能完成', { exact: true }).waitFor({ timeout: 15000 });
    await handshakeDialog.getByText('服务只返回固定故障类别；不会展示代理、证书或远端错误正文。', { exact: true }).waitFor();
    const handshakeWrites = writeRequests.filter(item => item.kind === 'handshake');
    assert.equal(handshakeWrites.length, 1, 'Unknown handshake POST was replayed');
    assert.deepEqual(Object.keys(handshakeWrites[0].body).sort(), ['expected_connection_revision', 'expected_proxy_revision', 'expected_upstream_revision', 'operation_id', 'upstream_id']);
    assert.equal(handshakeWrites[0].body.expected_proxy_revision, actual.revision);
    assert.equal(handshakeWrites[0].body.expected_connection_revision, actual.connection_revision);
    assert.equal(handshakeWrites[0].body.expected_upstream_revision, binding.upstream_revision);
    assert.equal(handshakeWrites[0].body.upstream_id, upstream.id);
    const operationID = handshakeWrites[0].body.operation_id;
    const operation = await api('GET', `/outbound-proxies/${proxyID}/tests/${operationID}`);
    assert.equal(operation.operation_id, operationID);
    assert.equal(operation.result_code, 'internal_failure', 'Bad proxy certificate did not produce the fixed conservative failure');
    assert.ok(visited.some(item => item === `GET /admin/api/v1/outbound-proxies/${proxyID}/tests/${operationID}`), 'Lost handshake response did not query the original operation ID');
    assert.equal(targetCalls, 0, 'Handshake test sent an HTTP request to the selected model target');
    assert.ok(badProxyConnections > 0, 'Handshake test did not reach the synthetic bad-certificate proxy');
    await page.screenshot({ path: path.join(output, 'outbound-proxy-handshake-failure.png'), animations: 'disabled' });
    await handshakeDialog.getByRole('button', { name: '关闭', exact: true }).last().click();
    await handshakeDialog.waitFor({ state: 'detached' });

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
    assert.equal(consoleErrors.filter(message => message.includes('net::ERR_FAILED')).length, 3, 'Response-loss fixtures emitted unexpected console failures');
    assertRedacted(logs);
    assertRedacted(consoleErrors.join('\n'));
    assertRedacted(await page.locator('body').innerText());
    const result = { status: 'PASS', proxies: 1, createRequests: creates.length, patchRequests: writeRequests.filter(item => item.kind === 'patch').length, bindingWrites: writeRequests.filter(item => item.kind === 'binding').length, handshakeRequests: handshakeWrites.length, handshakeResult: operation.result_code, targetCalls, badProxyConnections, pageErrors: 0, expectedConsoleFailures: consoleErrors.length, screenshots: ['outbound-proxy-handshake-failure.png', 'outbound-proxy-desktop.png', 'outbound-proxy-mobile.png'] };
    await fs.writeFile(path.join(output, 'result.json'), JSON.stringify(result, null, 2));
    console.log('PASS: real Go + browser proxy admin and fixed bad-certificate handshake failure, original-operation recovery, zero target HTTP, redaction');
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
    badProxy.closeAllConnections();
    if (badProxy.listening) await new Promise(resolve => badProxy.close(resolve));
    const resolved = path.resolve(dataDirectory);
    assert.ok(path.dirname(resolved) === path.resolve(os.tmpdir()) && path.basename(resolved).startsWith('cpac-egress-ui-'), 'Unexpected cleanup target');
    await fs.rm(resolved, { recursive: true, force: true });
  }
})().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
