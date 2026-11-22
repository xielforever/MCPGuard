#!/usr/bin/env node
// Exercise the real local console without placing fixture credentials in command
// arguments, files produced by this runner, or evidence. Supply input on stdin
// or point at an existing private fixture file when launched across WSL.
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createReadStream} from 'node:fs';
import {mkdtemp, readFile, realpath, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {isAbsolute, join, relative} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';

const kinds = ['postgresql', 'redis', 'jenkins', 'kubernetes'];
const configLabels = {
  host: '主机', port: '端口', tls: '使用 TLS', caCertificate: 'CA 证书（可选）',
  database: '数据库', schema: 'Schema', keyPrefix: '允许的键前缀', probeKey: '用于测试的键',
  baseUrl: '服务地址', folder: '任务目录', namespace: '只读命名空间',
  allowBuilds: '允许提交构建审批',
};
let phase = 'reading fixture input';

class ProofError extends Error {}

function check(condition, message) {
  if (!condition) throw new ProofError(message);
}

async function input() {
  let size = 0;
  const blocks = [];
  const source = process.argv[2] ? createReadStream(process.argv[2]) : process.stdin;
  for await (const block of source) {
    size += block.length;
    check(size <= 256 * 1024, 'Browser fixture input exceeds 256 KiB');
    blocks.push(block);
  }
  let fixture;
  try { fixture = JSON.parse(Buffer.concat(blocks).toString('utf8')); }
  catch { throw new ProofError('Browser fixture input must be JSON'); }
  check(fixture && typeof fixture.loginSecret === 'string' && fixture.loginSecret !== '', 'Missing console login secret');
  const url = URL.parse(fixture.url);
  check(url && ['http:', 'https:'].includes(url.protocol) && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) &&
    !url.username && !url.password && !url.search && !url.hash, 'Only a local console URL is allowed');
  check(Array.isArray(fixture.connections) && fixture.connections.length > 0 &&
    fixture.connections.length <= kinds.length, 'Provide one to four connections');
  check(fixture.connections.every(item => item && kinds.includes(item.type) && typeof item.name === 'string' && item.name !== '' &&
    item.config && typeof item.config === 'object' && typeof item.credentials?.secret === 'string' &&
    Array.isArray(item.principalIds) && item.principalIds.length > 0 && item.principalIds.every(id => typeof id === 'string')) &&
    new Set(fixture.connections.map(item => item.name)).size === fixture.connections.length, 'Invalid or duplicate connection fixture');
  return {...fixture, url: url.href, loginUsername: fixture.loginUsername || 'operator'};
}

async function until(label, read, timeout = 15000) {
  const deadline = Date.now() + timeout;
  do {
    const value = await read();
    if (value) return value;
    await delay(75);
  } while (Date.now() < deadline);
  throw new ProofError(label);
}

async function browserSocket(profile, browser) {
  let launchFailed = false;
  browser.once('error', () => { launchFailed = true; });
  const port = await until('Chrome did not start its local debugging endpoint', async () => {
    check(!launchFailed && browser.exitCode === null, 'Chrome could not start');
    const entry = await readFile(join(profile, 'DevToolsActivePort'), 'utf8').catch(() => '');
    const candidate = entry.split('\n')[0].trim();
    return /^\d{1,5}$/.test(candidate) && Number(candidate) <= 65535 ? candidate : null;
  });
  return until('Chrome did not expose an isolated page', async () => {
    try {
      const response = await fetch('http://127.0.0.1:' + port + '/json/list', {signal: AbortSignal.timeout(1000)});
      const pages = await response.json();
      const debuggerUrl = URL.parse(pages.find(page => page.type === 'page')?.webSocketDebuggerUrl);
      return debuggerUrl?.protocol === 'ws:' && debuggerUrl.hostname === '127.0.0.1' && debuggerUrl.port === port ? debuggerUrl.href : null;
    } catch {
      return null;
    }
  });
}

async function devTools(url) {
  const socket = new WebSocket(url);
  const pending = new Map();
  let sequence = 0;
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    const receive = pending.get(message.id);
    if (receive) receive(message);
  });
  await Promise.race([
    once(socket, 'open'),
    delay(5000, undefined, {ref: false}).then(() => { throw new ProofError('Cannot connect to isolated Chrome'); }),
  ]);
  function command(method, params = {}, timeout = 10000) {
    return new Promise((resolve, reject) => {
      const id = ++sequence;
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new ProofError('Chrome ' + method + ' timed out after ' + timeout + ' ms'));
      }, timeout);
      pending.set(id, message => {
        clearTimeout(timer);
        pending.delete(id);
        if (message.error) reject(new ProofError('Chrome rejected ' + method));
        else resolve(message.result);
      });
      socket.send(JSON.stringify({id, method, params}));
    });
  }
  return {
    command,
    close: () => socket.close(),
    evaluate: async (task, data = null) => {
      const result = await command('Runtime.evaluate', {
        expression: '(' + task + ')(' + JSON.stringify(data) + ')', awaitPromise: true, returnByValue: true,
      });
      check(!result.exceptionDetails, 'A console interaction failed in Chrome');
      return result.result.value;
    },
  };
}

async function createConnector(browser, row) {
  const accepted = await browser.evaluate(({row, labels}) => {
    const form = document.querySelector('form[data-form="新增连接器"]');
    if (!form) return false;
    const labelsInForm = [...form.querySelectorAll('label')];
    const read = label => labelsInForm.find(item => item.querySelector('span')?.textContent === label)?.control;
    const kind = read('连接器类型');
    if (!kind) return false;
    kind.value = row.type;
    kind.dispatchEvent(new Event('change', {bubbles: true}));
    function fill(label, value) {
      const input = [...form.querySelectorAll('label')]
        .find(item => item.querySelector('span')?.textContent === label)?.control;
      if (!input) return false;
      if (input.type === 'checkbox') input.checked = Boolean(value);
      else input.value = value;
      input.dispatchEvent(new Event('change', {bubbles: true}));
      return true;
    }
    if (!fill('连接器名称', row.name)) return false;
    for (const [key, value] of Object.entries(row.config)) {
      if (!labels[key] || !fill(labels[key], value)) return false;
    }
    if (row.type !== 'kubernetes' && row.credentials.username && !fill('服务账号', row.credentials.username)) return false;
    const password = form.querySelector('input[type="password"]');
    if (!password) return false;
    password.value = row.credentials.secret;
    const allowed = [...form.querySelectorAll('fieldset input[type="checkbox"]')];
    for (const input of allowed) input.checked = row.principalIds.includes(input.value);
    if (allowed.filter(input => input.checked).length !== row.principalIds.length || !form.checkValidity()) return false;
    form.requestSubmit();
    return true;
  }, {row, labels: configLabels});
  check(accepted, row.type + ' creation form could not be submitted');
}

async function readConnector(browser, name) {
  return browser.evaluate(name => {
    const entry = [...document.querySelectorAll('[data-connector-id]')].find(item => item.textContent === name);
    if (!entry) return null;
    const cells = entry.closest('tr').cells;
    return {id: entry.dataset.connectorId, testLabel: cells[2].textContent, activeLabel: cells[4].textContent};
  }, name);
}

async function clickButton(browser, label) {
  return browser.evaluate(label => {
    const button = [...document.querySelectorAll('button')]
      .find(item => (item.getAttribute('aria-label') || item.textContent) === label && !item.disabled);
    if (!button) return false;
    button.click();
    return true;
  }, label);
}

async function play(browser, fixture) {
  phase = 'opening the console in Chrome';
  const navigation = await browser.command('Page.navigate', {url: fixture.url}, 20000);
  if (navigation.errorText) {
    const reason = /^net::ERR_[A-Z_]+$/.test(navigation.errorText) ? ' (' + navigation.errorText + ')' : '';
    throw new ProofError('Console page navigation failed' + reason);
  }
  phase = 'signing in to the console';
  await until('Console login form did not appear', () => browser.evaluate(() => {
    const panel = document.getElementById('login');
    return panel && !panel.hidden;
  }).catch(() => false));
  const submitted = await browser.evaluate(({username, password}) => {
    document.getElementById('username').value = username;
    document.getElementById('password').value = password;
    const form = document.getElementById('loginForm');
    if (!form.checkValidity()) return false;
    form.requestSubmit();
    return true;
  }, {username: fixture.loginUsername, password: fixture.loginSecret});
  check(submitted, 'Console login form did not accept fixture credentials');
  await until('Console login did not succeed', () => browser.evaluate(() => document.body.dataset.auth === 'ready'));
  check(await clickButton(browser, '资源'), 'Resource navigation is unavailable');
  await until('Connector management is not available for this console account',
    () => browser.evaluate(() => Boolean(document.querySelector('form[data-form="新增连接器"]'))));

  const results = [];
  for (const row of fixture.connections) {
    phase = 'creating the ' + row.type + ' connector';
    check(!await readConnector(browser, row.name), row.type + ' fixture name is already registered');
    await createConnector(browser, row);
    const created = await until(row.type + ' draft was not listed', () => readConnector(browser, row.name));
    check(created.testLabel === '待测试' && created.activeLabel === '已停用', row.type + ' was not created as an untested draft');
    phase = 'testing the ' + row.type + ' connector';
    check(await clickButton(browser, '测试连接 ' + row.name), row.type + ' test action is unavailable');
    const tested = await until(row.type + ' test did not record a result', async () => {
      const state = await readConnector(browser, row.name);
      return state && state.testLabel !== '待测试' ? state : null;
    });
    check(tested.testLabel === '测试通过', row.type + ' connection test failed');
    phase = 'enabling the ' + row.type + ' connector';
    check(await clickButton(browser, '启用 ' + row.name), row.type + ' enable action is unavailable');
    check(await clickButton(browser, '确认启用'), row.type + ' enable confirmation is unavailable');
    await until(row.type + ' enabled state was not visible', async () => (await readConnector(browser, row.name))?.activeLabel === '已启用');
    phase = 'reading the ' + row.type + ' connector audit';
    check(await clickButton(browser, '查看审计 ' + row.name), row.type + ' audit action is unavailable');
    const audit = await until(row.type + ' audit was not shown', () => browser.evaluate(name => {
      const heading = [...document.querySelectorAll('h2')].find(item => item.textContent === name + ' · 审计');
      if (!heading) return null;
      return [...heading.closest('section').querySelectorAll('tbody tr')].map(item => ({
        event: item.cells[0].textContent, revision: Number(item.cells[1].textContent),
      }));
    }, row.name));
    const events = audit.map(item => item.event);
    check(['新增', '测试通过', '启用'].every(event => events.includes(event)), row.type + ' audit did not show expected events');
    const revision = audit.find(item => item.event === '启用').revision;
    check(Number.isSafeInteger(revision) && revision > 0, row.type + ' enabled revision was missing from audit');
    results.push({id: created.id, name: row.name, type: row.type, revision, testStatus: 'PASSED', enabled: true, events});
  }
  return {status: 'PASS', browser: 'Chrome', connectors: results};
}

async function main() {
  const fixture = await input();
  phase = 'starting isolated Chrome';
  const temp = await realpath(tmpdir());
  const profile = await mkdtemp(join(temp, 'mcpguard-connector-chrome-'));
  const chrome = process.env.MCPGUARD_BROWSER || (process.platform === 'win32'
    ? join(process.env.ProgramFiles || 'C:/Program Files', 'Google', 'Chrome', 'Application', 'chrome.exe')
    : 'google-chrome');
  const browser = spawn(chrome, [
    '--headless=new', '--disable-background-networking', '--disable-component-update', '--disable-extensions',
    '--no-default-browser-check', '--no-first-run', '--no-proxy-server',
    '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0',
    '--window-size=1440,960', '--user-data-dir=' + profile, 'about:blank',
  ], {stdio: 'ignore', windowsHide: true});
  let tools;
  try {
    tools = await devTools(await browserSocket(profile, browser));
    return await play(tools, fixture);
  } finally {
    if (tools) {
      // Closing Chrome can close its socket before the command gets a reply.
      await tools.command('Browser.close', {}, 500).catch(() => {});
      tools.close();
    }
    if (browser.exitCode === null && browser.signalCode === null) {
      browser.kill();
      await Promise.race([once(browser, 'exit').catch(() => {}), delay(1000)]);
    }
    const safeProfile = await realpath(profile).catch(() => '');
    const location = safeProfile && relative(temp, safeProfile);
    if (location && !isAbsolute(location) && /^mcpguard-connector-chrome-[^/\\]+$/.test(location)) {
      await rm(safeProfile, {recursive: true, force: true, maxRetries: 2, retryDelay: 200}).catch(() => {});
    }
  }
}

try {
  console.log(JSON.stringify(await main()));
} catch (error) {
  const detail = error instanceof ProofError ? error.message : 'Connector browser proof could not be completed';
  console.error(phase + ' — ' + detail);
  process.exitCode = 1;
}
