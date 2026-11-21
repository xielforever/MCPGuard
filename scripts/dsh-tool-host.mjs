// Isolated installed DeepSeek Harness tool registry for MCPGuard fixtures.
// Stdin is a private pipe: transport config, then {tool,args} requests as JSON lines.
import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';

const asar = process.env.MCPGUARD_DSH_ASAR ?? path.join(path.dirname(process.execPath), 'resources', 'app.asar');
const modules = path.join(asar, 'dsh', 'node_modules', '@deepseek-ai');
const load = (name) => import(pathToFileURL(path.join(modules, name, 'lib', 'index.js')).href);
const wire = createInterface({ input: process.stdin, crlfDelay: Infinity });
const inbox = wire[Symbol.asyncIterator]();
const registerPrefix = 'mcp__mcpguard_ra18__';
const reply = (message) => console.log('MCPGUARD_DSH ' + JSON.stringify(message));

function registeredName(names, rawName) {
  const expected = registerPrefix + rawName.replace(/[^A-Za-z0-9_-]/g, '_');
  if (names.includes(expected)) return expected;
  const matches = names.filter((name) => name.startsWith(expected + '_'));
  if (matches.length === 1) return matches[0];
  throw new Error('tool registration is missing or ambiguous');
}

function failureTypes(error) {
  // Keep the package error classes that distinguish discovery and transport
  // failures. Error messages and response data can contain private material.
  const known = new Set(['Error', 'TypeError', 'JsonSchemaError', 'SdkError', 'McpLocalError',
    'McpToolRegistrationError', 'McpTimeoutError', 'McpNetworkError', 'ToolContextUnavailableError']);
  const types = [];
  for (let current = error; current && types.length < 5; current = current.cause) {
    types.push(known.has(current.name) ? current.name : 'UnclassifiedError');
  }
  return types;
}

let phase = 'configuration';
const fibers = [];
try {
  const initial = await inbox.next();
  if (initial.done) throw new Error('configuration missing');
  const {reportWorkspace, ...config} = JSON.parse(initial.value);
  process.env.DSH_TELEMETRY = 'off';
  const [cordis, prompt, tools, executor, client] = await Promise.all([
    load('cordis'), load('dsh-system-prompt'), load('dsh-tools'),
    load('dsh-tool-executor-native'), load('dsh-mcp-client'),
  ]);
  const ctx = new cordis.Context();
  async function mount(plugin, options) {
    const fiber = ctx.plugin(plugin, options);
    fibers.push(fiber);
    await fiber.await();
  }

  phase = 'registry';
  await mount(prompt.default);
  await mount(tools.default);
  await mount(executor);
  phase = 'discovery';
  await mount(client, { ...config, serverName: 'mcpguard_ra18', failOnStartupError: true,
    startupTimeoutMs: config.startupTimeoutMs ?? 16000, toolCallTimeoutMs: 16000, reconnect: { enabled: false } });
  const names = ctx.tools.definitions().map((tool) => tool.name).filter((name) => name.startsWith(registerPrefix));
  const ready = { event: 'ready', names };
  if (reportWorkspace) {
    ready.workspace = process.cwd();
    ready.runtimeVersion = JSON.parse(readFileSync(path.join(asar, 'package.json'), 'utf8')).version;
  }
  reply(ready);

  for await (const line of inbox) {
    phase = 'invocation';
    const request = JSON.parse(line);
    const name = registeredName(names, request.tool);
    // This is the ordinary tool path. Private authorization metadata belongs
    // in the operator-only dsh-private-resume.mjs process.
    const batch = await ctx.tools.execute({ signal: AbortSignal.timeout(17000), calls: [
      { type: 'tool-call', id: randomUUID(), name, arguments: JSON.stringify(request.args) },
    ] });
    const result = batch.results[0]?.result;
    if (!result) throw new Error('tool result missing');
    if (!result.isError) {
      reply({ ...result.value, event: 'result', isError: false });
      continue;
    }
    let rejected;
    try {
      rejected = JSON.parse(result.error.message);
    } catch {
      rejected = { clientError: 'DSH_TOOL_ERROR' };
    }
    reply({ event: 'result', isError: true, structuredContent: rejected });
  }
} catch (error) {
  // Startup diagnostics can contain expanded transport configuration.
  reply({ event: 'failed', phase, failureTypes: failureTypes(error) });
  process.exitCode = 1;
} finally {
  wire.close();
  for (const fiber of fibers.reverse()) {
    try {
      await fiber.dispose();
    } catch {
      reply({ event: 'failed', phase: 'cleanup' });
      process.exitCode = 1;
    }
  }
}
