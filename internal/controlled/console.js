'use strict';

(() => {
  const ui = globalThis.MCPGuardUI;
  const $ = id => document.getElementById(id);
  const {el, button, badge, stateBadge, notice, panel, table, field, keyValues} = ui;
  const pages = [
    {label: '操作与审批', glyph: 'actions', eyebrow: '受控操作 / CONTROL BRIDGE',
      description: '核对身份、资源与冻结计划，再作出审批决定。', render: renderApprovals},
    {label: '自助访问', glyph: 'key', eyebrow: '自助访问 / SELF-SERVICE',
      description: '查看 v0.2 资源、凭据、撤销与写审批（真实服务端 API）。', render: renderSelfService},
    {label: '资源', glyph: 'database', eyebrow: '资源管理 / RESOURCES',
      description: '查看真实资源绑定，管理受控工具的启用状态。', render: renderResources},
    {label: '规则', glyph: 'rules', eyebrow: '策略管理 / POLICIES',
      description: '拒绝规则优先；工具授权与审批要求分别校验。', render: renderRules},
    {label: '用户与权限', glyph: 'users', eyebrow: '身份与授权 / IDENTITIES',
      description: '维护现有身份、工具授权、资源审批资格和用户组。', render: renderIdentities},
    {label: '本人审计', glyph: 'history', eyebrow: '个人轨迹 / AUDIT',
      description: '按游标读取当前身份的真实审计记录。', render: renderAudit},
    {label: '通知与设置', glyph: 'settings', eyebrow: '通知与接入 / SETTINGS',
      description: '查看通知投递和当前身份接入方式。', render: renderSettings}
  ];
  const tools = ['pg.update_demo_note', 'redis.get_demo_note', 'redis.set_demo_note', 'directory.get_demo_office', 'inventory.reserve_demo'];
  const actionStates = new Set(Object.keys(globalThis.MCPGuardUIStateLabels.action));
  let snapshot = null, csrf = '', epoch = 0, page = 0, detail = null, selectedID = '';
  let detailSeq = 0, stateSeq = 0, pending = null, queueSearch = '';
  let audit = {after: '0', rows: [], loaded: false, ended: false, busy: false};
  let motion = true;

  class PublicError extends Error {
    constructor(message, status = 0) { super(message); this.publicMessage = message; this.status = status; }
  }
  function stale() { const error = new PublicError('当前视图已变化，请重新读取。'); error.silent = true; return error; }
  function assertEpoch(value) { if (epoch !== value) throw stale(); }
  function showMessage(text, tone = 'info') {
    $('message').textContent = text;
    $('message').className = 'mg-notice mg-notice--' + tone;
    $('message').hidden = false;
  }
  function resetIdentity() {
    epoch++; stateSeq++; detailSeq++;
    snapshot = null; csrf = ''; detail = null; pending = null; selectedID = ''; page = 0; queueSearch = '';
    audit = {after: '0', rows: [], loaded: false, ended: false, busy: false};
    ui.closeDialog(); $('confirmDialog').replaceChildren();
    $('content').replaceChildren(); $('summary').replaceChildren(); $('nav').replaceChildren();
    $('recoveryControls').replaceChildren(); $('recoveryControls').hidden = true;
    $('toast').replaceChildren(); $('toast').hidden = true;
    $('password').value = ''; $('username').value = '';
    for (const id of ['workspace', 'logout', 'sidebar', 'pageTools', 'mobileNav']) $(id).hidden = true;
    $('identity').textContent = '独立浏览器登录'; $('identity').removeAttribute('title'); $('version').textContent = '';
    document.body.dataset.auth = 'anonymous'; closeMenu(false);
  }
  function showLogin(text) {
    resetIdentity(); $('bootState').hidden = true; $('login').hidden = false;
    $('page-title').textContent = '登录控制台'; $('pageEyebrow').textContent = '身份校验 / IDENTITY';
    $('pageDescription').textContent = '使用独立浏览器身份，进入受控资源与审批工作区。';
    if (text) showMessage(text, 'warning');
    else $('message').hidden = true;
  }

  async function api(path, options = {}, expectedEpoch = epoch) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch('/console/' + path, {
        credentials: 'same-origin', cache: 'no-store', ...options, signal: controller.signal,
        headers: {'Content-Type': 'application/json', ...(csrf ? {'X-CSRF-Token': csrf} : {}), ...options.headers}
      });
      assertEpoch(expectedEpoch);
      if (!response.ok) {
        if (response.status === 401) {
          showLogin(path === 'login' ? '登录未成功，请核对独立登录凭据。' : '浏览器会话已失效，请重新登录。');
          throw stale();
        }
        const messages = {
          400: '请求未通过校验，请检查填写内容。',
          403: '当前身份无权执行此操作，请重新核对权限。',
          404: '对象当前不可用，请重新读取列表。',
          409: '对象或配置版本已变化，请刷新并重新核对后提交。',
          412: '版本校验未通过，请刷新并重新核对后提交。',
          429: '请求过于频繁，请稍后重试。'
        };
        throw new PublicError(messages[response.status] || '服务暂时不可用，请核对当前状态。', response.status);
      }
      const result = await response.json(); assertEpoch(expectedEpoch); return result;
    } catch (error) {
      if (error instanceof PublicError) throw error;
      assertEpoch(expectedEpoch);
      throw new PublicError('连接中断或响应未完整收到，请核对当前状态。');
    } finally { clearTimeout(timeout); }
  }

  function identityName(id) { return snapshot?.users?.find(user => user.id === id)?.name || ui.shortID(id); }
  function resourceName(id) { return snapshot?.resources?.find(resource => resource.id === id)?.name || id || '资源未取得'; }
  function boolBadge(value, yes = '有效', no = '未授权') { return badge(value ? yes : no, value ? 'success' : 'neutral'); }
  function actionBadge(action) {
    if (action.state === 'READY') return badge(action.authorizationSource === 'HUMAN' ? '已批准，待显式恢复' : '已就绪，待显式恢复', 'info');
    return stateBadge('action', action.state);
  }
  function qualified(action) {
    return snapshot && actionStates.has(action.state) && action.state === 'PENDING_APPROVAL' && action.canReview === true &&
      action.requesterId !== snapshot.principalId && new Date(action.expiresAt).getTime() > Date.now();
  }
  function adminRequired(parent, label) { parent.append(notice(label + '需要管理员权限。当前身份可继续使用已授权的操作与本人审计。', 'neutral')); }
  function listPanel(title, description, headers, rows) { const section = panel(title, description); section.append(table(headers, rows, {label: title + '，可横向滚动'})); return section; }

  async function readState() {
    const expected = epoch, sequence = ++stateSeq;
    const next = await api('api/state', {}, expected);
    if (sequence !== stateSeq) throw stale();
    if (snapshot && snapshot.principalId !== next.principalId) { showLogin('浏览器身份已变化，请重新登录。'); throw stale(); }
    snapshot = next; csrf = next.csrf;
    $('bootState').hidden = true; $('login').hidden = true;
    for (const id of ['workspace', 'logout', 'sidebar', 'pageTools', 'mobileNav']) $(id).hidden = false;
    document.body.dataset.auth = 'ready';
    $('identity').textContent = (next.admin ? '管理员' : '当前身份') + ' · ' + identityName(next.principalId) + ' · ' + next.scopeId;
    $('identity').title = '身份 ' + next.principalId;
    $('version').textContent = '配置版本 ' + next.revision;
    renderSummary(); return next;
  }
  function renderSummary() {
    if (!snapshot) return;
    const counts = [
      ['当前可见待审批', snapshot.actions.filter(action => action.state === 'PENDING_APPROVAL').length],
      ['当前可见操作', snapshot.actions.length],
      ['已配置资源', snapshot.resources.filter(resource => resource.configured).length + ' / ' + snapshot.resources.length]
    ];
    $('summary').replaceChildren(...counts.map(([label, value]) => { const item = el('div', null, 'summary-item'); item.append(el('span', label), el('strong', value)); return item; }));
  }
  function renderNav() {
    $('nav').replaceChildren(...pages.map(({label, glyph}, index) => {
      const item = el('button', null, 'nav-item'); item.type = 'button'; item.setAttribute('aria-label', label);
      if (index === page) item.setAttribute('aria-current', 'page');
      item.append(ui.icon(glyph), el('span', label)); item.addEventListener('click', () => navigate(index)); return item;
    }));
  }
  function renderPage() {
    if (!snapshot) return;
    renderNav();
    const {label, eyebrow, description, render} = pages[page];
    $('page-title').textContent = label; $('pageEyebrow').textContent = eyebrow; $('pageDescription').textContent = description;
    $('content').replaceChildren();
    render($('content'));
    renderRecovery();
  }
  function closeMenu(focus) {
    document.body.dataset.menu = 'closed'; $('mobileNav').setAttribute('aria-expanded', 'false'); $('mobileNav').setAttribute('aria-label', '展开主导航');
    if (focus && !$('mobileNav').hidden) $('mobileNav').focus();
  }
  function navigate(index) {
    if (!snapshot) return;
    page = index; detailSeq++; detail = null; selectedID = ''; queueSearch = '';
    history.replaceState(null, '', location.pathname + location.search);
    closeMenu(false); renderPage(); $('page-title').focus();
  }
  function deepLinkID() {
    const id = new URLSearchParams(location.hash.slice(1)).get('action');
    return id && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id) ? id : '';
  }
  async function refresh() {
    const beforePage = page, beforeID = selectedID;
    await readState();
    if (beforePage !== page) return;
    renderPage();
    if (page === 0 && beforeID) await selectAction(beforeID, false);
  }
  async function selectAction(id, focus = true) {
    if (!snapshot || page !== 0) return;
    const sequence = ++detailSeq, expected = epoch;
    selectedID = id; detail = null;
    history.replaceState(null, '', location.pathname + location.search + '#action=' + encodeURIComponent(id));
    updateQueueSelection();
    $('planSlot').replaceChildren(notice('正在读取当前申请与审批资格…'));
    $('decisionSlot').replaceChildren();
    try {
      const next = await api('api/actions/' + encodeURIComponent(id), {}, expected);
      if (sequence !== detailSeq || page !== 0 || selectedID !== id) throw stale();
      detail = next; renderDetail(); if (focus) $('requestTitle')?.focus({preventScroll: true});
    } catch (error) {
      if (error.silent) return;
      if (sequence === detailSeq && page === 0 && selectedID === id) $('planSlot').replaceChildren(notice(ui.errorText(error), 'warning', '申请尚未读取'));
      throw error;
    }
  }
  function updateQueueSelection() {
    for (const item of document.querySelectorAll('.queue-item')) item.setAttribute('aria-pressed', String(item.dataset.id === selectedID));
  }
  function renderApprovals(parent) {
    const layout = el('div', null, 'review-layout');
    const queue = panel('请求队列', '当前身份可见，最多 100 条。', 'request-queue');
    const search = field(queue, '筛选当前队列', 'search', {placeholder: '工具、状态或安全编号', value: queueSearch});
    const list = el('div', null, 'queue-list'); list.id = 'requestQueue';
    const draw = () => {
      const term = queueSearch.trim().toLowerCase();
      const actions = snapshot.actions.filter(action => [action.toolName, action.actionId, action.state, globalThis.MCPGuardUIStateLabels.action[action.state]?.label].join(' ').toLowerCase().includes(term));
      list.replaceChildren(...actions.map(action => {
        const item = el('button', null, 'queue-item'); item.type = 'button'; item.dataset.id = action.actionId;
        item.setAttribute('aria-pressed', String(action.actionId === selectedID));
        item.append(el('strong', action.toolName), actionBadge(action), el('small', identityName(action.requesterId) + ' · ' + ui.shortID(action.actionId)), el('small', '到期 ' + ui.date(action.expiresAt)));
        item.addEventListener('click', () => selectAction(action.actionId).catch(handleError)); return item;
      }));
      if (!actions.length) list.append(ui.empty(snapshot.actions.length ? '没有匹配的申请' : '暂无可见操作', '新的受控操作出现后，可刷新此列表。'));
    };
    search.addEventListener('input', () => { queueSearch = search.value; draw(); }); draw(); queue.append(list);
    const main = el('div', null, 'panel-stack'); main.id = 'planSlot';
    const decision = el('div'); decision.id = 'decisionSlot';
    layout.append(queue, main, decision); parent.append(layout);
    if (detail && detail.actionId === selectedID) renderDetail();
    else main.append(ui.empty(selectedID ? '正在核对申请' : '选择一条申请', '详情区会显示真实身份、资源、冻结参数和执行状态。', 'actions'));
  }
  function renderDetail() {
    if (!detail || page !== 0 || !$('planSlot')) return;
    const action = detail, name = resourceName(action.resourceId);
    const plan = el('section', null, 'mg-panel request-plan');
    const heading = el('div', null, 'request-heading');
    const titleGroup = el('div'); const title = el('h2', action.toolName); title.id = 'requestTitle'; title.tabIndex = -1;
    titleGroup.append(el('p', '冻结计划 / FROZEN PLAN', 'mg-eyebrow'), title, ui.copyID(action.actionId, '复制操作 ID'));
    heading.append(titleGroup, actionBadge(action)); plan.append(heading);
    const orbitPhase = ({PENDING_APPROVAL: '等待审批', READY: action.authorizationSource === 'HUMAN' ? '已批准' : '已就绪', EXECUTING: '执行中', SUCCEEDED: '已成功', FAILED: '请求失败', DENIED: '已拒绝', CANCELLED: '已取消', EXPIRED: '已到期', OUTCOME_UNKNOWN: '待核对'})[action.state] || '未确认';
    plan.append(ui.orbit({owner: identityName(action.requesterId), resource: name, phase: orbitPhase, description: action.state === 'PENDING_APPROVAL' ? '等待独立审批' : '以服务端状态为准'}));
    const brief = el('div', null, 'request-brief');
    brief.append(keyValues([['申请人', identityName(action.requesterId)], ['目标资源', name], ['数据范围', action.scopeId], ['到期时间', ui.date(action.expiresAt)]])); plan.append(brief);
    plan.append(ui.tabs([
      {label: '冻结参数', render: () => { const box = el('div'); box.append(el('h3', '冻结的请求参数'), notice('只读展示当前计划中的原始参数。修改参数需要原调用者重新申请。', 'neutral'), el('pre', JSON.stringify(action.arguments, null, 2), 'mg-code')); return box; }},
      {label: '状态与派发', render: () => { const box = el('div'); box.append(keyValues([['业务请求', action.requestStatus ? stateBadge('requestStatus', action.requestStatus) : badge('尚未取得结果', 'neutral')], ['派发状态', stateBadge('dispatch', action.dispatchState)], ['副作用状态', stateBadge('sideEffect', action.sideEffectStatus)], ['执行编号', action.executionId ? ui.copyID(action.executionId) : '尚未创建'], ['授权来源', action.authorizationSource === 'HUMAN' ? '独立人工审批' : action.authorizationSource === 'POLICY' ? '策略校验' : '尚未批准']])); box.append(notice('请求结果、派发状态与副作用分别展示。结果待核对时，先从原调用端查询状态。', 'warning')); return box; }},
      {label: '核对依据', render: () => { const box = el('div'); box.append(keyValues([['操作 ID', action.actionId], ['申请人 ID', action.requesterId], ['资源 ID', action.resourceId], ['计划版本', action.revision], ['计划摘要', action.planHash], ['创建时间', ui.date(action.createdAt)]])); box.append(notice('当前接口未提供变更前快照和完整审批历史；以上为本次读取的真实计划依据。', 'neutral')); return box; }}
    ]));
    $('planSlot').replaceChildren(plan);
    const decision = panel('审批决定', '仅对当前冻结计划生效。', 'decision-pane');
    decision.append(keyValues([['当前状态', actionBadge(action)], ['身份核对', action.requesterId === snapshot.principalId ? badge('本人申请', 'warning') : badge('独立身份', 'info')]]));
    if (qualified(action)) {
      decision.append(notice('批准只记录决定。申请人仍需在原调用端显式恢复执行。'));
      const actions = el('div', null, 'decision-actions');
      actions.append(button('批准', trigger => decide(action, 'APPROVE', trigger), {variant: 'primary', icon: 'check', action: 'approve'}), button('拒绝', trigger => decide(action, 'REJECT', trigger), {variant: 'danger', action: 'reject'}));
      decision.append(actions, el('p', '当前决定接口仅记录批准或拒绝，尚不接收审批说明。', 'mg-muted'));
    } else {
      const text = action.requesterId === snapshot.principalId ? '此为本人申请，不能自行审批。' : action.state === 'READY' ? '审批已记录。请由申请人在原调用端核对状态并显式恢复。' : action.state === 'OUTCOME_UNKNOWN' ? '执行结果待核对，请先查询原操作状态。' : '当前申请状态、有效期或身份资格不允许审批。';
      decision.append(notice(text, ['OUTCOME_UNKNOWN', 'PENDING_APPROVAL'].includes(action.state) ? 'warning' : 'neutral'));
    }
    decision.append(button('重新核对申请', () => selectAction(action.actionId, false), {icon: 'refresh'}));
    $('decisionSlot').replaceChildren(decision);
  }

  async function decide(action, vote, trigger) {
    const expected = epoch;
    if (pending) throw new PublicError('上一项提交尚未确认，请先核对或使用原请求重试。');
    if (!qualified(action)) throw new PublicError('审批资格或有效期已变化，请重新读取申请。');
    const confirmed = await ui.confirm({title: vote === 'APPROVE' ? '批准这份冻结计划' : '拒绝这份冻结计划', danger: vote === 'REJECT', confirmLabel: vote === 'APPROVE' ? '确认批准' : '确认拒绝',
      details: [['申请人', identityName(action.requesterId)], ['资源', resourceName(action.resourceId)], ['数据范围', action.scopeId], ['操作 ID', action.actionId], ['计划版本', action.revision], ['到期时间', ui.date(action.expiresAt)]],
      notice: '批准只记录决定，不会立即执行。提交前会再次核对计划版本与审批资格。', returnTo: trigger, canRestore: () => epoch === expected});
    assertEpoch(expected); if (!confirmed) return;
    const current = await api('api/actions/' + encodeURIComponent(action.actionId), {}, expected);
    if (!qualified(current) || current.revision !== action.revision || current.planHash !== action.planHash) {
      if (selectedID === action.actionId && page === 0) { detail = current; renderDetail(); }
      throw new PublicError('计划或审批资格已变化，请重新核对当前申请。', 409);
    }
    await submitCommand({path: 'api/actions/' + action.actionId + '/decision', method: 'POST', body: JSON.stringify({vote, planHash: action.planHash}), revision: action.revision,
      label: vote === 'APPROVE' ? '批准冻结计划' : '拒绝冻结计划', success: vote === 'APPROVE' ? '已批准，等待申请者显式恢复。' : '已拒绝。', target: action.actionId, key: crypto.randomUUID(), epoch: expected});
  }
  function configDetails(payload) {
    const keys = {resource: '资源', principalId: '用户 ID', groupId: '用户组 ID', toolName: '业务工具', scopeId: '数据范围', effect: '规则效果', name: '组名', enabled: '启用', canRead: '业务权限', admin: '控制台管理', allowed: '授权', pattern: '匹配表达式'};
    return Object.entries(payload).filter(([key]) => keys[key]).map(([key, value]) => [keys[key], typeof value === 'boolean' ? value ? '是' : '否' : value]);
  }
  async function configure(payload, trigger, revision) {
    const expected = epoch;
    if (pending) throw new PublicError('上一项提交尚未确认，请先核对或使用原请求重试。');
    const body = JSON.stringify(payload);
    const confirmed = await ui.confirm({title: '核对配置变更', details: [...configDetails(payload), ['配置版本', revision]], confirmLabel: '确认保存',
      notice: '配置变化会使相关旧审批计划失效，后续操作仍会校验当前权限。', returnTo: trigger, canRestore: () => epoch === expected});
    assertEpoch(expected); if (!confirmed) return;
    await submitCommand({path: 'api/config', method: 'PATCH', body, revision, label: '配置变更', success: '已保存。旧审批计划在配置变化后需要重新申请。', target: payload.resource || payload.toolName || payload.name || payload.principalId || payload.groupId, key: crypto.randomUUID(), epoch: expected});
  }
  async function submitCommand(command, retry = false) {
    assertEpoch(command.epoch);
    if (pending && (!retry || pending !== command || command.busy)) throw new PublicError('已有提交待确认，请先处理上一项请求。');
    pending = command; command.busy = true; command.uncertain = false; renderRecovery();
    try {
      await api(command.path, {method: command.method, body: command.body, headers: {'If-Match': '"' + command.revision + '"', 'Idempotency-Key': command.key}}, command.epoch);
    } catch (error) {
      if (command.epoch !== epoch) throw stale();
      command.busy = false;
      if (!error.status || error.status >= 500) {
        command.uncertain = true; showMessage('提交响应未确认。请核对当前状态；需要重试时使用下方原请求，避免生成重复变更。', 'warning'); renderRecovery(); return;
      }
      pending = null; renderRecovery();
      if ([403, 409, 412].includes(error.status)) {
        // Do not rerender forms here: retain the draft until the user chooses to refresh.
        if (page === 0 && selectedID) await selectAction(selectedID, false).catch(handleError);
      }
      throw error;
    }
    assertEpoch(command.epoch); pending = null; renderRecovery(); showMessage(command.success, 'success'); ui.toast(command.success);
    try { await refresh(); }
    catch (error) { if (!error.silent) showMessage(command.success + ' 列表暂未刷新，请手动核对。', 'warning'); }
  }
  function renderRecovery() {
    const region = $('recoveryControls'); region.replaceChildren(); region.hidden = !pending;
    if (!pending) return;
    if (pending.busy) { region.append(badge('正在提交，请等待当前响应', 'info')); return; }
    const command = pending;
    region.append(button('核对当前状态', async () => { await refresh(); showMessage('已重新读取当前状态。原提交的响应仍待确认，可使用同一请求重试来核对幂等结果。', 'warning'); }, {icon: 'refresh'}),
      button('使用原请求重试', async trigger => {
        const expected = epoch;
        const confirmed = await ui.confirm({title: '重试尚未确认的管理请求', details: [['操作', command.label], ['目标', command.target], ['原版本', command.revision]],
          notice: '复用原请求内容、版本与幂等编号。只核对该管理请求，不恢复业务执行。', confirmLabel: '确认原请求重试', returnTo: trigger, canRestore: () => epoch === expected});
        assertEpoch(expected); if (confirmed) await submitCommand(command, true);
      }));
  }

  function renderResources(parent) {
    parent.append(notice('连接配置由服务端私有配置管理。此页显示资源绑定与启停状态。', 'neutral'));
    const revision = snapshot.revision;
    parent.append(listPanel('资源目录', '“已配置”表示存在配置绑定，不代表本次已探测连接健康。', ['资源', '连接配置', '状态', '操作'], snapshot.resources.map(resource => {
      const title = el('div', null, 'resource-name'); title.append(ui.icon('database'), el('strong', resource.name));
      return [title, boolBadge(resource.configured, '已配置', '未配置'), boolBadge(resource.enabled, '启用', '停用'), snapshot.admin ? button(resource.enabled ? '停用' : '启用', trigger => configure({kind: 'resource', resource: resource.name, enabled: !resource.enabled}, trigger, revision), {small: true}) : '需要管理员权限'];
    })));
  }
  function renderRules(parent) {
    parent.append(notice('拒绝规则优先。受控写入始终需要他人审批；ALLOW 不能授予缺失的工具权限。', 'info'));
    if (!snapshot.admin) { adminRequired(parent, '规则管理'); return; }
    const revision = snapshot.revision;
    parent.append(listPanel('现有规则', '按已保存的真实规则展示。', ['工具', '效果', '匹配表达式', '状态', '操作'], snapshot.rules.map(rule => [rule.toolName, ({DENY: '拒绝', REQUIRE_APPROVAL: '需要审批', ALLOW: '允许'})[rule.effect] || rule.effect,
      el('span', rule.pattern || '全部参数', 'mg-code'), boolBadge(rule.enabled, '启用', '停用'), button(rule.enabled ? '停用' : '启用', trigger => configure({kind: 'rule', id: rule.id, toolName: rule.toolName, effect: rule.effect, pattern: rule.pattern, enabled: !rule.enabled}, trigger, revision), {small: true})])));
    let tool, effect, pattern;
    const form = ui.form('新增规则', '匹配表达式由服务端校验；空表达式匹配全部参数。', '保存规则', trigger => configure({kind: 'rule', id: crypto.randomUUID(), toolName: tool.value, effect: effect.value, pattern: pattern.value, enabled: true}, trigger, revision));
    tool = field(form.fields, '工具', 'select', {choices: tools});
    effect = field(form.fields, '效果', 'select', {choices: [{value: 'DENY', label: '拒绝'}, {value: 'REQUIRE_APPROVAL', label: '需要审批'}, {value: 'ALLOW', label: '允许'}]});
    pattern = field(form.fields, '参数匹配表达式', 'text', {maxLength: 512}); parent.append(form.element);
  }
  function userChoices() { return snapshot.users.map(user => ({value: user.id, label: user.name + ' · ' + user.scopeId})); }
  function renderIdentities(parent) {
    if (!snapshot.admin) { adminRequired(parent, '用户与权限管理'); return; }
    const revision = snapshot.revision;
    parent.append(listPanel('现有身份', '身份的数据范围、业务权限和管理权限分别生效。', ['用户', '数据范围', '身份状态', '业务权限', '管理权限'], snapshot.users.map(user => [user.name, user.scopeId, boolBadge(user.enabled, '有效', '停用'), boolBadge(user.canRead), user.admin ? badge('管理员', 'info') : badge('普通身份', 'neutral')])));
    const grid = el('div', null, 'management-grid');
    let who, scope, enabled, canRead;
    const identity = ui.form('调整现有身份', '更换用户后载入其真实配置。当前身份不可通过此表修改自身。', '保存身份', trigger => configure({kind: 'principal', principalId: who.value, scopeId: scope.value, enabled: enabled.checked, canRead: canRead.checked}, trigger, revision));
    who = field(identity.fields, '用户', 'select', {choices: userChoices()});
    scope = field(identity.fields, '数据范围', 'text', {required: true, maxLength: 64});
    enabled = field(identity.fields, '身份有效', 'checkbox'); canRead = field(identity.fields, '业务权限有效', 'checkbox');
    const fillIdentity = () => { const user = snapshot.users.find(item => item.id === who.value); if (!user) return; scope.value = user.scopeId; enabled.checked = user.enabled; canRead.checked = user.canRead; identity.submit.disabled = user.id === snapshot.principalId; };
    who.addEventListener('change', fillIdentity); fillIdentity();
    let user, kind, tool, resource, allowed;
    const grants = ui.form('工具及审批授权', '切换目标时显示其当前直接授权。用户组授权在下方分别管理。', '保存授权', trigger => configure(kind.value === 'grant' ? {kind: 'grant', principalId: user.value, toolName: tool.value, allowed: allowed.checked} : kind.value === 'reviewer' ? {kind: 'reviewer', principalId: user.value, resource: resource.value, allowed: allowed.checked} : {kind: 'member', principalId: user.value, admin: allowed.checked}, trigger, revision));
    user = field(grants.fields, '用户', 'select', {choices: userChoices()});
    kind = field(grants.fields, '权限类型', 'select', {choices: [{value: 'grant', label: '业务工具'}, {value: 'reviewer', label: '资源审批'}, {value: 'member', label: '控制台管理'}]});
    tool = field(grants.fields, '业务工具', 'select', {choices: tools}); resource = field(grants.fields, '审批资源', 'select', {choices: snapshot.resources.map(item => item.name)}); allowed = field(grants.fields, '授权', 'checkbox');
    const fillGrant = () => {
      tool.parentElement.hidden = kind.value !== 'grant'; resource.parentElement.hidden = kind.value !== 'reviewer';
      allowed.checked = kind.value === 'grant' ? Boolean(snapshot.grants.find(item => item.principalId === user.value && item.toolName === tool.value)?.allowed) : kind.value === 'reviewer' ? Boolean(snapshot.reviewers.find(item => item.principalId === user.value && item.resource === resource.value)?.allowed) : Boolean(snapshot.users.find(item => item.id === user.value)?.admin);
      grants.submit.disabled = kind.value === 'member' && user.value === snapshot.principalId;
    };
    for (const input of [user, kind, tool, resource]) input.addEventListener('change', fillGrant); fillGrant();
    grid.append(identity.element, grants.element); parent.append(grid);
    parent.append(listPanel('现有工具授权', '直接授予用户的业务工具权限。', ['用户', '工具', '授权'], snapshot.grants.map(grant => [identityName(grant.principalId), grant.toolName, boolBadge(grant.allowed)])),
      listPanel('现有审批授权', '直接授予用户的资源审批资格，仍禁止审批本人申请。', ['用户', '资源', '授权'], snapshot.reviewers.map(grant => [identityName(grant.principalId), grant.resource, boolBadge(grant.allowed)])));
    renderGroups(parent, revision);
  }
  function renderGroups(parent, revision) {
    const groups = snapshot.groups || [], groupName = id => groups.find(group => group.id === id)?.name || ui.shortID(id);
    parent.append(listPanel('用户组', '新组以停用状态创建，完成授权核对后再启用。', ['组名', '状态', '操作'], groups.map(group => [group.name, boolBadge(group.enabled, '启用', '停用'), button(group.enabled ? '停用' : '启用', trigger => configure({kind: 'group', id: group.id, name: group.name, enabled: !group.enabled}, trigger, revision), {small: true})])));
    const grid = el('div', null, 'management-grid'); let name;
    const create = ui.form('创建用户组', '组名会作为普通文本显示。', '创建用户组', trigger => configure({kind: 'group', id: crypto.randomUUID(), name: name.value, enabled: false}, trigger, revision));
    name = field(create.fields, '新组名', 'text', {required: true, maxLength: 100}); grid.append(create.element);
    if (groups.length) {
      let group, kind, user, tool, resource, allowed;
      const form = ui.form('调整用户组配置', '成员、业务工具和资源审批授权分别保存。', '保存组配置', trigger => configure(kind.value === 'groupMember' ? {kind: kind.value, groupId: group.value, principalId: user.value, allowed: allowed.checked} : kind.value === 'groupGrant' ? {kind: kind.value, groupId: group.value, toolName: tool.value, allowed: allowed.checked} : {kind: kind.value, groupId: group.value, resource: resource.value, allowed: allowed.checked}, trigger, revision));
      group = field(form.fields, '用户组', 'select', {choices: groups.map(item => ({value: item.id, label: item.name}))});
      kind = field(form.fields, '操作类型', 'select', {choices: [{value: 'groupMember', label: '组成员'}, {value: 'groupGrant', label: '组工具权限'}, {value: 'groupReviewer', label: '组审批权限'}]});
      user = field(form.fields, '用户', 'select', {choices: userChoices()}); tool = field(form.fields, '工具', 'select', {choices: tools}); resource = field(form.fields, '资源', 'select', {choices: snapshot.resources.map(item => item.name)}); allowed = field(form.fields, '加入或授权', 'checkbox');
      const fill = () => {
        user.parentElement.hidden = kind.value !== 'groupMember'; tool.parentElement.hidden = kind.value !== 'groupGrant'; resource.parentElement.hidden = kind.value !== 'groupReviewer';
        allowed.checked = kind.value === 'groupMember' ? snapshot.groupMembers.some(item => item.groupId === group.value && item.principalId === user.value) : kind.value === 'groupGrant' ? Boolean(snapshot.groupGrants.find(item => item.groupId === group.value && item.toolName === tool.value)?.allowed) : Boolean(snapshot.groupReviewers.find(item => item.groupId === group.value && item.resource === resource.value)?.allowed);
      };
      for (const input of [group, kind, user, tool, resource]) input.addEventListener('change', fill); fill(); grid.append(form.element);
    }
    parent.append(grid, listPanel('用户组成员', '当前已保存的成员关系。', ['用户组', '成员'], (snapshot.groupMembers || []).map(item => [groupName(item.groupId), identityName(item.principalId)])),
      listPanel('用户组授权', '组工具权限与组审批权限分别展示。', ['用户组', '授权类型', '目标', '授权'], [...(snapshot.groupGrants || []).map(item => [groupName(item.groupId), '业务工具', item.toolName, boolBadge(item.allowed)]), ...(snapshot.groupReviewers || []).map(item => [groupName(item.groupId), '资源审批', item.resource, boolBadge(item.allowed)])]));
  }
  function renderAudit(parent) {
    const section = panel('本人审计记录', '仅显示当前身份的事件；序号与游标以十进制字符串传递。');
    const toolbar = el('div', null, 'audit-toolbar');
    const load = button(audit.ended ? '已到当前末尾' : audit.loaded ? '读取下一页' : '读取本人事件', async () => {
      if (audit.busy || audit.ended) return;
      const expected = epoch, current = audit, after = audit.after; current.busy = true;
      try {
        const data = await api('api/audit?after=' + encodeURIComponent(after) + '&limit=25', {}, expected);
        if (current !== audit) throw stale();
        current.rows.push(...data.events); current.after = data.nextAfter; current.loaded = true; current.ended = data.events.length < 25; current.busy = false;
        if (pages[page].render === renderAudit) renderPage();
        showMessage('已读取 ' + data.events.length + ' 条本人事件。', 'success');
      } finally { current.busy = false; }
    }, {disabled: audit.ended || audit.busy, icon: 'history'});
    toolbar.append(el('span', audit.loaded ? '已载入 ' + audit.rows.length + ' 条 · 游标 ' + audit.after : '尚未读取事件', 'mg-muted'), load); section.append(toolbar);
    if (audit.loaded) section.append(table(['序号', '事件', '操作 ID', '时间'], audit.rows.map(event => [el('span', event.seq, 'mg-code'), event.eventType, event.actionId ? ui.copyID(event.actionId) : '—', ui.date(event.createdAt)]), {empty: '当前没有本人事件', label: '本人审计记录，可横向滚动'}));
    else section.append(ui.empty('等待首次读取', '点击读取本人事件后显示真实记录。', 'history'));
    parent.append(section);
  }
  function renderSettings(parent) {
    const grid = el('div', null, 'settings-grid');
    const identity = panel('身份接入', '浏览器会话独立于 coding 客户端的 MCP 身份。');
    identity.append(keyValues([['当前模式', '本机身份'], ['企业 SSO', snapshot.sso === 'PENDING_PROVIDER_SELECTION' ? badge('待选择身份源', 'neutral') : badge('状态待核对', 'warning')], ['身份 ID', ui.copyID(snapshot.principalId)], ['数据范围', snapshot.scopeId]]));
    const notification = panel('Telegram 通知', '审批操作可独立于通知送达进行。');
    notification.append(boolBadge(snapshot.notificationConfigured, '已配置', '待配置 Bot 和接收目标'), notice('通知只包含安全摘要和控制台入口，不发送业务参数、登录凭据或审批验证码。', 'neutral'));
    grid.append(identity, notification); parent.append(grid);
    if (snapshot.admin) parent.append(listPanel('通知投递', '以下是当前管理视图中的真实投递记录。', ['操作 ID', '投递状态', '尝试次数', '下次尝试'], (snapshot.notifications || []).map(item => [ui.copyID(item.actionId), badge(({PENDING: '待投递', SENDING: '投递中', SENT: '已送达', FAILED: '投递失败', SUPERSEDED: '已被后续状态替代'})[item.state] || '状态待核对', item.state === 'SENT' ? 'success' : 'warning'), item.attempts, item.nextAt ? ui.date(item.nextAt) : '—'])));
    else parent.append(notice('通知投递明细需要管理员权限。当前身份仅能查看接入配置状态。', 'neutral'));
  }
  function handleError(error) {
    if (error?.silent) return;
    showMessage(ui.errorText(error), 'warning');
  }

  ui.setErrorHandler(handleError);
  $('brandIcon').append(ui.icon('shield')); $('loginGate').append(ui.icon('shield')); $('footerIcon').append(ui.icon('shield'));
  $('navToggle').append(ui.icon('menu')); $('refresh').prepend(ui.icon('refresh'));
  $('navToggle').addEventListener('click', () => {
    const expanded = document.body.dataset.nav !== 'expanded'; document.body.dataset.nav = expanded ? 'expanded' : 'compact';
    $('navToggle').setAttribute('aria-expanded', String(expanded)); $('navToggle').setAttribute('aria-label', expanded ? '收起主导航文字' : '展开主导航文字');
  });
  $('mobileNav').addEventListener('click', () => {
    const open = document.body.dataset.menu !== 'open'; document.body.dataset.menu = open ? 'open' : 'closed';
    $('mobileNav').setAttribute('aria-expanded', String(open)); $('mobileNav').setAttribute('aria-label', open ? '收起主导航' : '展开主导航');
  });
  document.addEventListener('keydown', event => { if (event.key === 'Escape' && !ui.dialogOpen() && document.body.dataset.menu === 'open') closeMenu(true); });
  $('motionToggle').addEventListener('click', () => {
    motion = !motion; document.body.dataset.motion = motion ? 'on' : 'off'; $('motionToggle').setAttribute('aria-pressed', String(motion)); $('motionToggle').textContent = motion ? '暂停背景动效' : '恢复背景动效';
  });
  document.addEventListener('visibilitychange', () => { document.body.dataset.paused = String(document.hidden); });
  $('loginForm').addEventListener('submit', event => {
    event.preventDefault(); if (!$('loginForm').reportValidity()) return;
    ui.runButton($('loginSubmit'), async () => {
      const expected = epoch, username = $('username').value, password = $('password').value;
      try {
        await api('login', {method: 'POST', body: JSON.stringify({username, password})}, expected);
        $('password').value = ''; await readState(); renderPage(); showMessage('已登录。', 'success');
        const id = deepLinkID(); if (id) await selectAction(id, false);
      } finally { $('password').value = ''; }
    });
  });
  $('refresh').addEventListener('click', () => ui.runButton($('refresh'), async () => { await refresh(); if (!pending) showMessage('已读取当前身份下的最新状态。', 'success'); }));
  $('logout').addEventListener('click', () => ui.runButton($('logout'), async () => {
    let confirmed = false;
    try { await api('api/logout', {method: 'POST', body: '{}'}); confirmed = true; }
    finally { showLogin(confirmed ? undefined : '本地视图已清空，服务端退出响应未确认。请重新登录后核对会话。'); if (confirmed) showMessage('已退出。', 'success'); $('username').focus(); }
  }));
  window.addEventListener('hashchange', () => {
    if (!snapshot) return;
    const id = deepLinkID(); if (id) { page = 0; renderPage(); selectAction(id).catch(handleError); }
  });
  window.addEventListener('pageshow', event => {
    if (event.persisted) { showLogin('页面已恢复，请重新确认浏览器身份。'); }
  });
  readState().then(async () => { renderPage(); const id = deepLinkID(); if (id) await selectAction(id, false); }).catch(error => {
    if (error.silent) return;
    showLogin('暂时无法确认浏览器会话，可使用独立凭据重新登录。');
  });

  async function decideWrite(id, approve, trigger) {
    if (trigger) trigger.disabled = true;
    try {
      await api('api/v2/self-service/write-requests/' + encodeURIComponent(id) + '/decisions', {method: 'POST', body: JSON.stringify({approve, reason: ''})});
      showMessage(approve ? '已记录批准决定（派发仍需申请人 resume）。' : '已记录拒绝决定。', 'success');
    } catch (error) {
      showMessage(error.publicMessage || '决定未能记录。', 'danger');
    } finally { if (trigger) trigger.disabled = false; }
  }
  async function renderSelfService(parent) {
    parent.append(notice('v0.2 自助访问：数据来自服务端真实 API；不显示原生秘密、包内容或计划正文。', 'info'));
    let resources = [], credentials = [], revocations = [], writes = [];
    try { ({resources} = await api('api/v2/self-service/resources')); } catch (error) { parent.append(notice(error.publicMessage || '读取资源失败。', 'danger')); return; }
    try { ({credentials} = await api('api/v2/self-service/credentials')); } catch (error) {}
    try { ({revocations} = await api('api/v2/self-service/revocations')); } catch (error) {}
    try { ({writeRequests: writes} = await api('api/v2/self-service/write-requests')); } catch (error) {}
    parent.append(listPanel('资源与用量', '计数为当前真实状态；停用资源不影响历史记录。', ['资源', '类型', '状态', '套餐', '绑定', '授予', '凭据', '待批写'], resources.map(row => [row.resourceId, row.kind, boolBadge(row.enabled, '启用', '停用'), String(row.profiles), String(row.bindings), String(row.activeGrants), String(row.activeCredentials), String(row.pendingWrites)])));
    parent.append(listPanel('凭据', '只显示状态与到期；不含包内容或原生秘密。', ['凭据', '主体', '资源', '状态', '到期'], credentials.map(row => [ui.shortID(row.credentialId), ui.shortID(row.subjectId), ui.shortID(row.resourceId), stateBadge(row.state), row.expiresAt])));
    parent.append(listPanel('待批写', '申请人不能自批；重复决定返回明确的版本冲突。', ['计划', '状态', '资源', '操作'], writes.map(row => [ui.shortID(row.requestId), stateBadge(row.state), ui.shortID(row.resourceId), row.state === 'DEFERRED' ? button('批准', trigger => decideWrite(row.requestId, true, trigger), {small: true}) : '—'])));
    parent.append(listPanel('撤销记录', '每次撤销的影响计数来自服务端审计。', ['序号', '级别', '目标', '主体', '资源', '授予', '凭据', '时间'], revocations.map(row => [row.seq, row.kind, ui.shortID(row.targetId), row.subjectId ? ui.shortID(row.subjectId) : '—', row.resourceId ? ui.shortID(row.resourceId) : '—', String(row.grants), String(row.credentials), row.createdAt])));
    let kind, target, reason;
    const form = ui.form('执行撤销', '撤销先提交即生效；影响计数随后出现在上方记录。', '确认撤销', async trigger => {
      trigger.disabled = true;
      try {
        await api('api/v2/self-service/revocations', {method: 'POST', body: JSON.stringify({kind: kind.value, targetId: target.value.trim(), reason: reason.value})});
        showMessage('撤销已提交，记录已刷新。', 'success');
        target.value = ''; reason.value = '';
      } catch (error) { showMessage(error.publicMessage || '撤销未执行。', 'danger'); }
      finally { trigger.disabled = false; }
    });
    kind = field(form.fields, '级别', 'select', {choices: [{value: 'CREDENTIAL', label: '凭据/文件'}, {value: 'GRANT', label: '授权'}, {value: 'PRINCIPAL', label: '主体'}, {value: 'RESOURCE', label: '资源'}]});
    target = field(form.fields, '目标 ID', 'text', {required: true, maxLength: 64});
    reason = field(form.fields, '原因', 'text', {maxLength: 200});
    parent.append(form.element);
  }
})();
