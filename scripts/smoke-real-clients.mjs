// Runs unmodified AI CLIs against a real isolated CPA Cloud process and a
// synthetic upstream. This is client compatibility evidence, not provider or
// membership-account evidence.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const name = process.argv[index];
  const value = process.argv[index + 1];
  assert.ok(name?.startsWith('--') && value, `Invalid argument near ${name ?? '<end>'}`);
  args.set(name.slice(2), path.resolve(value));
}
for (const required of ['server', 'codex', 'claude', 'gemini-entry']) {
  assert.ok(args.has(required), `--${required} absolute path is required`);
}

const root = await mkdtemp(path.join(os.tmpdir(), 'cpac-real-clients-'));
const dataDir = path.join(root, 'server-data');
const clientRoot = path.join(root, 'clients');
const workspace = path.join(root, 'workspace');
const password = randomBytes(24).toString('hex');
const upstreamSecrets = {
  openai: `synthetic-openai-${randomUUID()}`,
  claude: `synthetic-claude-${randomUUID()}`,
  gemini: `synthetic-gemini-${randomUUID()}`,
};
const marker = `SYNTHETIC_CPA_OK_${randomUUID().replaceAll('-', '')}`;
const prompt = `Return the exact synthetic marker ${marker}.`;
const fixturePath = path.join(workspace, 'tool-fixture.txt');
const toolFixture = 'SYNTHETIC_TOOL_RESULT_ONLY';
const toolPrompt = `SYNTHETIC_TOOL_ROUND: use the available read-only tool to read ${fixturePath}, then answer.`;
const statusToolPrompt = 'SYNTHETIC_TOOL_ROUND: use the available read-only status tool, then answer.';
const cancelPrompt = 'SYNTHETIC_CANCEL: wait for the response until the client is terminated.';
const requests = [];
const upstreamErrors = [];
let child;
let origin;
let cookie = '';
let csrf = '';
let serverLogs = '';
let employeeKey;
let employeeID;

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

const cancellations = Object.fromEntries(['codex', 'claude', 'gemini'].map(name => [name, {
  started: deferred(), requests: 0, responses: new Set(), modelID: `public-${name}`,
}]));

function holdCancellation(name, res, firstFrame) {
  const state = cancellations[name];
  state.requests++;
  state.responses.add(res);
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache');
  res.flushHeaders();
  res.write(firstFrame);
  state.started.resolve();
  res.once('close', () => state.responses.delete(res));
}

const platform = { os: os.platform(), release: os.release(), arch: os.arch() };

async function sha256File(file) {
  return createHash('sha256').update(await readFile(file)).digest('hex');
}

function boundedVersion(value) {
  return value.replace(/[\r\n]+/g, ' ').replace(/[^\x20-\x7e]/g, '').trim().slice(0, 160);
}

function json(res, value) {
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(value));
}

function responseObject(model = 'upstream-codex') {
  return {
    id: 'resp_synthetic_client', object: 'response', created_at: 1, status: 'completed', model,
    output: [{ id: 'msg_synthetic_client', type: 'message', status: 'completed', role: 'assistant',
      content: [{ type: 'output_text', annotations: [], text: marker }] }],
    usage: { input_tokens: 7, output_tokens: 3, total_tokens: 10 },
  };
}

function writeSSE(res, event, data) {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

const upstream = http.createServer(async (req, res) => {
  try {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks)) : undefined;
    requests.push({ method: req.method, url: req.url, headers: req.headers, body });
    assert.equal(req.headers.cookie, undefined);
    assert.equal(req.headers['x-csrf-token'], undefined);
    assert.equal(req.headers.origin, undefined);

    if (req.method === 'GET' && req.url.startsWith('/v1/models')) {
      assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.openai}`);
      json(res, { object: 'list', data: [{ id: 'upstream-codex', object: 'model' }] });
      return;
    }
    if (req.method === 'GET' && req.url.startsWith('/v1beta/models')) {
      assert.equal(req.headers['x-goog-api-key'], upstreamSecrets.gemini);
      json(res, { models: [{ name: 'models/upstream-gemini', supportedGenerationMethods: ['generateContent'] }] });
      return;
    }
    if (req.method === 'GET' && req.url.startsWith('/v1/models')) return;

    if (req.url.startsWith('/v1/responses')) {
      assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.openai}`);
      assert.equal(body.model, 'upstream-codex');
      const serializedInput = JSON.stringify(body.input ?? []);
      const codexToolRound = serializedInput.includes('SYNTHETIC_TOOL_ROUND');
      const hasFunctionOutput = serializedInput.includes('function_call_output');
      const goalTool = body.tools?.find(tool => tool.name === 'get_goal');
      if (JSON.stringify(body).includes('SYNTHETIC_CANCEL')) {
        const initial = { ...responseObject(), status: 'in_progress', output: [] };
        holdCancellation('codex', res, `event: response.created\ndata: ${JSON.stringify({ type: 'response.created', sequence_number: 0, response: initial })}\n\n`);
        return;
      }
      if (codexToolRound && !hasFunctionOutput && goalTool) {
        assert.equal(body.stream, true, 'Codex CLI tool round was expected to stream');
        const item = { id: 'fc_synthetic_read', type: 'function_call', status: 'completed',
          call_id: 'call_synthetic_read', name: 'get_goal', arguments: '{}' };
        const toolResponse = { ...responseObject(), output: [item] };
        res.setHeader('Content-Type', 'text/event-stream');
        writeSSE(res, 'response.created', { type: 'response.created', sequence_number: 0,
          response: { ...toolResponse, status: 'in_progress', output: [] } });
        writeSSE(res, 'response.output_item.added', { type: 'response.output_item.added', sequence_number: 1,
          output_index: 0, item: { ...item, status: 'in_progress', arguments: '' } });
        writeSSE(res, 'response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta',
          sequence_number: 2, item_id: item.id, output_index: 0, delta: item.arguments });
        writeSSE(res, 'response.function_call_arguments.done', { type: 'response.function_call_arguments.done',
          sequence_number: 3, item_id: item.id, output_index: 0, arguments: item.arguments });
        writeSSE(res, 'response.output_item.done', { type: 'response.output_item.done', sequence_number: 4,
          output_index: 0, item });
        writeSSE(res, 'response.completed', { type: 'response.completed', sequence_number: 5, response: toolResponse });
        res.end();
        return;
      }
      if (!body.stream) {
        json(res, responseObject());
        return;
      }
      res.setHeader('Content-Type', 'text/event-stream');
      const initial = { ...responseObject(), status: 'in_progress', output: [] };
      const message = { id: 'msg_synthetic_client', type: 'message', status: 'in_progress', role: 'assistant', content: [] };
      const part = { type: 'output_text', annotations: [], text: '' };
      const completedMessage = responseObject().output[0];
      writeSSE(res, 'response.created', { type: 'response.created', sequence_number: 0, response: initial });
      writeSSE(res, 'response.output_item.added', { type: 'response.output_item.added', sequence_number: 1,
        output_index: 0, item: message });
      writeSSE(res, 'response.content_part.added', { type: 'response.content_part.added', sequence_number: 2,
        item_id: 'msg_synthetic_client', output_index: 0, content_index: 0, part });
      writeSSE(res, 'response.output_text.delta', { type: 'response.output_text.delta', sequence_number: 3,
        item_id: 'msg_synthetic_client', output_index: 0, content_index: 0, delta: marker });
      writeSSE(res, 'response.output_text.done', { type: 'response.output_text.done', sequence_number: 4,
        item_id: 'msg_synthetic_client', output_index: 0, content_index: 0, text: marker });
      writeSSE(res, 'response.content_part.done', { type: 'response.content_part.done', sequence_number: 5,
        item_id: 'msg_synthetic_client', output_index: 0, content_index: 0,
        part: completedMessage.content[0] });
      writeSSE(res, 'response.output_item.done', { type: 'response.output_item.done', sequence_number: 6,
        output_index: 0, item: completedMessage });
      writeSSE(res, 'response.completed', { type: 'response.completed', sequence_number: 7, response: responseObject() });
      res.end();
      return;
    }

    if (req.url.startsWith('/v1/messages/count_tokens')) {
      assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.claude}`);
      json(res, { input_tokens: 7 });
      return;
    }
    if (req.url.startsWith('/v1/messages')) {
      assert.equal(req.headers.authorization, `Bearer ${upstreamSecrets.claude}`);
      assert.equal(body.model, 'upstream-claude');
      const serializedMessages = JSON.stringify(body.messages ?? []);
      const toolRound = serializedMessages.includes('SYNTHETIC_TOOL_ROUND');
      const hasToolResult = serializedMessages.includes('tool_result');
      const syntheticTool = body.tools?.find(tool => tool.name?.includes('synthetic') && tool.name?.includes('echo'));
      if (serializedMessages.includes('SYNTHETIC_CANCEL')) {
        holdCancellation('claude', res, `event: message_start\ndata: ${JSON.stringify({ type: 'message_start', message: {
          id: 'msg_synthetic_cancel', type: 'message', role: 'assistant', model: 'upstream-claude', content: [],
          stop_reason: null, stop_sequence: null, usage: { input_tokens: 7, output_tokens: 0 } } })}\n\n`);
        return;
      }
      if (toolRound && !hasToolResult && syntheticTool) {
        assert.equal(body.stream, true, 'Claude Code tool round was expected to stream');
        res.setHeader('Content-Type', 'text/event-stream');
        writeSSE(res, 'message_start', { type: 'message_start', message: { id: 'msg_synthetic_tool',
          type: 'message', role: 'assistant', model: 'upstream-claude', content: [], stop_reason: null,
          stop_sequence: null, usage: { input_tokens: 7, output_tokens: 0 } } });
        writeSSE(res, 'content_block_start', { type: 'content_block_start', index: 0,
          content_block: { type: 'tool_use', id: 'toolu_synthetic_echo', name: syntheticTool.name, input: {} } });
        writeSSE(res, 'content_block_delta', { type: 'content_block_delta', index: 0,
          delta: { type: 'input_json_delta', partial_json: JSON.stringify({ value: 'synthetic' }) } });
        writeSSE(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
        writeSSE(res, 'message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use', stop_sequence: null },
          usage: { output_tokens: 3 } });
        writeSSE(res, 'message_stop', { type: 'message_stop' });
        res.end();
        return;
      }
      if (!body.stream) {
        json(res, { id: 'msg_synthetic_client', type: 'message', role: 'assistant', model: 'upstream-claude',
          content: [{ type: 'text', text: marker }], stop_reason: 'end_turn', stop_sequence: null,
          usage: { input_tokens: 7, output_tokens: 3 } });
        return;
      }
      res.setHeader('Content-Type', 'text/event-stream');
      writeSSE(res, 'message_start', { type: 'message_start', message: { id: 'msg_synthetic_client',
        type: 'message', role: 'assistant', model: 'upstream-claude', content: [], stop_reason: null,
        stop_sequence: null, usage: { input_tokens: 7, output_tokens: 0 } } });
      writeSSE(res, 'content_block_start', { type: 'content_block_start', index: 0,
        content_block: { type: 'text', text: '' } });
      writeSSE(res, 'content_block_delta', { type: 'content_block_delta', index: 0,
        delta: { type: 'text_delta', text: marker } });
      writeSSE(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
      writeSSE(res, 'message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null },
        usage: { output_tokens: 3 } });
      writeSSE(res, 'message_stop', { type: 'message_stop' });
      res.end();
      return;
    }

    if (req.url.includes(':streamGenerateContent') || req.url.includes(':generateContent')) {
      assert.equal(req.headers['x-goog-api-key'], upstreamSecrets.gemini);
      assert.equal(req.url.includes('/models/upstream-gemini:'), true);
      if (JSON.stringify(body).includes('SYNTHETIC_CANCEL')) {
        holdCancellation('gemini', res, 'data: {"candidates":[{"content":{"role":"model","parts":[{"text":"synthetic partial"}]},"index":0}]}\n\n');
        return;
      }
      const serializedContents = JSON.stringify(body.contents ?? []);
      const geminiToolRound = serializedContents.includes('SYNTHETIC_TOOL_ROUND');
      const hasFunctionResponse = serializedContents.includes('functionResponse');
      const declarations = (body.tools ?? []).flatMap(tool => tool.functionDeclarations ?? []);
      const readFileTool = declarations.find(declaration => declaration.name === 'read_file');
      if (geminiToolRound && !hasFunctionResponse && readFileTool) {
        const toolCall = { candidates: [{ content: { role: 'model', parts: [{ functionCall: {
          name: 'read_file', args: { file_path: fixturePath },
        } }] }, finishReason: 'STOP', index: 0 }], usageMetadata: { promptTokenCount: 7,
          candidatesTokenCount: 3, totalTokenCount: 10 } };
        if (req.url.includes(':streamGenerateContent')) {
          res.setHeader('Content-Type', 'text/event-stream');
          res.write(`data: ${JSON.stringify(toolCall)}\n\n`);
          res.end();
        } else json(res, toolCall);
        return;
      }
      const value = { candidates: [{ content: { role: 'model', parts: [{ text: marker }] },
        finishReason: 'STOP', index: 0 }], usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 3,
        totalTokenCount: 10 } };
      if (req.url.includes(':streamGenerateContent')) {
        res.setHeader('Content-Type', 'text/event-stream');
        res.write(`data: ${JSON.stringify(value)}\n\n`);
        res.end();
      } else json(res, value);
      return;
    }
    res.writeHead(404);
    res.end();
  } catch (error) {
    upstreamErrors.push(error.stack ?? error.message);
    if (!res.headersSent) res.writeHead(500);
    res.end();
  }
});

function launchServer(extra) {
  const proc = spawn(args.get('server'), ['--data-dir', dataDir, ...extra], {
    stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true,
  });
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => {
    serverLogs += chunk;
    if (serverLogs.length > (1 << 20)) proc.kill();
  });
  return proc;
}

async function stopServer() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = once(child, 'exit');
    child.stdin.end();
    const timer = setTimeout(() => child.kill(), 5000);
    try { await ended; } finally { clearTimeout(timer); }
  }
}

async function startServer(reuseAddress = false) {
  let port;
  if (reuseAddress) {
    assert.ok(origin, 'Cannot reuse an address before first startup');
    port = Number(new URL(origin).port);
  } else {
    const probe = http.createServer();
    probe.listen(0, '127.0.0.1');
    await once(probe, 'listening');
    port = probe.address().port;
    await new Promise(resolve => probe.close(resolve));
    origin = `http://127.0.0.1:${port}`;
  }
  cookie = '';
  csrf = '';
  child = launchServer(['--listen', `127.0.0.1:${port}`, '--allow-loopback-upstream', '--shutdown-on-stdin-eof']);
  for (let attempt = 0; attempt < 100; attempt++) {
    if (child.exitCode !== null) throw new Error(`CPA Cloud exited during startup: ${serverLogs}`);
    try {
      if ((await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(500) })).ok) return;
    } catch { /* wait */ }
    await delay(100);
  }
  throw new Error('CPA Cloud readiness timeout');
}

async function admin(route, method = 'GET', body) {
  const response = await fetch(`${origin}/admin/api/v1${route}`, {
    method,
    headers: { Cookie: cookie, Origin: origin, 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(5000),
  });
  const text = await response.text();
  assert.ok(response.ok, `Admin ${method} ${route}: HTTP ${response.status} ${text}`);
  const setCookie = response.headers.get('set-cookie');
  if (setCookie) cookie = setCookie.split(';')[0];
  return JSON.parse(text);
}

function isolatedEnvironment(home, extra = {}) {
  const appData = path.join(home, 'AppData', 'Roaming');
  const localAppData = path.join(home, 'AppData', 'Local');
  return {
    SystemRoot: process.env.SystemRoot,
    WINDIR: process.env.WINDIR,
    ComSpec: process.env.ComSpec,
    PATH: process.env.PATH,
    PATHEXT: process.env.PATHEXT,
    OS: process.env.OS,
    PROCESSOR_ARCHITECTURE: process.env.PROCESSOR_ARCHITECTURE,
    NUMBER_OF_PROCESSORS: process.env.NUMBER_OF_PROCESSORS,
    HOME: home,
    USERPROFILE: home,
    APPDATA: appData,
    LOCALAPPDATA: localAppData,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    TEMP: path.join(home, 'tmp'),
    TMP: path.join(home, 'tmp'),
    NO_COLOR: '1',
    CI: '1',
    ...extra,
  };
}

async function run(executable, clientArgs, options) {
  const proc = spawn(executable, clientArgs, { cwd: workspace, env: options.env, windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '';
  let stderr = '';
  for (const [stream, append] of [[proc.stdout, value => { stdout += value; }], [proc.stderr, value => { stderr += value; }]]) {
    stream.on('data', chunk => {
      append(chunk.toString('utf8'));
      if (stdout.length + stderr.length > (2 << 20)) proc.kill();
    });
  }
  const timeout = setTimeout(() => proc.kill(), options.timeout ?? 45000);
  const [code, signal] = await once(proc, 'exit');
  clearTimeout(timeout);
  return { code, signal, stdout, stderr };
}

async function runAndCancel(executable, clientArgs, env, state) {
  const beforeUsage = await usageRows();
  const proc = spawn(executable, clientArgs, { cwd: workspace, env, windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'] });
  let bytes = 0;
  for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => {
    bytes += chunk.length;
    if (bytes > (1 << 20)) proc.kill();
  });
  const exit = once(proc, 'exit');
  await Promise.race([state.started.promise, delay(15000).then(() => { throw new Error('Cancellation dispatch timeout'); })]);
  assert.equal(state.requests, 1, 'Cancellation scenario dispatched more than once before termination');
  if (process.platform === 'win32') {
    const killer = spawn('taskkill.exe', ['/PID', String(proc.pid), '/T', '/F'], {
      stdio: 'ignore', windowsHide: true,
    });
    const [killerCode] = await once(killer, 'exit');
    assert.equal(killerCode, 0, `Client process tree could not be terminated (pid=${proc.pid})`);
  } else {
    assert.equal(proc.kill('SIGTERM'), true, 'Client process could not be terminated for cancellation test');
  }
  await Promise.race([exit, delay(10000).then(() => { throw new Error('Cancelled client did not exit'); })]);
  const deadline = Date.now() + 10000;
  while (state.responses.size > 0 && Date.now() < deadline) await delay(100);
  const aborted = state.responses.size === 0;
  const requestsBeforeCleanup = state.requests;
  const activeBeforeCleanup = state.responses.size;
  const tcpOwnerPids = activeBeforeCleanup > 0 ? await upstreamConnectionOwners() : [];
  const previousIDs = new Set(beforeUsage.map(row => row.id));
  let afterUsage = [];
  let newUsage = [];
  let previousSignature = '';
  let stableReads = 0;
  const usageDeadline = Date.now() + 5000;
  while (Date.now() < usageDeadline) {
    afterUsage = await usageRows();
    newUsage = afterUsage.filter(row => !previousIDs.has(row.id) && row.model_id === state.modelID);
    const signature = JSON.stringify(newUsage.map(row => [row.id, row.status, row.attempt_count]));
    stableReads = signature && signature === previousSignature ? stableReads + 1 : 0;
    if (stableReads >= 2) break;
    previousSignature = signature;
    await delay(250);
  }
  const employeeRequests = newUsage.length;
  const upstreamAttempts = newUsage.reduce((total, row) => total + Number(row.attempt_count), 0);
  if (!aborted) {
    for (const response of state.responses) response.destroy();
    await delay(250);
  }
  return { aborted, requestsBeforeCleanup, activeBeforeCleanup, employeeRequests, upstreamAttempts,
    tcpOwnerPids, cpaPid: child.pid, terminatedClientPid: proc.pid,
    usageBeforeCount: beforeUsage.length, usageAfterCount: afterUsage.length,
    usageNewRows: newUsage.map(row => ({ provider: row.provider, model_id: row.model_id,
      status: row.status, attempt_count: row.attempt_count })) };
}

async function usageRows() {
  assert.ok(employeeID, 'Employee must exist before reading usage evidence');
  const page = await admin(`/usage/requests?employee_id=${encodeURIComponent(employeeID)}&limit=100`);
  assert.equal(page.next_cursor, null, 'Usage evidence exceeded the bounded single page');
  return page.items;
}

async function upstreamConnectionOwners() {
  if (process.platform !== 'win32') return [];
  const port = upstream.address().port;
  const query = `Get-NetTCPConnection -State Established -RemotePort ${port} -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess`;
  const result = await run('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', query], {
    env: process.env, timeout: 10000,
  });
  assert.equal(result.code, 0, 'Unable to read Windows TCP process ownership');
  return [...new Set(result.stdout.split(/\s+/).filter(Boolean).map(Number).filter(Number.isSafeInteger))].sort((a, b) => a - b);
}

function cancellationResult(client, version, evidence) {
  const pass = evidence.aborted && evidence.requestsBeforeCleanup === 1 && evidence.employeeRequests === 1 &&
    evidence.upstreamAttempts === 1;
  let reason;
  if (!evidence.aborted) reason = 'upstream_response_open_after_process_tree_termination';
  else if (evidence.employeeRequests > 1) reason = 'multiple_employee_requests_observed';
  else if (evidence.upstreamAttempts > evidence.employeeRequests) reason = 'multiple_upstream_attempts_in_one_employee_request';
  else if (evidence.requestsBeforeCleanup !== 1) reason = 'multiple_upstream_requests_observed';
  return { client, version, scenario: 'client_cancel_upstream_abort', status: pass ? 'PASS' : 'FAIL',
    upstream_requests: evidence.requestsBeforeCleanup, employee_requests: evidence.employeeRequests,
    upstream_attempts: evidence.upstreamAttempts, active_upstream_responses: evidence.activeBeforeCleanup,
    usage_before_count: evidence.usageBeforeCount, usage_after_count: evidence.usageAfterCount,
    usage_new_rows: evidence.usageNewRows,
    tcp_owner_pids: evidence.tcpOwnerPids, cpa_pid_owned_connection: evidence.tcpOwnerPids.includes(evidence.cpaPid),
    terminated_client_pid_owned_connection: evidence.tcpOwnerPids.includes(evidence.terminatedClientPid),
    ...(pass ? {} : { reason_code: reason }) };
}

function assertClientSuccess(name, result, requiredMarker = marker) {
  assert.equal(result.code, 0, `${name} exited unsuccessfully (code=${result.code}, signal=${result.signal ?? 'none'})`);
  if (requiredMarker) assert.ok(result.stdout.includes(requiredMarker), `${name} did not render the synthetic marker`);
}

async function clientIdentity(name, executable, versionArgs, env, entry) {
  const result = await run(executable, versionArgs, { env, timeout: 15000 });
  assert.equal(result.code, 0, `${name} version command failed (code=${result.code})`);
  const raw = boundedVersion(result.stdout || result.stderr);
  assert.ok(raw, `${name} returned an empty version`);
  const hashedPath = entry ?? executable;
  return { name, version: raw, executable: path.basename(hashedPath), sha256: await sha256File(hashedPath), ...platform };
}

async function prepareHome(name) {
  const home = path.join(clientRoot, name);
  await mkdir(path.join(home, 'AppData', 'Roaming'), { recursive: true });
  await mkdir(path.join(home, 'AppData', 'Local'), { recursive: true });
  await mkdir(path.join(home, 'tmp'), { recursive: true });
  return home;
}

function summarizeRequest(client, match) {
  const request = requests.findLast(match);
  assert.ok(request, `${client} did not reach the synthetic upstream`);
  const fields = Object.keys(request.body ?? {}).sort();
  const toolNames = (request.body?.tools ?? []).flatMap(tool => tool.name ? [tool.name] :
    (tool.functionDeclarations ?? []).map(declaration => declaration.name)).sort();
  return { client, method: request.method, path: request.url.split('?')[0], request_fields: fields,
    tool_names: toolNames, stream: request.body?.stream ?? request.url.includes(':streamGenerateContent') };
}

const results = [];
let identities;
let serverIdentity;
try {
  await mkdir(workspace, { recursive: true });
  await writeFile(fixturePath, `${toolFixture}\n`, 'utf8');
  const mcpServerPath = path.join(workspace, 'synthetic-mcp.mjs');
  const mcpConfigPath = path.join(workspace, 'synthetic-mcp.json');
  await writeFile(mcpServerPath, `import readline from 'node:readline';
const lines = readline.createInterface({ input: process.stdin });
function send(id, result) { process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id, result }) + '\\n'); }
lines.on('line', line => {
  const message = JSON.parse(line);
  if (message.method === 'initialize') send(message.id, { protocolVersion: message.params.protocolVersion,
    capabilities: { tools: {} }, serverInfo: { name: 'synthetic', version: '1.0.0' } });
  else if (message.method === 'ping') send(message.id, {});
  else if (message.method === 'tools/list') send(message.id, { tools: [{ name: 'echo',
    description: 'Return a fixed synthetic fixture without side effects.',
    inputSchema: { type: 'object', properties: { value: { type: 'string' } }, required: ['value'] } }] });
  else if (message.method === 'tools/call') send(message.id, { content: [{ type: 'text', text: 'SYNTHETIC_TOOL_RESULT_ONLY' }], isError: false });
});
`, 'utf8');
  await writeFile(mcpConfigPath, JSON.stringify({ mcpServers: { synthetic: { command: process.execPath,
    args: [mcpServerPath] } } }), 'utf8');
  const codexHome = await prepareHome('codex');
  const claudeHome = await prepareHome('claude');
  const geminiHome = await prepareHome('gemini');
  identities = {
    codex: await clientIdentity('Codex CLI', args.get('codex'), ['--version'], isolatedEnvironment(codexHome), args.get('codex')),
    claude: await clientIdentity('Claude Code', args.get('claude'), ['--version'], isolatedEnvironment(claudeHome), args.get('claude')),
    gemini: await clientIdentity('Gemini CLI', process.execPath, [args.get('gemini-entry'), '--version'], isolatedEnvironment(geminiHome), args.get('gemini-entry')),
  };
  serverIdentity = { executable: path.basename(args.get('server')), sha256: await sha256File(args.get('server')), ...platform };
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  const upstreamOrigin = `http://127.0.0.1:${upstream.address().port}`;

  const init = launchServer(['--init']);
  const initialized = once(init, 'exit');
  init.stdin.end(`${password}\n`);
  assert.equal((await initialized)[0], 0, 'CPA Cloud initialization failed');
  await startServer();
  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  serverIdentity.version = (await admin('/system/status')).version;

  for (const spec of [
    ['codex', 'openai-compatible', upstreamSecrets.openai, 'upstream-codex'],
    ['claude', 'anthropic-api-key', upstreamSecrets.claude, 'upstream-claude'],
    ['gemini', 'gemini-api-key', upstreamSecrets.gemini, 'upstream-gemini'],
  ]) {
    const [name, provider_kind, api_key, upstream_model] = spec;
    const account = await admin('/upstreams', 'POST', { name, provider_kind, endpoint: upstreamOrigin, api_key });
    await admin('/models', 'POST', { id: `public-${name}`, upstream_id: account.id, upstream_model });
  }
  const employee = await admin('/employees', 'POST', { name: 'real client synthetic acceptance' });
  employeeID = employee.id;
  employeeKey = await admin(`/employees/${employee.id}/keys`, 'POST', { name: 'client harness', operation_id: randomUUID() });

  await mkdir(path.join(codexHome, '.codex'), { recursive: true });
  await writeFile(path.join(codexHome, '.codex', 'config.toml'), `model = "public-codex"\nmodel_provider = "cpacloud"\napproval_policy = "never"\nsandbox_mode = "read-only"\ncheck_for_update_on_startup = false\n\n[analytics]\nenabled = false\n\n[features]\napps = false\nplugins = false\nremote_plugin = false\nplugin_sharing = false\nrecommended_plugins = false\nskill_search = false\n\n[model_providers.cpacloud]\nname = "CPA Cloud synthetic"\nbase_url = "${origin}/v1"\nenv_key = "CPA_SYNTHETIC_KEY"\nwire_api = "responses"\nrequest_max_retries = 0\nstream_max_retries = 0\n`, 'utf8');
  const codex = await run(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', 'public-codex', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', prompt], {
    env: isolatedEnvironment(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_SYNTHETIC_KEY: employeeKey.key }),
  });
  assertClientSuccess('Codex CLI', codex);
  results.push({ client: 'Codex CLI', version: identities.codex.version, scenario: 'text_sse', status: 'PASS',
    ...summarizeRequest('Codex CLI', value => value.url.startsWith('/v1/responses')) });

  const claude = await run(args.get('claude'), ['--bare', '--print', '--output-format', 'stream-json',
    '--verbose', '--include-partial-messages', '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk',
    '--permission-prompts', 'none', '--tools', '', '--model', 'public-claude', prompt], {
    env: isolatedEnvironment(claudeHome, { CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'),
      ANTHROPIC_API_KEY: employeeKey.key, ANTHROPIC_BASE_URL: origin, CLAUDE_CODE_MAX_RETRIES: '0',
      DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' }),
  });
  assertClientSuccess('Claude Code', claude);
  results.push({ client: 'Claude Code', version: identities.claude.version, scenario: 'text_sse', status: 'PASS',
    ...summarizeRequest('Claude Code', value => value.url.startsWith('/v1/messages')) });

  const beforeClaudeTool = requests.length;
  const claudeTool = await run(args.get('claude'), ['--bare', '--print', '--output-format', 'stream-json',
    '--verbose', '--include-partial-messages', '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk',
    '--permission-prompts', 'none', '--mcp-config', mcpConfigPath, '--strict-mcp-config',
    '--allowedTools', 'mcp__synthetic__echo', '--tools', 'mcp__synthetic__echo', '--model', 'public-claude', toolPrompt], {
    env: isolatedEnvironment(claudeHome, { CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'),
      ANTHROPIC_API_KEY: employeeKey.key, ANTHROPIC_BASE_URL: origin, CLAUDE_CODE_MAX_RETRIES: '0',
      DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' }),
  });
  assertClientSuccess('Claude Code tool round', claudeTool);
  const toolRequests = requests.slice(beforeClaudeTool).filter(value => value.url.startsWith('/v1/messages') &&
    !value.url.startsWith('/v1/messages/count_tokens'));
  const toolSignatures = toolRequests.map(value => ({ max_tokens: value.body?.max_tokens,
    tool_names: (value.body?.tools ?? []).map(tool => tool.name),
    messages: (value.body?.messages ?? []).map(message => ({ role: message.role,
      types: Array.isArray(message.content) ? message.content.map(block => block.type) : [typeof message.content],
      lengths: Array.isArray(message.content) ? message.content.map(block => typeof block.text === 'string' ? block.text.length : undefined) : [] })) }));
  const auxiliary = toolRequests.filter(value => (value.body?.tools ?? []).length === 0);
  const toolConversation = toolRequests.filter(value => (value.body?.tools ?? []).some(tool => tool.name === 'mcp__synthetic__echo'));
  assert.equal(toolRequests.length, 3,
    `Claude Code tool round request count changed; signatures=${JSON.stringify(toolSignatures)}`);
  assert.equal(auxiliary.length, 1, 'Claude Code tool round must have exactly one no-tool auxiliary request');
  assert.equal(toolConversation.length, 2, 'Claude Code tool conversation must have exactly two tool-bearing requests');
  assert.ok(toolRequests.some(value => JSON.stringify(value.body?.messages ?? []).includes('tool_result')),
    'Claude Code follow-up omitted tool_result');
  results.push({ client: 'Claude Code', version: identities.claude.version, scenario: 'tool_call_result_round',
    status: 'PASS', upstream_requests: toolRequests.length, auxiliary_requests: auxiliary.length,
    tool_conversation_requests: toolConversation.length });

  const beforeCodexTool = requests.length;
  const codexTool = await run(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', 'public-codex', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', statusToolPrompt], {
    env: isolatedEnvironment(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_SYNTHETIC_KEY: employeeKey.key }),
  });
  assertClientSuccess('Codex CLI tool round', codexTool);
  const codexToolRequests = requests.slice(beforeCodexTool).filter(value => value.url.startsWith('/v1/responses'));
  assert.equal(codexToolRequests.length, 2, 'Codex CLI tool round must use exactly two Responses requests');
  assert.ok(JSON.stringify(codexToolRequests[1].body?.input ?? []).includes('function_call_output'),
    'Codex CLI follow-up omitted function_call_output');
  assert.ok(!JSON.stringify(codexToolRequests[1].body?.input ?? []).includes('failed:'),
    'Codex CLI read-only status tool failed');
  results.push({ client: 'Codex CLI', version: identities.codex.version, scenario: 'tool_call_result_round',
    status: 'PASS', upstream_requests: codexToolRequests.length, tool: 'get_goal_read_only' });

  await mkdir(path.join(geminiHome, '.gemini'), { recursive: true });
  await writeFile(path.join(geminiHome, '.gemini', 'settings.json'), JSON.stringify({ security: { auth: {
    selectedType: 'gemini-api-key' } }, telemetry: { enabled: false } }), 'utf8');
  const node = process.execPath;
  const beforeGemini = requests.length;
  const gemini = await run(node, [args.get('gemini-entry'), '--prompt', prompt, '--model', 'public-gemini',
    '--approval-mode', 'plan', '--output-format', 'stream-json', '--skip-trust'], {
    env: isolatedEnvironment(geminiHome, { GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey.key,
      GOOGLE_GEMINI_BASE_URL: origin, GEMINI_TELEMETRY_ENABLED: '0' }),
  });
  const geminiPassed = gemini.code === 0 && gemini.stdout.includes(marker);
  let geminiFailureReason;
  if (geminiPassed) {
    results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'text_sse', status: 'PASS',
      ...summarizeRequest('Gemini CLI', value => value.url.includes(':streamGenerateContent')) });

    const beforeGeminiTool = requests.length;
    const geminiTool = await run(node, [args.get('gemini-entry'), '--prompt', toolPrompt, '--model', 'public-gemini',
      '--approval-mode', 'plan', '--output-format', 'stream-json', '--skip-trust'], {
      env: isolatedEnvironment(geminiHome, { GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey.key,
        GOOGLE_GEMINI_BASE_URL: origin, GEMINI_TELEMETRY_ENABLED: '0' }),
    });
    const geminiToolRequests = requests.slice(beforeGeminiTool).filter(value =>
      value.url.includes(':streamGenerateContent') || value.url.includes(':generateContent'));
    if (geminiTool.code === 0 && geminiTool.stdout.includes(marker)) {
      assert.equal(geminiToolRequests.length, 2, 'Gemini CLI tool round must use exactly two generation requests');
      assert.ok(JSON.stringify(geminiToolRequests[1].body?.contents ?? []).includes('functionResponse'),
        'Gemini CLI follow-up omitted functionResponse');
      assert.ok(JSON.stringify(geminiToolRequests[1].body?.contents ?? []).includes(toolFixture),
        'Gemini CLI follow-up omitted the synthetic tool result');
      results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'tool_call_result_round',
        status: 'PASS', upstream_requests: geminiToolRequests.length, tool: 'read_file' });
    } else {
      assert.equal(geminiTool.code, 400, `Gemini CLI tool round failed for an unclassified reason (code=${geminiTool.code})`);
      assert.equal(geminiToolRequests.length, 1, 'Rejected Gemini tool follow-up unexpectedly reached the upstream');
      results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'tool_call_result_round',
        status: 'FAIL', upstream_requests: geminiToolRequests.length, tool: 'read_file', tool_executed: true,
        reason_code: 'unsupported_contents_part_thought_signature' });
    }
  } else {
    const wireOutput = `${gemini.stdout}\n${gemini.stderr}`;
    const unsupportedFields = wireOutput.includes('UNIMPLEMENTED');
    const unsupportedAuth = gemini.code === 401 || wireOutput.includes('UNAUTHENTICATED');
    assert.ok(unsupportedFields || unsupportedAuth,
      `Gemini CLI failed for an unclassified reason (code=${gemini.code}, signal=${gemini.signal ?? 'none'})`);
    assert.equal(requests.length, beforeGemini, 'Rejected Gemini request unexpectedly reached the upstream');
    geminiFailureReason = unsupportedFields ? 'unsupported_request_fields' : 'unsupported_client_auth_header';
    results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'text_sse', status: 'FAIL',
      reason_code: geminiFailureReason,
      fields: unsupportedFields ? ['generationConfig.thinkingConfig', 'parametersJsonSchema'] : ['x-goog-api-key'],
      upstream_requests: 0 });
  }

  const codexCancelled = await runAndCancel(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', 'public-codex', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', cancelPrompt],
  isolatedEnvironment(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_SYNTHETIC_KEY: employeeKey.key }),
  cancellations.codex);
  results.push(cancellationResult('Codex CLI', identities.codex.version, codexCancelled));

  const claudeCancelled = await runAndCancel(args.get('claude'), ['--bare', '--print', '--output-format', 'stream-json', '--verbose',
    '--include-partial-messages', '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk',
    '--permission-prompts', 'none', '--tools', '', '--model', 'public-claude', cancelPrompt],
  isolatedEnvironment(claudeHome, { CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'),
    ANTHROPIC_API_KEY: employeeKey.key, ANTHROPIC_BASE_URL: origin, CLAUDE_CODE_MAX_RETRIES: '0',
    DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' }), cancellations.claude);
  results.push(cancellationResult('Claude Code', identities.claude.version, claudeCancelled));

  if (geminiPassed) {
    const geminiCancelled = await runAndCancel(node, [args.get('gemini-entry'), '--prompt', cancelPrompt, '--model', 'public-gemini',
      '--approval-mode', 'plan', '--output-format', 'stream-json', '--skip-trust'],
    isolatedEnvironment(geminiHome, { GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey.key,
      GOOGLE_GEMINI_BASE_URL: origin, GEMINI_TELEMETRY_ENABLED: '0' }), cancellations.gemini);
    results.push(cancellationResult('Gemini CLI', identities.gemini.version, geminiCancelled));
  }

  await stopServer();
  await startServer(true);
  const responseRequestCount = () => requests.filter(value => value.url.startsWith('/v1/responses')).length;
  const messageRequestCount = () => requests.filter(value => value.url.startsWith('/v1/messages') &&
    !value.url.startsWith('/v1/messages/count_tokens')).length;
  const geminiRequestCount = () => requests.filter(value => value.url.includes(':streamGenerateContent') ||
    value.url.includes(':generateContent')).length;
  const beforeRestartResponses = responseRequestCount();
  const beforeRestartMessages = messageRequestCount();
  const beforeRestartGemini = geminiRequestCount();
  const restartedCodex = await run(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', 'public-codex', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', prompt], {
    env: isolatedEnvironment(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_SYNTHETIC_KEY: employeeKey.key }),
  });
  assertClientSuccess('Codex CLI after restart', restartedCodex);
  const restartedClaude = await run(args.get('claude'), ['--bare', '--print', '--output-format', 'json',
    '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-mode', 'dontAsk', '--permission-prompts', 'none', '--tools', '',
    '--model', 'public-claude', prompt], {
    env: isolatedEnvironment(claudeHome, { CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'),
      ANTHROPIC_API_KEY: employeeKey.key, ANTHROPIC_BASE_URL: origin, CLAUDE_CODE_MAX_RETRIES: '0',
      DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' }),
  });
  assertClientSuccess('Claude Code after restart', restartedClaude);
  assert.equal(responseRequestCount(), beforeRestartResponses + 1, 'Codex restart did not produce exactly one Responses request');
  const restartMessages = requests.filter(value => value.url.startsWith('/v1/messages') &&
    !value.url.startsWith('/v1/messages/count_tokens')).slice(beforeRestartMessages);
  const restartSignatures = restartMessages.map(value => ({ tools: (value.body?.tools ?? []).length,
    message_count: (value.body?.messages ?? []).length,
    content_lengths: (value.body?.messages ?? []).flatMap(message => Array.isArray(message.content) ?
      message.content.map(block => typeof block.text === 'string' ? block.text.length : undefined) : []) }));
  assert.equal(restartMessages.length, 2,
    `Claude restart request count changed; signatures=${JSON.stringify(restartSignatures)}`);
  if (geminiPassed) {
    const restartedGemini = await run(node, [args.get('gemini-entry'), '--prompt', prompt, '--model', 'public-gemini',
      '--approval-mode', 'plan', '--output-format', 'stream-json', '--skip-trust'], {
      env: isolatedEnvironment(geminiHome, { GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey.key,
        GOOGLE_GEMINI_BASE_URL: origin, GEMINI_TELEMETRY_ENABLED: '0' }),
    });
    assertClientSuccess('Gemini CLI after restart', restartedGemini);
    assert.equal(geminiRequestCount(), beforeRestartGemini + 1,
      'Gemini restart produced an unexpected auxiliary or duplicate generation request');
    results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'server_restart_existing_key', status: 'PASS' });
  }
  results.push({ client: 'Codex CLI', version: identities.codex.version, scenario: 'server_restart_existing_key', status: 'PASS' });
  results.push({ client: 'Claude Code', version: identities.claude.version, scenario: 'server_restart_existing_key', status: 'PASS' });

  csrf = (await admin('/sessions', 'POST', { username: 'admin', password })).csrf_token;
  await admin(`/keys/${employeeKey.id}/revoke`, 'POST', {});
  const beforeRevoked = requests.length;
  const revokedCodex = await run(args.get('codex'), ['--no-daemon', '--strict-config', '-s', 'read-only', '-a', 'never',
    '-m', 'public-codex', 'exec', '--ephemeral', '--ignore-rules', '--skip-git-repo-check', '--json', prompt], {
    env: isolatedEnvironment(codexHome, { CODEX_HOME: path.join(codexHome, '.codex'), CPA_SYNTHETIC_KEY: employeeKey.key }),
    timeout: 15000,
  });
  assert.notEqual(revokedCodex.code, 0, 'Codex CLI unexpectedly succeeded after employee Key revocation');
  const revokedClaude = await run(args.get('claude'), ['--bare', '--print', '--output-format', 'json',
    '--no-session-persistence', '--prompt-suggestions', 'false', '--permission-prompts', 'none', '--tools', '', '--model', 'public-claude', prompt], {
    env: isolatedEnvironment(claudeHome, { CLAUDE_CONFIG_DIR: path.join(claudeHome, '.claude'),
      ANTHROPIC_API_KEY: employeeKey.key, ANTHROPIC_BASE_URL: origin, CLAUDE_CODE_MAX_RETRIES: '0',
      DISABLE_TELEMETRY: '1', DISABLE_ERROR_REPORTING: '1', DISABLE_AUTOUPDATER: '1' }),
    timeout: 15000,
  });
  assert.notEqual(revokedClaude.code, 0, 'Claude Code unexpectedly succeeded after employee Key revocation');
  if (geminiPassed) {
    const revokedGemini = await run(node, [args.get('gemini-entry'), '--prompt', prompt, '--model', 'public-gemini',
      '--approval-mode', 'plan', '--output-format', 'json', '--skip-trust'], {
      env: isolatedEnvironment(geminiHome, { GEMINI_CLI_HOME: geminiHome, GEMINI_API_KEY: employeeKey.key,
        GOOGLE_GEMINI_BASE_URL: origin, GEMINI_TELEMETRY_ENABLED: '0' }), timeout: 15000,
    });
    assert.notEqual(revokedGemini.code, 0, 'Gemini CLI unexpectedly succeeded after employee Key revocation');
    results.push({ client: 'Gemini CLI', version: identities.gemini.version, scenario: 'revoked_key_zero_dispatch', status: 'PASS' });
  }
  assert.equal(requests.length, beforeRevoked, 'Revoked employee Key reached the upstream');
  results.push({ client: 'Codex CLI', version: identities.codex.version, scenario: 'revoked_key_zero_dispatch', status: 'PASS' });
  results.push({ client: 'Claude Code', version: identities.claude.version, scenario: 'revoked_key_zero_dispatch', status: 'PASS' });

  if (!geminiPassed) {
    results.push(
      { client: 'Gemini CLI', version: identities.gemini.version, scenario: 'tool_call_result_round', status: 'SKIP', reason_code: `blocked_by_${geminiFailureReason}` },
      { client: 'Gemini CLI', version: identities.gemini.version, scenario: 'client_cancel_upstream_abort', status: 'SKIP', reason_code: `blocked_by_${geminiFailureReason}` },
      { client: 'Gemini CLI', version: identities.gemini.version, scenario: 'server_restart_existing_key', status: 'SKIP', reason_code: `blocked_by_${geminiFailureReason}` },
      { client: 'Gemini CLI', version: identities.gemini.version, scenario: 'revoked_key_zero_dispatch', status: 'SKIP', reason_code: `blocked_by_${geminiFailureReason}` },
    );
  }

  assert.deepEqual(upstreamErrors, []);
  const upstreamWire = JSON.stringify(requests);
  assert.ok(!upstreamWire.includes(employeeKey.key), 'Employee Key was forwarded to the synthetic upstream');
  await stopServer();
  const sensitive = [password, employeeKey.key, prompt, ...Object.values(upstreamSecrets)];
  assert.ok(!sensitive.some(value => serverLogs.includes(value)), 'CPA Cloud logs contain a synthetic secret or prompt');
  for (const entry of await readdir(dataDir, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const bytes = await readFile(path.join(dataDir, entry.name));
    assert.ok(!sensitive.some(value => bytes.includes(Buffer.from(value))), `Plaintext secret/content persisted in ${entry.name}`);
  }
  const status = results.some(result => result.status === 'FAIL') ? 'FAIL' : 'PASS';
  console.log(JSON.stringify({ status, boundary: 'real_clients_synthetic_upstream', platform,
    server: serverIdentity, clients: identities, results }, null, 2));
  if (status === 'FAIL') process.exitCode = 1;
} finally {
  await stopServer();
  upstream.closeAllConnections();
  if (upstream.listening) await new Promise(resolve => upstream.close(resolve));
  const resolved = path.resolve(root);
  assert.equal(path.dirname(resolved), path.resolve(os.tmpdir()));
  assert.ok(path.basename(resolved).startsWith('cpac-real-clients-'));
  let cleanupError;
  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      await rm(resolved, { recursive: true, force: true, maxRetries: 2, retryDelay: 250 });
      cleanupError = undefined;
      break;
    } catch (error) {
      cleanupError = error;
      if (error.code !== 'EBUSY' && error.code !== 'EPERM') throw error;
      await delay(500);
    }
  }
  if (cleanupError) throw cleanupError;
  assert.rejects(readFile(resolved), { code: 'ENOENT' });
}
