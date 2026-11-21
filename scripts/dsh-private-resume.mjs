// Run under the installed DeepSeek Harness Electron with ELECTRON_RUN_AS_NODE=1.
// Usage: ... dsh-private-resume.mjs <action-uuid> <private-transport-config.json>
// Test pipe: ... dsh-private-resume.mjs <action-uuid> --private-pipe
// The test pipe takes one transport JSON line followed by one code line.
import { writeSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { createInterface, emitKeypressEvents } from 'node:readline';
import { pathToFileURL } from 'node:url';

const metadataKey = 'mcpguard.dev/authorization-code';
const toolName = 'action.resume';
const protocolVersion = '2025-11-25';
const connectTimeout = 16000;
const cleanupTimeout = 5000;
const privatePipeTimeout = 27000;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const codeFormat = /^[A-Za-z0-9_-]{22}$/;
const safeLabels = /^[A-Z][A-Z0-9_]{0,63}$/;

async function packageInfo(modules, name) {
  const raw = await readFile(path.join(modules, name, 'package.json'), 'utf8');
  return JSON.parse(raw);
}

function readHiddenCode(input, output) {
  if (!input.isTTY || !output.isTTY) {
    throw new Error('a private terminal is required');
  }
  return new Promise((resolve, reject) => {
    let code = '';
    const previousRawMode = Boolean(input.isRaw);
    function finish(error) {
      input.removeListener('keypress', onKey);
      input.removeListener('end', onEnd);
      input.setRawMode(previousRawMode);
      input.pause();
      output.write('\n');
      if (error) reject(error);
      else resolve(code);
    }
    function onEnd() {
      finish(new Error('input closed'));
    }
    function onKey(char, key) {
      if (key?.name === 'escape' || (key?.ctrl && ['c', 'd'].includes(key.name))) {
        finish(new Error('input cancelled'));
      } else if (key?.name === 'return' || key?.name === 'enter') {
        finish();
      } else if (key?.name === 'backspace') {
        code = code.slice(0, -1);
      } else if (!key?.ctrl && !key?.meta && typeof char === 'string' && /^[A-Za-z0-9_-]$/.test(char)) {
        code += char;
      }
      if (code.length > 128) finish(new Error('input too long'));
    }
    emitKeypressEvents(input);
    input.setRawMode(true);
    input.on('keypress', onKey);
    input.once('end', onEnd);
    output.write('请输入本次操作的授权码（输入不显示），按回车提交。\n');
    input.resume();
  });
}

function safeReceipt(actionId, reply, code, runtime) {
  const data = reply.structuredContent;
  if (!data || typeof data !== 'object') throw new Error('structured receipt missing');
  if (data.action?.actionId && data.action.actionId.toLowerCase() !== actionId.toLowerCase()) {
    throw new Error('receipt does not match the requested action');
  }
  const label = (value) => typeof value === 'string' && safeLabels.test(value) && !value.includes(code) ? value : null;
  return {
    ...runtime,
    actionId,
    requestId: typeof data.requestId === 'string' && uuid.test(data.requestId) ? data.requestId : null,
    outcome: label(data.outcome),
    actionState: label(data.action?.state),
    dispatchState: label(data.action?.dispatchState),
    executionStatus: label(data.result?.requestStatus),
    errorCode: label(data.error?.code),
    rejected: reply.isError === true || Boolean(data.error),
  };
}

function writeLine(stream, value) {
  // A one-shot process must flush a known receipt even if SDK handles linger.
  writeSync(stream.fd, JSON.stringify(value) + '\n');
}

async function closeClient(client) {
  if (!client) return true;
  let limit;
  try {
    return await Promise.race([
      client.close().then(() => true, () => false),
      new Promise((resolve) => { limit = setTimeout(() => resolve(false), cleanupTimeout); }),
    ]);
  } finally {
    clearTimeout(limit);
  }
}

async function resume() {
  let phase = 'configuration';
  let client;
  let input;
  let actionId;
  let submitted = false;
  let reported = false;
  let exitCode = 1;
  let requestDeadline;
  let processDeadline;
  const requests = new AbortController();
  const options = { timeout: connectTimeout, signal: requests.signal };

  function reportFailure() {
    if (reported) return;
    const failure = { failure: phase };
    // No response after starting the request means the gateway may already
    // have acted. Inspect action.status for this action before doing anything else.
    if (submitted) Object.assign(failure, { actionId, outcomeUnknown: true });
    writeLine(process.stderr, failure);
    reported = true;
  }

  try {
    const [requestedAction, configSource, ...extra] = process.argv.slice(2);
    if (!uuid.test(requestedAction ?? '') || !configSource || extra.length) throw new Error('invalid invocation');
    actionId = requestedAction;

    // The code never goes in command-line arguments, environment or the model registry.
    if (configSource === '--private-pipe') {
      if (process.stdin.isTTY) throw new Error('private pipe required');
      input = createInterface({ input: process.stdin, crlfDelay: Infinity });
      requestDeadline = setTimeout(() => requests.abort(), privatePipeTimeout - cleanupTimeout);
      processDeadline = setTimeout(() => {
        try {
          if (exitCode === 1) reportFailure();
          else writeLine(process.stderr, { cleanup: 'UNCONFIRMED' });
        } finally {
          process.exit(exitCode);
        }
      }, privatePipeTimeout);
    }
    const pipe = input?.[Symbol.asyncIterator]();
    const rawConfig = pipe ? (await pipe.next()).value : await readFile(configSource, 'utf8');
    const config = JSON.parse(rawConfig);

    phase = 'installed-runtime';
    process.env.DSH_TELEMETRY = 'off';
    const asar = process.env.MCPGUARD_DSH_ASAR ?? path.join(path.dirname(process.execPath), 'resources', 'app.asar');
    const modules = path.join(asar, 'dsh', 'node_modules');
    const [harness, sdk] = await Promise.all([
      packageInfo(modules, '@deepseek-ai/dsh-mcp-client'), packageInfo(modules, '@modelcontextprotocol/client'),
    ]);
    const loadSDK = (module) => import(pathToFileURL(path.join(modules, '@modelcontextprotocol', 'client', 'dist', module)).href);
    const { Client, StreamableHTTPClientTransport } = await loadSDK('index.mjs');
    client = new Client({ name: 'dsh-mcpguard-private-resume', version: harness.version }, {
      capabilities: {}, supportedProtocolVersions: [protocolVersion], versionNegotiation: { mode: 'legacy' },
    });

    phase = 'transport';
    let transport;
    if (config.transport === 'streamable-http') {
      const endpoint = new URL(config.url);
      const loopback = ['127.0.0.1', 'localhost', '[::1]'].includes(endpoint.hostname);
      if ((endpoint.protocol !== 'https:' && !(endpoint.protocol === 'http:' && loopback)) ||
          endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
        throw new Error('invalid gateway URL');
      }
      const headers = new Headers(config.headers);
      if (headers.has('x-mcpguard-authorization-code')) throw new Error('code in transport configuration');
      transport = new StreamableHTTPClientTransport(endpoint, { requestInit: { headers, redirect: 'error' } });
    } else if (config.transport === 'stdio') {
      const { StdioClientTransport } = await loadSDK('stdio.mjs');
      if (!path.isAbsolute(config.command)) throw new Error('connector path must be absolute');
      transport = new StdioClientTransport({
        command: config.command, args: config.args, env: config.env, cwd: config.cwd, stderr: 'ignore',
      });
    } else {
      throw new Error('unsupported transport');
    }
    await client.connect(transport, options);
    phase = 'discovery';
    const catalog = await client.listTools(undefined, options);
    const tool = catalog.tools.find((entry) => entry.name === toolName);
    if (!tool) throw new Error('resume tool unavailable');

    phase = 'private-input';
    const code = pipe ? (await pipe.next()).value : await readHiddenCode(process.stdin, process.stderr);
    input?.close();
    if (typeof code !== 'string' || !codeFormat.test(code)) throw new Error('invalid code format');

    phase = 'resume';
    requests.signal.throwIfAborted();
    submitted = true;
    const reply = await client.callTool({ name: toolName, arguments: { actionId }, _meta: { [metadataKey]: code } }, {
      ...options, toolDefinition: tool,
    });
    phase = 'receipt';
    const receipt = safeReceipt(actionId, reply, code, {
      transport: config.transport, harnessVersion: harness.version, sdkVersion: sdk.version,
    });
    writeLine(process.stdout, receipt);
    reported = true;
    exitCode = receipt.rejected ? 2 : 0;
  } catch {
    // Upstream diagnostics may echo request metadata. Emit only local phase names.
    reportFailure();
  } finally {
    input?.close();
    clearTimeout(requestDeadline);
    try {
      if (!(await closeClient(client)) && exitCode !== 1) writeLine(process.stderr, { cleanup: 'UNCONFIRMED' });
    } finally {
      clearTimeout(processDeadline);
      process.exit(exitCode);
    }
  }
}

await resume();
