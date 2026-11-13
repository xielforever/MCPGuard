// Real isolated browser/API acceptance. Never claims native coding-client acceptance.
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {chromium} = require(process.env.MCPGUARD_PLAYWRIGHT);
const root = path.resolve(__dirname, '..');
const cfg = JSON.parse(fs.readFileSync(process.env.MCPGUARD_CONSOLE_D_CONFIG || path.join(root, '.dev/console-d-ui-2026-10-29/private-browser.json'), 'utf8'));
const origin = 'http://' + cfg.MCPGUARD_LISTEN;
const out = path.join(root, '.dev/console-d-ui-2026-10-29/runs', new Date().toISOString().replaceAll(/[:.]/g, '-'));
fs.mkdirSync(out, {recursive: true});
const checks = [], screenshots = [], errors = [], sessions = {}, createdIDs = [];
let rpcID = 0, browser;
function check(value, label) { assert.ok(value, label); checks.push(label); }
async function rpc(user, method, params) {
  const response = await fetch(origin + '/mcp', {method: 'POST', headers: {
    'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', Authorization: 'Bearer ' + cfg[user + '_token'],
    'MCP-Protocol-Version': '2025-11-25', ...(sessions[user] ? {'Mcp-Session-Id': sessions[user]} : {})
  }, body: JSON.stringify({jsonrpc: '2.0', id: ++rpcID, method, params})});
  assert.ok(response.ok, 'fixture RPC transport');
  if (response.headers.get('mcp-session-id')) sessions[user] = response.headers.get('mcp-session-id');
  const body = await response.json(); assert.ok(!body.error, 'fixture RPC result'); return body.result;
}
async function createAction(note, user = 'alice') {
  const response = await rpc(user, 'tools/call', {name: 'pg.update_demo_note', arguments: {itemId: 'item_demo_1', note, expectedVersion: '1'}});
  assert.ok(!response.isError && response.structuredContent.kind === 'DEFERRED', 'real pending request');
  const id = response.structuredContent.actionId; createdIDs.push([user, id]); return id;
}
async function status(id, user = 'alice') { return (await rpc(user, 'tools/call', {name: 'action.status', arguments: {actionId: id}})).structuredContent.action; }
async function login(page, user, id = '') {
  await page.goto(origin + '/web/' + (id ? '#action=' + id : ''));
  await page.locator('#login').waitFor({state: 'visible'});
  await page.locator('#username').fill(user); await page.locator('#password').fill(cfg.console_secrets[user]);
  await page.locator('#loginSubmit').click(); await page.locator('#workspace').waitFor({state: 'visible'});
  if (id) await page.locator('#requestTitle').waitFor();
}
async function logout(page) { await page.locator('#logout').click(); await page.locator('#login').waitFor({state: 'visible'}); }
async function nav(page, name) {
  if (await page.locator('#mobileNav').isVisible() && !await page.locator('#sidebar').isVisible()) await page.locator('#mobileNav').click();
  await page.locator('#nav').getByRole('button', {name, exact: true}).click();
  await page.getByRole('heading', {name, exact: true, level: 1}).waitFor();
}
async function screen(page, name) {
  const filename = name + '.png'; await page.screenshot({path: path.join(out, filename), fullPage: true}); screenshots.push(filename);
}
async function state(context) { const response = await context.request.get(origin + '/console/api/state'); assert.equal(response.status(), 200); return response.json(); }
async function configure(context, payload, options = {}) {
  const current = await state(context);
  const response = await context.request.patch(origin + '/console/api/config', {headers: {Origin: origin, 'X-CSRF-Token': current.csrf, 'If-Match': '"' + current.revision + '"', 'Idempotency-Key': crypto.randomUUID(), ...options.headers}, data: payload});
  return response;
}
async function save(page, trigger, label = '确认保存') {
  await trigger.click(); await page.locator('#confirmDialog').waitFor({state: 'visible'});
  const promise = page.waitForResponse(response => response.url().endsWith('/console/api/config') && response.request().method() === 'PATCH');
  await page.locator('#confirmDialog').getByRole('button', {name: label, exact: true}).click();
  const response = await promise; assert.equal(response.status(), 200, 'real management save'); const result = await response.json();
  await page.waitForFunction(revision => document.getElementById('version').textContent === '配置版本 ' + revision, result.revision);
  await page.waitForFunction(() => document.getElementById('recoveryControls').hidden);
}
const form = (page, name) => page.locator('form[data-form="' + name + '"]');

(async () => {
  browser = await chromium.launch({headless: true, executablePath: process.env.MCPGUARD_BROWSER, args: ['--no-proxy-server']});
  const context = await browser.newContext({viewport: {width: 1600, height: 1100}, reducedMotion: 'reduce'});
  const page = await context.newPage(); page.on('pageerror', error => errors.push(error.message));
  for (const user of ['alice', 'bob']) await rpc(user, 'initialize', {protocolVersion: '2025-11-25', capabilities: {}, clientInfo: {name: 'console-d-isolated-browser', version: '1'}});
  const id = await createAction('D 控制台本地验证 · 待独立审批');
  const secondID = await createAction('D 控制台本地验证 · 第二份计划');
  const bobID = await createAction('D 控制台本地验证 · Bob 独立计划', 'bob');

  await page.goto(origin + '/web/#action=' + id); await page.locator('#login').waitFor({state: 'visible'});
  check(await page.locator('#workspace').isHidden(), 'anonymous session has no management data');
  check((await context.request.get(origin + '/console/api/state')).status() === 401, 'management API still requires independent browser login');
  const htmlResponse = await context.request.get(origin + '/web/');
  const policy = htmlResponse.headers()['content-security-policy'];
  check(policy.includes("style-src 'self'") && !policy.includes('unsafe-inline'), 'console CSP permits only same-origin scripts and styles');
  for (const [asset, mime] of [['tokens.css', 'text/css'], ['components.css', 'text/css'], ['state-labels.js', 'text/javascript'], ['components.js', 'text/javascript']]) {
    const response = await context.request.get(origin + '/web/ui/' + asset);
    check(response.ok() && response.headers()['content-type'].startsWith(mime) && response.headers()['cache-control'] === 'no-store', 'shared public asset served securely: ' + asset);
  }
  check((await context.request.get(origin + '/web/ui/assets.go')).status() === 404, 'asset router does not expose arbitrary embedded sources');
  check((await context.request.get(origin + '/web/', {headers: {Host: 'untrusted.example'}})).status() === 403, 'loopback Host restriction remains enforced');
  await screen(page, 'login-desktop');
  await login(page, 'alice', id);
  check(await page.getByText('此为本人申请，不能自行审批。', {exact: true}).isVisible(), 'requester sees explicit self-approval boundary');
  check(await page.locator('[data-action=approve]').count() === 0, 'requester never receives an approval control');
  const alice = await state(context);
  check(alice.principalId === cfg.ids.alice && !alice.admin && !Object.hasOwn(alice, 'users') && !alice.actions.some(action => action.actionId === bobID), 'Alice snapshot excludes admin data and Bob requests');
  for (const name of ['规则', '用户与权限']) { await nav(page, name); check(await page.getByText(/需要管理员权限/).first().isVisible() && await page.locator('#content form').count() === 0, 'non-admin management boundary: ' + name); }
  await logout(page);
  check((await page.locator('#content').textContent()) === '' && (await page.locator('#summary').textContent()) === '' && !(await page.locator('#identity').getAttribute('title')), 'logout removes private DOM, identity metadata and selections');
  await login(page, 'visitor', id);
  check(await page.locator('[data-action=approve]').isVisible(), 'independent authorized reviewer can review the real request');
  await screen(page, 'approvals-desktop');

  const approve = page.locator('[data-action=approve]'); await approve.focus(); await page.keyboard.press('Enter');
  await page.locator('#confirmDialog').waitFor({state: 'visible'});
  check(await page.locator('#confirmDialog').getByText(id, {exact: true}).isVisible(), 'approval confirmation binds the exact request');
  check(await page.locator('#confirmDialog').getByText(/批准只记录决定/).isVisible(), 'approval confirmation explains deferred execution');
  await screen(page, 'approval-confirmation');
  await page.keyboard.press('Escape'); await page.locator('#confirmDialog').waitFor({state: 'hidden'});
  await page.waitForFunction(() => document.activeElement?.dataset.action === 'approve');
  check((await status(id)).state === 'PENDING_APPROVAL', 'Escape cancels confirmation without posting a decision and restores focus');
  await page.locator('[role=tab]').first().focus(); await page.keyboard.press('ArrowRight');
  check(await page.getByRole('tab', {name: '状态与派发'}).getAttribute('aria-selected') === 'true', 'details tabs support arrow-key navigation');
  check(await page.locator('[data-state=NOT_DISPATCHED]').isVisible() && await page.locator('[data-state=NONE]').isVisible(), 'dispatch and side-effect states are displayed independently');
  await page.getByRole('tab', {name: '冻结参数'}).click();
  await approve.click();
  const decisionResponse = page.waitForResponse(response => response.url().endsWith('/decision') && response.request().method() === 'POST');
  await page.locator('#confirmDialog').getByRole('button', {name: '确认批准', exact: true}).dblclick({delay: 50});
  const decided = await decisionResponse; check(decided.status() === 200, 'confirmed decision reaches the existing protected endpoint');
  check(Object.keys(decided.request().postDataJSON()).sort().join(',') === 'planHash,vote', 'legacy decision body preserves its exact versioned fields');
  await page.getByText('审批已记录。请由申请人在原调用端核对状态并显式恢复。', {exact: true}).waitFor();
  const ready = await status(id); check(ready.state === 'READY' && ready.executionId === null && ready.dispatchState === 'NOT_DISPATCHED', 'approval records READY without dispatching a native write');
  await screen(page, 'approval-ready');

  const current = await state(context);
  const missingCsrf = await context.request.patch(origin + '/console/api/config', {headers: {Origin: origin, 'If-Match': '"' + current.revision + '"', 'Idempotency-Key': crypto.randomUUID()}, data: {kind: 'resource', resource: 'demo.directory', enabled: true}});
  check(missingCsrf.status() === 403, 'configuration still rejects missing CSRF');
  const wrongOrigin = await context.request.patch(origin + '/console/api/config', {headers: {Origin: 'http://untrusted.example', 'X-CSRF-Token': current.csrf, 'If-Match': '"' + current.revision + '"', 'Idempotency-Key': crypto.randomUUID()}, data: {kind: 'resource', resource: 'demo.directory', enabled: true}});
  check(wrongOrigin.status() === 403, 'configuration still rejects a foreign Origin');

  await nav(page, '资源'); await screen(page, 'resources-desktop');
  const resourceRow = () => page.locator('tr').filter({has: page.getByText('demo.directory', {exact: true})});
  await save(page, resourceRow().getByRole('button', {name: '停用', exact: true}));
  check(!(await state(context)).resources.find(resource => resource.name === 'demo.directory').enabled, 'resource disable persists through the real API');
  await save(page, resourceRow().getByRole('button', {name: '启用', exact: true}));
  check((await state(context)).resources.find(resource => resource.name === 'demo.directory').enabled, 'resource enable persists through the real API');

  await nav(page, '规则'); await screen(page, 'rules-desktop');
  const ruleForm = () => form(page, '新增规则');
  await ruleForm().getByLabel('参数匹配表达式').fill('[');
  await ruleForm().getByRole('button', {name: '保存规则', exact: true}).click();
  const badRuleResponse = page.waitForResponse(response => response.url().endsWith('/api/config') && response.request().method() === 'PATCH');
  await page.locator('#confirmDialog').getByRole('button', {name: '确认保存', exact: true}).click();
  check((await badRuleResponse).status() === 400, 'invalid server-side rule expression is rejected');
  await ruleForm().locator('[role=alert]').waitFor();
  check(await ruleForm().getByLabel('参数匹配表达式').inputValue() === '[', 'rejected form submission retains the draft with an inline error');
  const xssPattern = '<img src=x onerror=alert(1)> ' + path.basename(out);
  await ruleForm().getByLabel('参数匹配表达式').fill(xssPattern); await save(page, ruleForm().getByRole('button', {name: '保存规则', exact: true}));
  const createdRule = (await state(context)).rules.find(rule => rule.pattern === xssPattern); check(Boolean(createdRule), 'new rule persists in existing policy storage');
  check(await page.locator('#content img').count() === 0 && await page.getByText(xssPattern, {exact: true}).isVisible(), 'rule pattern is rendered as inert text');
  const ruleRow = () => page.locator('tr').filter({has: page.getByText(xssPattern, {exact: true})});
  await save(page, ruleRow().getByRole('button', {name: '停用', exact: true}));
  check(!(await state(context)).rules.find(rule => rule.id === createdRule.id).enabled, 'rule disable preserves the same rule identity');

  await nav(page, '用户与权限'); await screen(page, 'identities-desktop');
  let identity = form(page, '调整现有身份');
  await identity.getByLabel('用户', {exact: true}).selectOption(cfg.ids.bob);
  const originalBob = (await state(context)).users.find(user => user.id === cfg.ids.bob);
  check(await identity.getByLabel('数据范围').inputValue() === originalBob.scopeId, 'identity editor loads the selected user actual scope');
  await identity.getByLabel('业务权限有效').uncheck(); await save(page, identity.getByRole('button', {name: '保存身份', exact: true}));
  check(!(await state(context)).users.find(user => user.id === cfg.ids.bob).canRead, 'identity business permission can be disabled');
  identity = form(page, '调整现有身份'); await identity.getByLabel('用户', {exact: true}).selectOption(cfg.ids.bob); await identity.getByLabel('业务权限有效').check(); await save(page, identity.getByRole('button', {name: '保存身份', exact: true}));
  check((await state(context)).users.find(user => user.id === cfg.ids.bob).canRead, 'identity permission is restored explicitly');
  const grants = () => form(page, '工具及审批授权');
  for (const kind of ['grant', 'reviewer', 'member']) {
    await grants().getByLabel('用户', {exact: true}).selectOption(cfg.ids.bob); await grants().getByLabel('权限类型').selectOption(kind);
    if (kind === 'reviewer') await grants().getByLabel('审批资源').selectOption('demo.directory');
    const before = await grants().getByLabel('授权', {exact: true}).isChecked();
    await grants().getByLabel('授权', {exact: true}).setChecked(!before); await save(page, grants().getByRole('button', {name: '保存授权', exact: true}));
    const persisted = await state(context);
    check(kind === 'grant' ? persisted.grants.some(grant => grant.principalId === cfg.ids.bob && grant.toolName === 'pg.update_demo_note' && grant.allowed === !before) : kind === 'reviewer' ? persisted.reviewers.some(grant => grant.principalId === cfg.ids.bob && grant.resource === 'demo.directory' && grant.allowed === !before) : persisted.users.find(user => user.id === cfg.ids.bob).admin === !before, 'direct permission form persists: ' + kind);
    await grants().getByLabel('用户', {exact: true}).selectOption(cfg.ids.bob); await grants().getByLabel('权限类型').selectOption(kind);
    if (kind === 'reviewer') await grants().getByLabel('审批资源').selectOption('demo.directory');
    await grants().getByLabel('授权', {exact: true}).setChecked(before); await save(page, grants().getByRole('button', {name: '保存授权', exact: true}));
  }
  await grants().getByLabel('用户', {exact: true}).selectOption(cfg.ids.visitor); await grants().getByLabel('权限类型').selectOption('member');
  check(await grants().getByRole('button', {name: '保存授权', exact: true}).isDisabled(), 'current admin self-edit remains unavailable');
  const groupName = 'D local <script>plain text</script> ' + path.basename(out);
  await form(page, '创建用户组').getByLabel('新组名').fill(groupName); await save(page, form(page, '创建用户组').getByRole('button', {name: '创建用户组', exact: true}));
  const group = (await state(context)).groups.find(group => group.name === groupName);
  check(Boolean(group) && !group.enabled && await page.locator('#content script').count() === 0, 'new group is persisted disabled and rendered without script execution');
  const groupRow = () => page.locator('tr').filter({has: page.getByText(groupName, {exact: true})});
  await save(page, groupRow().getByRole('button', {name: '启用', exact: true}));
  check((await state(context)).groups.find(item => item.id === group.id).enabled, 'group enable uses its existing identity');
  const groupForm = () => form(page, '调整用户组配置');
  for (const kind of ['groupMember', 'groupGrant', 'groupReviewer']) {
    await groupForm().getByLabel('用户组', {exact: true}).selectOption(group.id); await groupForm().getByLabel('操作类型').selectOption(kind);
    if (kind === 'groupMember') await groupForm().getByLabel('用户', {exact: true}).selectOption(cfg.ids.bob);
    if (kind === 'groupReviewer') await groupForm().getByLabel('资源', {exact: true}).selectOption('demo.directory');
    await groupForm().getByLabel('加入或授权').check(); await save(page, groupForm().getByRole('button', {name: '保存组配置', exact: true}));
    const saved = await state(context);
    check(kind === 'groupMember' ? saved.groupMembers.some(item => item.groupId === group.id && item.principalId === cfg.ids.bob) : kind === 'groupGrant' ? saved.groupGrants.some(item => item.groupId === group.id && item.allowed) : saved.groupReviewers.some(item => item.groupId === group.id && item.resource === 'demo.directory' && item.allowed), 'group permission form persists: ' + kind);
  }
  await screen(page, 'identities-configured');
  await save(page, groupRow().getByRole('button', {name: '停用', exact: true}));
  check(!(await state(context)).groups.find(item => item.id === group.id).enabled, 'group disable preserves all historical membership records');

  await nav(page, '本人审计'); await page.getByRole('button', {name: '读取本人事件', exact: true}).click();
  await page.getByRole('columnheader', {name: '序号', exact: true}).waitFor();
  check(await page.locator('.mg-table tbody tr').count() > 0, 'audit reads real current-user events');
  const auditAfter = await page.locator('#content').textContent();
  if (await page.getByRole('button', {name: '读取下一页', exact: true}).count()) {
    await page.getByRole('button', {name: '读取下一页', exact: true}).click();
    await page.waitForFunction(before => document.getElementById('content').textContent !== before, auditAfter);
    check(true, 'audit pagination remains available after the first page');
  }
  await screen(page, 'audit-desktop');
  await nav(page, '通知与设置'); await screen(page, 'settings-desktop');
  check(await page.getByText('待选择身份源', {exact: true}).isVisible() && await page.getByText('待配置 Bot 和接收目标', {exact: true}).isVisible(), 'settings show actual unconfigured integration state');

  // Lost acknowledgement: backend commits once, first browser response is interrupted.
  await nav(page, '资源');
  const bodies = [], keys = []; let loseOnce = true;
  await page.route('**/console/api/config', async route => {
    bodies.push(route.request().postData()); keys.push(route.request().headers()['idempotency-key']);
    if (loseOnce) { loseOnce = false; const response = await route.fetch(); assert.equal(response.status(), 200); await route.abort('connectionreset'); }
    else await route.continue();
  });
  await resourceRow().getByRole('button', {name: '停用', exact: true}).click();
  await page.locator('#confirmDialog').getByRole('button', {name: '确认保存', exact: true}).click();
  await page.getByRole('button', {name: '使用原请求重试', exact: true}).waitFor();
  const lostState = await state(context);
  check(!lostState.resources.find(resource => resource.name === 'demo.directory').enabled, 'lost acknowledgement is shown as uncertain even when backend committed');
  await screen(page, 'uncertain-submission');
  await page.getByRole('button', {name: '核对当前状态', exact: true}).click();
  await page.getByRole('button', {name: '使用原请求重试', exact: true}).click();
  await page.locator('#confirmDialog').getByRole('button', {name: '确认原请求重试', exact: true}).click();
  await page.locator('#recoveryControls').waitFor({state: 'hidden'});
  check(bodies.length === 2 && bodies[0] === bodies[1] && keys[0] === keys[1], 'uncertain management retry reuses the exact body and idempotency key');
  check((await state(context)).revision === lostState.revision, 'idempotent retry does not create a second configuration mutation');
  await page.unroute('**/console/api/config');
  await save(page, resourceRow().getByRole('button', {name: '启用', exact: true}));

  // Stale form: another authenticated management request advances the version.
  await nav(page, '规则'); await ruleForm().getByLabel('参数匹配表达式').fill('D stale draft');
  const advance = await configure(context, {kind: 'resource', resource: 'demo.directory', enabled: true}); assert.equal(advance.status(), 200);
  await ruleForm().getByRole('button', {name: '保存规则', exact: true}).click();
  const conflictResponse = page.waitForResponse(response => response.url().endsWith('/api/config') && response.request().method() === 'PATCH');
  await page.locator('#confirmDialog').getByRole('button', {name: '确认保存', exact: true}).click();
  check((await conflictResponse).status() === 409, 'stale configuration version is rejected by the unchanged backend');
  await ruleForm().locator('[role=alert]').waitFor();
  check(await ruleForm().getByLabel('参数匹配表达式').inputValue() === 'D stale draft', 'version conflict preserves the user draft and requires explicit refresh');
  check(!(await state(context)).rules.some(rule => rule.pattern === 'D stale draft'), 'version conflict never silently resubmits a new mutation');

  // Preflight qualification: remove the reviewer while confirmation is open.
  const qualifiedID = await createAction('D 本地验证 · 审批资格变化');
  await page.goto(origin + '/web/#action=' + qualifiedID); await page.locator('[data-action=approve]').waitFor();
  await page.locator('[data-action=approve]').click();
  const revoke = await configure(context, {kind: 'reviewer', principalId: cfg.ids.visitor, resource: 'demo.pg', allowed: false}); assert.equal(revoke.status(), 200);
  let staleDecisionPosts = 0;
  const decisionListener = request => { if (request.url().endsWith('/decision') && request.method() === 'POST') staleDecisionPosts++; };
  page.on('request', decisionListener);
  await page.locator('#confirmDialog').getByRole('button', {name: '确认批准', exact: true}).click();
  await page.getByText('当前身份无权执行此操作，请重新核对权限。', {exact: true}).waitFor();
  check(staleDecisionPosts === 0 && (await status(qualifiedID)).state === 'PENDING_APPROVAL', 'revoked reviewer qualification fails the fresh read before any decision POST');
  page.off('request', decisionListener);
  const restore = await configure(context, {kind: 'reviewer', principalId: cfg.ids.visitor, resource: 'demo.pg', allowed: true}); assert.equal(restore.status(), 200);

  const rejectedID = await createAction('D 本地验证 · 拒绝不会执行');
  await page.goto(origin + '/web/#action=' + rejectedID); await page.locator('[data-action=reject]').waitFor();
  await page.locator('[data-action=reject]').click();
  const rejectionResponse = page.waitForResponse(response => response.url().endsWith('/decision') && response.request().method() === 'POST');
  await page.locator('#confirmDialog').getByRole('button', {name: '确认拒绝', exact: true}).click();
  check((await rejectionResponse).status() === 200, 'independent rejection uses the existing protected decision API');
  const rejected = await status(rejectedID);
  check(rejected.state === 'DENIED' && rejected.executionId === null, 'rejection persists DENIED without creating a native execution');

  // Delayed A-detail response must not replace a more recently selected request.
  await page.goto(origin + '/web/'); await page.locator('#workspace').waitFor({state: 'visible'});
  let releaseDetail; const detailGate = new Promise(resolve => { releaseDetail = resolve; }); let capturedDetail;
  const detailCaptured = new Promise(resolve => { capturedDetail = resolve; });
  await page.route('**/console/api/actions/' + id, async route => { const response = await route.fetch(); capturedDetail(); await detailGate; await route.fulfill({response}); });
  await page.locator('.queue-item[data-id="' + id + '"]').click(); await detailCaptured;
  await page.locator('.queue-item[data-id="' + secondID + '"]').click();
  await page.getByText('D 控制台本地验证 · 第二份计划', {exact: false}).waitFor(); releaseDetail();
  await page.waitForTimeout(200);
  check(await page.locator('#planSlot').textContent().then(text => text.includes('D 控制台本地验证 · 第二份计划')), 'late detail response cannot overwrite a newer selected request');
  await page.unroute('**/console/api/actions/' + id);

  // Six actual pages at mobile width, then modal resize and keyboard focus.
  await page.setViewportSize({width: 375, height: 900});
  for (const [name, file] of [['操作与审批', 'approvals'], ['资源', 'resources'], ['规则', 'rules'], ['用户与权限', 'identities'], ['本人审计', 'audit'], ['通知与设置', 'settings']]) {
    await nav(page, name);
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'mobile has no document horizontal overflow: ' + name);
    await screen(page, file + '-mobile');
  }
  await page.goto(origin + '/web/#action=' + secondID); await page.locator('[data-action=approve]').waitFor();
  await page.locator('[data-action=approve]').click();
  await page.setViewportSize({width: 1024, height: 768}); await page.setViewportSize({width: 768, height: 900}); await page.setViewportSize({width: 375, height: 900});
  check(await page.locator('#confirmDialog').isVisible(), 'open confirmation survives viewport changes');
  check(await page.evaluate(() => { const box = document.getElementById('confirmDialog').getBoundingClientRect(); return box.left >= 0 && box.right <= innerWidth && box.top >= 0 && box.bottom <= innerHeight; }), 'confirmation fits the narrow viewport');
  await page.keyboard.press('Tab');
  check(await page.evaluate(() => document.getElementById('confirmDialog').contains(document.activeElement)), 'native modal keeps keyboard focus inside the confirmation');
  await screen(page, 'confirmation-mobile'); await page.keyboard.press('Escape');
  await page.waitForFunction(() => document.activeElement?.dataset.action === 'approve');
  check(true, 'Escape restores the triggering control after modal resize');
  check(await page.locator('input').first().evaluate(input => parseFloat(getComputedStyle(input).fontSize) >= 16), 'mobile form inputs meet the 16px typography rule');
  await page.setViewportSize({width: 1600, height: 1100});
  check(await page.locator('.mg-orbit-rotating').evaluate(element => getComputedStyle(element).animationName === 'none'), 'reduced-motion preference disables decorative orbit motion');
  await page.emulateMedia({reducedMotion: 'no-preference'});
  await page.locator('#motionToggle').click();
  check(await page.locator('.mg-orbit-rotating').evaluate(element => getComputedStyle(element).animationPlayState === 'paused'), 'background motion has an explicit pause control');
  await page.locator('#navToggle').click(); check(await page.locator('#navToggle').getAttribute('aria-expanded') === 'true', 'desktop navigation expansion preserves accessible state');
  await page.setViewportSize({width: 800, height: 550});
  check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), '200-percent equivalent CSS viewport retains the complete layout without document overflow');
  await screen(page, 'zoom-200-equivalent');
  await page.setViewportSize({width: 1600, height: 1100});

  // Delay authenticated state, log out, then log in as Bob before it arrives.
  let releaseState, stateCaptured; const stateGate = new Promise(resolve => { releaseState = resolve; }); const captured = new Promise(resolve => { stateCaptured = resolve; }); let delayOnce = true;
  await page.route('**/console/api/state', async route => {
    if (!delayOnce) return route.continue(); delayOnce = false; const response = await route.fetch(); stateCaptured(); await stateGate; await route.fulfill({response});
  });
  await page.locator('#refresh').click(); await captured; await logout(page);
  await page.locator('#username').fill('bob'); await page.locator('#password').fill(cfg.console_secrets.bob); await page.locator('#loginSubmit').click();
  await page.locator('#workspace').waitFor({state: 'visible'}); releaseState(); await page.waitForTimeout(200);
  check((await state(context)).principalId === cfg.ids.bob && (await page.locator('#identity').textContent()).includes('scope_b'), 'late state response from an earlier identity cannot replace Bob current session');
  check(await page.locator('.queue-item[data-id="' + id + '"]').count() === 0 && await page.locator('#content form').count() === 0, 'identity switch never revives Alice requests or admin forms');
  await page.unroute('**/console/api/state');
  await nav(page, '本人审计'); check(await page.getByText('尚未读取事件', {exact: true}).isVisible(), 'audit cursor and cached rows reset across identities');
  await context.clearCookies(); await page.locator('#refresh').click(); await page.locator('#login').waitFor({state: 'visible'});
  check((await page.locator('#content').textContent()) === '' && await page.locator('#workspace').isHidden(), 'expired browser token clears all private content instead of retaining a stale view');

  // Pure rendering fallback for future/unknown enum values does not grant eligibility.
  await login(page, 'visitor'); await nav(page, '通知与设置');
  const unknown = await page.evaluate(() => { const element = MCPGuardUI.stateBadge('action', 'FUTURE_UNKNOWN'); return {text: element.textContent, state: element.dataset.state, tone: element.className}; });
  check(unknown.text === '状态未确认' && unknown.state === 'FUTURE_UNKNOWN' && unknown.tone.includes('warning'), 'unknown state uses an explicit safe warning fallback');
  for (const fault of ['OUTCOME_UNKNOWN', 'EXPIRED_CLOCK']) {
    await page.route('**/console/api/actions/' + qualifiedID, async route => {
      const response = await route.fetch(); const data = await response.json();
      data.canReview = true;
      if (fault === 'OUTCOME_UNKNOWN') { data.state = 'OUTCOME_UNKNOWN'; data.dispatchState = 'DISPATCH_UNKNOWN'; data.sideEffectStatus = 'UNKNOWN'; }
      else { data.state = 'PENDING_APPROVAL'; data.expiresAt = '2000-01-01T00:00:00Z'; }
      await route.fulfill({response, json: data});
    });
    await page.goto(origin + '/web/#action=' + qualifiedID); await page.locator('#requestTitle').waitFor();
    check(await page.locator('[data-action=approve],[data-action=reject]').count() === 0, 'display fault injection remains ineligible despite stale canReview=true: ' + fault);
    if (fault === 'OUTCOME_UNKNOWN') { await page.getByRole('tab', {name: '状态与派发'}).click(); check(await page.locator('[data-state=DISPATCH_UNKNOWN]').isVisible() && await page.locator('[data-state=UNKNOWN]').isVisible(), 'display fault injection preserves independent unknown dispatch and side effects'); await screen(page, 'unknown-state-display-injection'); }
    await page.unroute('**/console/api/actions/' + qualifiedID);
  }
  const text = await page.locator('body').textContent();
  check(!Object.values(cfg.console_secrets).some(secret => text.includes(secret)) && !text.includes(cfg.alice_token) && !text.includes(cfg.bob_token), 'login secrets and MCP tokens never enter rendered text');
  // Playwright screenshots restore input caret styles as an empty style="" attribute.
  // Empty instrumentation attributes contain no CSS; authored source is checked separately.
  const inlineDeclarations = await page.locator('script:not([src]),style,[style],[onclick]').evaluateAll(nodes => nodes.filter(node => node.tagName === 'SCRIPT' || node.tagName === 'STYLE' || node.hasAttribute('onclick') || node.getAttribute('style')?.trim()).length);
  check(inlineDeclarations === 0, 'console uses no inline script, CSS declarations or event attributes');
  check(errors.length === 0, 'all real browser flows complete without JavaScript exceptions');
  await logout(page); await context.close();

  const report = {kind: 'CONSOLE_D_REAL_ISOLATED_BROWSER_API', nativeCodingClientAcceptance: false, liveServiceChanged: false, fixtureRunId: cfg.runId,
    passed: checks.length, checks, screenshots, browserErrors: errors, createdActions: createdIDs.map(([, id]) => id)};
  fs.writeFileSync(path.join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  process.stdout.write(JSON.stringify({status: 'PASS', checks: checks.length, screenshots: screenshots.length, evidence: out}) + '\n');
})().catch(error => {
  fs.writeFileSync(path.join(out, 'failure.json'), JSON.stringify({status: 'FAIL', passedBeforeFailure: checks.length, checks, screenshots, message: error.message, stack: error.stack}, null, 2) + '\n');
  process.stderr.write('Console D browser validation failed; evidence retained at ' + out + '\n' + error.stack + '\n'); process.exitCode = 1;
}).finally(async () => {
  for (const [user, id] of createdIDs) { try { await rpc(user, 'tools/call', {name: 'action.cancel', arguments: {actionId: id}}); } catch {} }
  if (browser) await browser.close();
});
