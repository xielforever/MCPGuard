'use strict';

// This preview is deliberately standalone: no fetch, credentials, SDK or service connection.
const root = document.getElementById('concept-root');
const dialog = document.getElementById('decision-dialog');
const params = new URLSearchParams(location.search);
const views = {
  actions: ['操作与审批', 'actions', 'OPERATIONS / REVIEW', '核对计划、作出决定，让每一步操作都有清晰依据。'],
  resources: ['资源目录', 'database', 'RESOURCES / ACCESS', '按资源查看访问范围、账号授权与凭据状态。'],
  rules: ['策略规则', 'shield', 'POLICY / RULES', '拒绝优先，审批资格与业务权限分别管理。'],
  users: ['用户与权限', 'users', 'IDENTITY / PERMISSIONS', '在具体的资源和工具范围内，授予明确的身份权限。'],
  audit: ['本人审计', 'history', 'AUDIT / EVIDENCE', '从申请到决定、恢复和结果，按记录核对完整过程。'],
  settings: ['通知与设置', 'settings', 'SETTINGS / NOTIFICATIONS', '查看身份、通知和本机运行设置。'],
  components: ['组件与状态', 'grid', 'DESIGN / COMPONENTS', '同一视觉方向下，检查字体、按钮、表单与反馈状态。'],
};
let concept = ['a', 'b', 'c'].includes(params.get('concept')) ? params.get('concept') : 'a';
let view = Object.hasOwn(views, params.get('view')) ? params.get('view') : 'actions';
let selected = 'REQ-1029-0041';
let query = '';
let statusFilter = '';
let requesterView = false;
let returnFocus = null;
let toastTimer;

const paths = {
  actions: '<rect x="4" y="3" width="16" height="18" rx="2"/><path d="M8 7h8M8 11h8M8 15h4"/>',
  database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v7c0 4 16 4 16 0V5M4 12v7c0 4 16 4 16 0v-7"/>',
  shield: '<path d="M12 2 4 5v6c0 5 8 10 8 10s8-5 8-10V5l-8-3Z"/><path d="m8 11 3 3 5-6"/>',
  users: '<circle cx="9" cy="7" r="3"/><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 4a3 3 0 0 1 0 6M18 15a5 5 0 0 1 3 5"/>',
  history: '<path d="M3 11a9 9 0 1 1 3 7M3 4v7h7M12 7v5l3 2"/>',
  settings: '<path d="m9 3-1 3-3 1v4l2 1v3l-2 1 2 4 3-1 2 2 3-2 3 1 2-4-2-2v-3l2-1-1-4-3-1-1-3-6 0Z"/><circle cx="12" cy="12" r="3"/>',
  search: '<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>',
  arrow: '<path d="M5 12h14m-5-5 5 5-5 5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  refresh: '<path d="M20 8a8 8 0 0 0-14-3L3 8M3 3v5h5M4 16a8 8 0 0 0 14 3l3-3M16 16h5v5"/>',
  filter: '<path d="M4 5h16l-6 7v6l-4 2v-8L4 5Z"/>',
  download: '<path d="M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  empty: '<path d="M3 15h5l2 3h4l2-3h5M3 15 6 5h12l3 10v6H3v-6Z"/>',
};
const icon = name => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || paths.actions}</svg>`;
const esc = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const chip = (text, tone = '') => `<span class="chip ${tone}">${esc(text)}</span>`;
const btn = (text, action, type = '', glyph = '') => `<button type="button" class="btn ${type}" data-action="${action}">${glyph ? icon(glyph) : ''}${text}</button>`;

const actions = [
  { id: 'REQ-1029-0041', title: '更新演示备注', tool: 'pg.update_demo_note', resource: '订单分析库', native: 'PG', scope: 'scope_a', owner: 'Alice', time: '09:42', state: 'PENDING_APPROVAL', label: '等待他人审批', tone: 'warning', note: '已核对的演示备注', item: 'item_demo_1' },
  { id: 'REQ-1029-0040', title: '预留演示库存', tool: 'inventory.reserve_demo', resource: '库存服务', native: 'API', scope: 'scope_b', owner: 'Bob', time: '09:36', state: 'PENDING_APPROVAL', label: '等待他人审批', tone: 'warning', note: '数量 2 · 影响上限 10', item: 'item_demo_1' },
  { id: 'REQ-1029-0039', title: '设置演示缓存', tool: 'redis.set_demo_note', resource: '会话缓存', native: 'RD', scope: 'scope_c', owner: 'Carol', time: '09:31', state: 'PENDING_APPROVAL', label: '等待他人审批', tone: 'warning', note: '演示欢迎语', item: 'welcome' },
  { id: 'REQ-1029-0038', title: '更新演示备注', tool: 'pg.update_demo_note', resource: '客户报表库', native: 'PG', scope: 'scope_b', owner: 'Bob', time: '09:24', state: 'READY', label: '已批准 · 待恢复', tone: 'positive', note: '已批准的演示变更', item: 'item_demo_2' },
  { id: 'REQ-1029-0037', title: '预留演示库存', tool: 'inventory.reserve_demo', resource: '库存服务', native: 'API', scope: 'scope_a', owner: 'Alice', time: '09:18', state: 'OUTCOME_UNKNOWN', label: '结果待核对', tone: 'danger', note: '提交回应中断', item: 'item_demo_1' },
  { id: 'REQ-1029-0036', title: '设置演示缓存', tool: 'redis.set_demo_note', resource: '会话缓存', native: 'RD', scope: 'scope_b', owner: 'Bob', time: '09:12', state: 'READY', label: '已批准 · 待恢复', tone: 'positive', note: '演示帮助信息', item: 'help' },
  { id: 'REQ-1029-0035', title: '更新演示备注', tool: 'pg.update_demo_note', resource: '订单分析库', native: 'PG', scope: 'scope_a', owner: 'Alice', time: '08:55', state: 'SUCCEEDED', label: '已完成', tone: 'positive', note: '已确认生效的演示变更', item: 'item_demo_1' },
];
const resources = [
  { id: 'pg-orders', name: '订单分析库', subtitle: 'PostgreSQL · 业务分析', kind: 'PG', scope: 'scope_a', status: '已激活', tone: 'positive', grant: '只读访问', account: 'Alice 独立账号', description: '订单与演示数据的受控读取' },
  { id: 'pg-customers', name: '客户报表库', subtitle: 'PostgreSQL · 报表查询', kind: 'PG', scope: 'scope_b', status: '待申请', tone: 'warning', grant: '未授权', account: '尚未分配账号', description: '客户报表的独立资源与原生账号' },
  { id: 'redis-session', name: '会话缓存', subtitle: 'Redis · 演示命名空间', kind: 'RD', scope: 'scope_a', status: '已授权', tone: 'positive', grant: '受控 GET / SET', account: '固定命名空间', description: '按作用域隔离的 welcome 与 help' },
  { id: 'http-inventory', name: '库存服务', subtitle: 'HTTP · 受控接口', kind: 'API', scope: 'scope_a', status: '已授权', tone: 'positive', grant: '预留需他人审批', account: '服务端执行', description: '冻结数量与上限的库存预留' },
];
let selectedResource = 'pg-orders';

function navItems(rail = false) {
  return ['actions', 'resources', 'rules', 'users', 'audit', 'settings'].map(id => `<button class="nav-item" data-view="${id}" ${view === id ? 'aria-current="page"' : ''} ${rail ? `aria-label="${views[id][0]}" title="${views[id][0]}"` : ''}>${icon(views[id][1])}<span>${views[id][0]}</span>${id === 'actions' ? '<span class="nav-count">3</span>' : ''}</button>`).join('');
}
function identity() {
  return `<button class="btn ghost identity" data-action="identity" title="切换审批人和申请人视角"><span class="avatar">${requesterView ? 'AL' : 'LC'}</span>${requesterView ? 'Alice · 申请人' : '林辰 · 审批人'}</button>`;
}
function topbar() {
  const title = views[view][0];
  return `<header class="app-top"><div class="top-location">${concept === 'c' ? '<strong>MCPGuard</strong><span>/</span>' : '<span>工作空间</span><span>/</span>'}<strong>${title}</strong></div><div class="top-meta"><span class="environment"><i></i>本机工作空间</span>${identity()}</div></header>`;
}
function pageHeader() {
  const title = concept === 'b' && view === 'actions' ? '待决事项' : views[view][0];
  const callToAction = view === 'resources' ? btn('申请资源访问', 'apply', 'primary', 'plus') : view === 'audit' ? btn('查看记录说明', 'audit-help', '', 'history') : view === 'components' ? btn('返回审批场景', 'back', '', 'arrow') : btn('查看资源目录', 'resources', '', 'database');
  return `<div class="page-heading"><div><p class="eyebrow">${views[view][2]}</p><h1>${title}</h1><p class="page-description">${views[view][3]}</p></div><div class="heading-actions">${view === 'actions' ? btn('刷新', 'refresh', '', 'refresh') : ''}${callToAction}</div></div>`;
}
function stats() {
  const items = view === 'resources' ? [['02', 'PostgreSQL 资源', '分别管理账号与授权'], [requesterView ? '01' : '00', '已激活访问', '绑定当前用户与资源'], [requesterView ? '01' : '04', '可申请资源', '审批后领取加密引用包']] : [['03', '等待他人审批', '尚未派发业务执行'], ['02', '已批准 · 待恢复', '由原申请者显式恢复'], ['01', '结果待核对', '请勿重复提交']];
  return `<div class="stat-strip" aria-label="样例状态摘要">${items.map(([n, title, caption]) => `<div class="stat"><strong>${n}</strong><div class="stat-label">${title}<small>${caption}</small></div></div>`).join('')}</div>`;
}
function filteredActions() {
  const term = query.trim().toLowerCase();
  return actions.filter(a => (!statusFilter || a.state === statusFilter) && (!term || `${a.title} ${a.id} ${a.tool} ${a.resource} ${a.owner}`.toLowerCase().includes(term)));
}
function filters(placeholder = '搜索操作、资源或申请人') {
  return `<div class="filters"><label class="search-field" aria-label="${placeholder}">${icon('search')}<input id="query" type="search" placeholder="${placeholder}" value="${esc(query)}" autocomplete="off"></label><select id="state-filter" aria-label="筛选操作状态"><option value="">全部状态</option><option value="PENDING_APPROVAL">等待审批</option><option value="READY">已批准</option><option value="OUTCOME_UNKNOWN">待核对</option></select></div>`;
}
function actionRows() {
  const rows = filteredActions();
  if (!rows.length) return '<tr><td colspan="4"><div class="empty-state" role="status"><strong>没有匹配的操作</strong><p>试着更换关键词或清除状态筛选。</p></div></td></tr>';
  return rows.map(a => `<tr class="${a.id === selected ? 'selected' : ''}"><td><button class="row-link" data-select="${a.id}" aria-label="查看${a.title}，${a.id}"><span class="row-title">${a.title}</span><span class="row-subtitle code">${a.tool}</span></button></td><td><span>${a.resource}</span><span class="row-subtitle code">${a.scope}</span></td><td>${chip(a.label, a.tone)}</td><td><span>${a.owner}</span><span class="row-subtitle code">${a.time}</span></td></tr>`).join('');
}
function actionsTable() {
  return `<section class="panel"><div class="panel-heading"><h2>操作队列</h2><span class="count list-count">${filteredActions().length} 项样例记录</span></div>${filters()}<div class="table-scroll" tabindex="0" role="region" aria-label="操作列表，可横向滚动"><table class="data-table"><thead><tr><th scope="col">操作 / 工具</th><th scope="col">资源 / 范围</th><th scope="col">当前阶段</th><th scope="col">申请人</th></tr></thead><tbody class="action-table-body">${actionRows()}</tbody></table></div><div class="table-footer"><span>只显示当前身份有权查看的记录（设计规则）</span><span>01 / 01</span></div></section>`;
}
function queue() {
  return `<aside class="case-queue" aria-label="审批案卷目录"><div class="case-queue-heading"><strong>工作目录 / 最近事项</strong><span>07 份</span></div>${actions.slice(0, 5).map((a, i) => `<button class="queue-item ${a.id === selected ? 'selected' : ''}" data-select="${a.id}" aria-pressed="${a.id === selected}"><div class="queue-item-head"><span>案卷 0${i + 1}</span><span>${a.time}</span></div><strong>${a.title}</strong><span class="code text-secondary">${a.resource} / ${a.scope}</span><div class="queue-meta"><span>${a.owner} 发起</span>${chip(a.label, a.tone)}</div></button>`).join('')}<p class="queue-note">先核对请求内容，再记录审批决定。<br>批准与执行分别留痕。</p></aside>`;
}
function fields(a) {
  return `<dl class="detail-fields"><dt>申请人</dt><dd>${a.owner} <span class="text-muted">· 独立身份</span></dd><dt>目标资源</dt><dd>${a.resource}</dd><dt>数据范围</dt><dd class="code">${a.scope}</dd><dt>请求编号</dt><dd class="code">${a.id}</dd><dt>到期时间</dt><dd>10 月 29 日 10:12 <span class="text-muted">（样例）</span></dd></dl>`;
}
function parameters(a) {
  let content;
  if (a.native === 'PG') content = `<span class="code-key">"itemId"</span>: <span class="code-value">"${a.item}"</span>,\n<span class="code-key">"note"</span>: <span class="code-value">"${a.note}"</span>,\n<span class="code-key">"expectedVersion"</span>: <span class="code-value">"3"</span>`;
  else if (a.native === 'API') content = `<span class="code-key">"itemId"</span>: <span class="code-value">"${a.item}"</span>,\n<span class="code-key">"quantity"</span>: <span class="code-value">2</span>`;
  else content = `<span class="code-key">"key"</span>: <span class="code-value">"${a.item}"</span>,\n<span class="code-key">"value"</span>: <span class="code-value">"${a.note}"</span>,\n<span class="code-key">"ttlSeconds"</span>: <span class="code-value">300</span>`;
  return `<pre class="code-block">{\n${content.split('\n').map(line => `  ${line}`).join('\n')}\n}</pre>`;
}
function decisionNote(a) {
  if (a.state === 'OUTCOME_UNKNOWN') return '<div class="notice"><strong>结果未知 · 请勿重复提交</strong>需要依据原生记录核对结果，不能通过再次执行推断是否成功。</div>';
  if (a.state === 'READY') return '<div class="notice"><strong>已批准，业务尚未执行</strong>等待原申请者显式恢复。此页面只展示审批决定。</div>';
  if (a.state === 'SUCCEEDED') return '<div class="notice"><strong>已有完成记录</strong>结果展示来自已存储的执行记录，无需重新执行。</div>';
  return '<div class="notice"><strong>批准只记录决定，不会立即执行</strong>原申请者仍需显式恢复。目标、范围或参数变化后，应重新申请。</div>';
}
function decisionButtons(a) {
  if (a.state !== 'PENDING_APPROVAL') return btn('查看记录', 'audit', '', 'history');
  if (requesterView && a.owner === 'Alice') return '<button class="btn primary" disabled>本人申请不能自行审批</button>';
  return `${btn('拒绝', 'reject', 'danger')}${btn('批准申请', 'approve', 'primary')}`;
}
function inspector() {
  const a = actions.find(a => a.id === selected) || actions[0];
  if (concept === 'b') return `<article class="paper-case" aria-label="申请详情"><div class="paper-case-header"><div><p class="eyebrow">REQUEST / ${a.id}</p><h2>${a.title}</h2><span class="code text-secondary">${a.tool}</span></div><span class="folio">${String(actions.indexOf(a) + 1).padStart(2, '0')}</span></div><div class="paper-stage"><span>已提交</span><i></i><span class="current">${a.label}</span><i></i><span>${a.state === 'SUCCEEDED' ? '已有执行记录' : '尚未确认执行结果'}</span></div><div class="case-detail-grid"><div><div class="paper-section-title"><span>01</span><h3>核对申请</h3></div>${fields(a)}</div><div><div class="paper-section-title"><span>02</span><h3>冻结的请求参数</h3></div>${parameters(a)}<p class="text-muted code">计划版本 3 / 上限 1 次受控操作</p></div></div>${decisionNote(a)}<div class="inspector-actions"><span class="decision-hint">${requesterView && a.owner === 'Alice' ? '本请求须由其他合格人员审批' : '你正在核对他人的申请'}</span>${decisionButtons(a)}</div></article>`;
  return `<aside class="inspector" aria-label="申请详情"><div class="inspector-top"><div class="inspector-kicker"><span>REQUEST DETAIL</span>${chip(a.label, a.tone)}</div><h2>${a.title}</h2><span class="code text-secondary">${a.tool}</span></div><div class="inspector-body">${fields(a)}<div><div class="section-caption"><span>冻结的请求参数</span><span>计划 v3</span></div>${parameters(a)}</div>${decisionNote(a)}<p class="inspector-bottom">${requesterView && a.owner === 'Alice' ? '此为本人申请，不能自行审批。' : '你正在为他人作出决定。审批资格与业务读取权限分别校验。'}</p></div><div class="inspector-actions">${decisionButtons(a)}</div></aside>`;
}
function eventStrip() {
  return `<div class="event-strip"><strong>最近记录<br><span class="code text-muted">SAMPLE EVENTS</span></strong><div class="event-line"><time>09:42:18</time><em>申请已记录</em><span>Alice / ${actions[0].tool} / 尚未执行</span></div><div class="event-line"><time>09:24:06</time><em>他人审批已批准</em><span>Bob 的申请 / READY / 等待原申请者恢复</span></div><div class="event-line"><time>09:18:32</time><span>库存服务 / OUTCOME_UNKNOWN / 需要人工核对</span></div></div>`;
}
function actionsView() {
  return `${stats()}<div class="workspace-grid ${concept === 'b' ? 'case-layout' : ''}"><div>${concept === 'b' ? queue() : actionsTable()}${concept === 'c' ? eventStrip() : ''}</div>${inspector()}</div>`;
}
function resourcesView() {
  const currentResources = requesterView ? resources : resources.map(item => ({...item, status: '待申请', tone: 'warning', grant: '未授予业务访问', account: '尚未分配业务账号'}));
  const r = currentResources.find(r => r.id === selectedResource) || currentResources[0];
  return `${stats()}<div class="workspace-grid resource-grid"><section class="panel"><div class="panel-heading"><h2>可见资源</h2><span class="count">04 个样例资源</span></div><div class="filters"><span class="text-secondary code">资源独立 / 账号独立 / 按当前身份授权</span></div><div class="table-scroll" tabindex="0" role="region" aria-label="资源目录，可横向滚动"><table class="data-table"><thead><tr><th scope="col">资源名称</th><th scope="col">访问范围</th><th scope="col">账号与授权</th><th scope="col">状态</th></tr></thead><tbody>${currentResources.map(item => `<tr class="${item.id === selectedResource ? 'selected' : ''}"><td><button class="row-link row-label" data-resource="${item.id}" aria-label="查看资源${item.name}"><span class="resource-icon">${item.kind}</span><span><span class="row-title">${item.name}</span><span class="row-subtitle">${item.subtitle}</span></span></button></td><td class="code">${item.scope}</td><td><span>${item.grant}</span><span class="row-subtitle">${item.account}</span></td><td>${chip(item.status, item.tone)}</td></tr>`).join('')}</tbody></table></div><div class="table-footer"><span>申请、审批、交付、激活分别显示状态</span><span>01 / 01</span></div></section><aside class="panel resource-summary"><span class="resource-icon">${r.kind}</span><h2>${r.name}</h2><p>${r.description}</p><dl class="detail-fields"><dt>当前身份</dt><dd>${requesterView ? 'Alice' : '林辰'}</dd><dt>资源范围</dt><dd class="code">${r.scope}</dd><dt>访问权限</dt><dd>${r.grant}</dd><dt>凭据状态</dt><dd>${chip(r.status, r.tone)}</dd></dl><h3>访问路径</h3><ol class="resource-steps"><li><span>申请资源账号及权限<small>明确资源、范围与用途</small></span></li><li><span>由有资格的他人审批<small>审批不等于已开通访问</small></span></li><li><span>客户端私有领取与激活<small>模型只接收安全状态信息</small></span></li><li><span>服务端校验后执行<small>撤销权限后拒绝后续访问</small></span></li></ol>${btn(r.status === '待申请' ? '申请此资源访问' : '查看访问详情', 'apply', 'primary', 'arrow')}</aside></div>`;
}
function auditView() {
  const entries = [
    ['1048', '09:42:18', '申请已记录', 'REQ-1029-0041', 'PENDING_APPROVAL', 'warning'],
    ['1047', '09:24:06', '他人审批已批准', 'REQ-1029-0038', 'READY', 'positive'],
    ['1046', '09:18:32', '执行结果待核对', 'REQ-1029-0037', 'OUTCOME_UNKNOWN', 'danger'],
    ['1045', '09:12:11', '他人审批已批准', 'REQ-1029-0036', 'READY', 'positive'],
    ['1044', '08:55:09', '已记录成功结果', 'REQ-1029-0035', 'SUCCEEDED', 'positive'],
  ];
  return `<div class="workspace-grid audit-grid"><section class="panel"><div class="panel-heading"><h2>本人可见的审计记录</h2><span class="count">2026.10.29 / 样例</span></div><div class="filters"><span class="text-secondary">事件、审批与执行结果分别留痕。</span></div><div class="table-scroll" tabindex="0" role="region" aria-label="审计记录，可横向滚动"><table class="data-table"><thead><tr><th scope="col">序号 / 时间</th><th scope="col">事件</th><th scope="col">操作编号</th><th scope="col">对应阶段</th></tr></thead><tbody>${entries.map(([id, time, title, req, status, tone]) => `<tr><td><span class="code">${id}</span><span class="row-subtitle code">${time}</span></td><td>${title}</td><td><button class="row-link code" data-select="${req}">${req}</button></td><td>${chip(status, tone)}</td></tr>`).join('')}</tbody></table></div><div class="table-footer"><span>顺序追加的历史记录</span>${btn('读取下一页 · 预览', 'next-page', 'small')}</div></section><aside class="panel audit-summary"><p class="eyebrow">RECORD / 1047</p><h2>决定与执行分开核对</h2><ol class="timeline"><li><time>09:23:40</time><strong>申请已提交</strong><p>冻结工具、目标与参数。</p></li><li><time>09:24:06</time><strong>他人审批已批准</strong><p>只保存审批决定，阶段变为 READY。</p></li><li><time>尚未发生</time><strong>原申请者恢复执行</strong><p>仍需重新校验当前权限与前置条件。</p></li></ol><div class="notice"><strong>批准成功 ≠ 业务已执行</strong>查看实际执行记录与副作用状态后，再判断最终结果。</div></aside></div>`;
}
function rulesView() {
  return `<section class="panel"><div class="panel-heading"><h2>当前规则 · 设计样例</h2>${btn('新增规则', 'new-rule', 'primary', 'plus')}</div><div class="filters"><span class="text-secondary">DENY 优先；ALLOW 不授予缺失的权限，也不会跳过受控写审批。</span></div><div class="table-scroll" tabindex="0" role="region" aria-label="策略规则，可横向滚动"><table class="data-table"><thead><tr><th scope="col">规则</th><th scope="col">受控工具</th><th scope="col">策略</th><th scope="col">状态</th><th scope="col">操作</th></tr></thead><tbody><tr><td>高危操作默认拒绝</td><td class="code">受控目录中的高危工具</td><td>${chip('拒绝', 'danger')}</td><td>启用</td><td>${btn('查看规则', 'rule-detail', 'small')}</td></tr><tr><td>业务变更由他人审批</td><td class="code">pg.update_demo_note</td><td>${chip('需他人审批', 'warning')}</td><td>启用</td><td>${btn('查看规则', 'rule-detail', 'small')}</td></tr><tr><td>授权范围内读取</td><td class="code">pg.get_demo_item</td><td>${chip('按权限读取', 'positive')}</td><td>启用</td><td>${btn('查看规则', 'rule-detail', 'small')}</td></tr></tbody></table></div></section>`;
}
function usersView() {
  return `<section class="panel"><div class="panel-heading"><h2>用户与角色 · 设计样例</h2>${btn('查看用户组', 'groups', '', 'users')}</div><div class="table-scroll" tabindex="0" role="region" aria-label="用户权限，可横向滚动"><table class="data-table"><thead><tr><th scope="col">用户</th><th scope="col">业务数据范围</th><th scope="col">业务读取</th><th scope="col">审批资格</th><th scope="col">操作</th></tr></thead><tbody><tr><td>Alice<span class="row-subtitle">业务申请人</span></td><td class="code">scope_a</td><td>${chip('已授权', 'positive')}</td><td>不能审批本人申请</td><td>${btn('查看权限', 'permissions', 'small')}</td></tr><tr><td>Bob<span class="row-subtitle">业务申请人</span></td><td class="code">scope_b</td><td>${chip('已授权', 'positive')}</td><td>按具体资源授权</td><td>${btn('查看权限', 'permissions', 'small')}</td></tr><tr><td>林辰<span class="row-subtitle">独立审批身份</span></td><td>未授予业务读取</td><td>${chip('无读取权限')}</td><td>${chip('指定资源审批', 'positive')}</td><td>${btn('查看权限', 'permissions', 'small')}</td></tr></tbody></table></div><div class="table-footer"><span>审批资格不等于业务数据读取权。用户与资源绑定分别管理。</span></div></section>`;
}
function settingsView() {
  return `<div class="workspace-grid"><section class="panel"><div class="panel-heading"><h2>通知配置</h2>${chip('待配置', 'warning')}</div><form class="settings-form" id="settings-form"><label>通知渠道<select><option>Telegram</option></select></label><label>接收目标<input placeholder="由操作员在私有配置中填写" readonly><small>预览不读取或保存 Bot 密钥。</small></label><label>消息范围<input value="操作编号、安全摘要与控制台入口" readonly></label><div class="notice"><strong>审批不依赖通知送达</strong>尚未配置通知时，申请和审批记录仍需保持完整可查。</div><div class="heading-actions" style="margin-top:20px">${btn('查看配置说明', 'settings-help', 'primary')}${btn('测试通知 · 仅预览', 'notify-test')}</div></form></section><aside class="panel resource-summary"><p class="eyebrow">WORKSPACE</p><h2>本机身份模式</h2><dl class="detail-fields"><dt>浏览器身份</dt><dd>独立登录凭据</dd><dt>企业 SSO</dt><dd>待选择身份源</dd><dt>模型可见</dt><dd>安全结果与流程状态</dd><dt>凭据交付</dt><dd>客户端私有通道</dd></dl><p>此处只展示设计语义，不表示真实服务的当前配置。</p></aside></div>`;
}
function componentsView() {
  return `<div class="component-grid"><section class="component-block"><h2>01 / 字体与颜色</h2><p class="component-caption">中文界面优先系统字体；工具名称、版本与编号使用等宽字体。</p><h1 style="font-size:26px">让授权与执行清晰可见</h1><p class="text-secondary">正文层级 / 14px · 信息分组与阅读节奏</p><p class="code text-muted">REQ-1029-0041 / pg.update_demo_note</p><div class="palette">${[['主操作', '--accent'], ['正文', '--ink'], ['工作底色', '--canvas'], ['状态：待批', '--warning'], ['状态：异常', '--danger']].map(([name, token]) => `<span><i style="background:var(${token})"></i>${name}</span>`).join('')}</div></section><section class="component-block"><h2>02 / 按钮与交互</h2><div class="component-row">${btn('主要操作', 'component-demo', 'primary')}${btn('次要操作', 'component-demo')}${btn('危险操作', 'component-demo', 'danger')}</div><div class="component-row"><button class="btn primary" disabled>无审批资格</button><button class="btn" disabled><span class="spinner"></span>正在提交</button>${btn('小尺寸', 'component-demo', 'small')}</div><p class="component-caption">Tab 可检查可见焦点；禁用与加载状态使用原生禁用语义。</p></section><section class="component-block"><h2>03 / 表单与错误</h2><form class="component-form" onsubmit="return false"><label>资源名称<input value="订单分析库"></label><label>申请用途<input value="" aria-invalid="true" aria-describedby="field-error" placeholder="填写实际用途"><span class="field-error" id="field-error">请说明需要访问此资源的用途。</span></label>${btn('提交申请 · 预览', 'apply', 'primary')}</form></section><section class="component-block"><h2>04 / 状态与结果语义</h2><div class="component-row">${chip('等待他人审批', 'warning')}${chip('已批准 · 待恢复', 'positive')}${chip('结果待核对', 'danger')}${chip('已撤销')}</div><div class="notice"><strong>结果未知 · 请勿重复提交</strong>展示核对入口，不提供一键重试或重新执行。</div><p class="component-caption" style="margin-top:14px">状态同时以文字、符号与结构表达，不能仅靠颜色区分。</p></section><section class="component-block"><h2>05 / 空状态与加载</h2><div class="empty-state">${icon('empty')}<strong>暂无待你处理的申请</strong><p>新的申请到达后会出现在此处。</p>${btn('查看资源目录', 'resources')}</div><div class="loading-preview" role="status"><span class="spinner"></span>正在读取申请记录…（状态样例）</div></section><section class="component-block"><h2>06 / 审批确认</h2><p class="component-caption">确认前展示申请人、资源、冻结参数与决定影响；关闭后焦点回到触发按钮。</p><dl class="detail-fields"><dt>申请人</dt><dd>Alice</dd><dt>目标资源</dt><dd>订单分析库 / scope_a</dd><dt>决定影响</dt><dd>只记录决定；等待原申请者恢复</dd></dl>${btn('体验审批确认', 'approve', 'primary', 'shield')}</section></div>`;
}
function content() {
  return ({ actions: actionsView, resources: resourcesView, audit: auditView, rules: rulesView, users: usersView, settings: settingsView, components: componentsView })[view]();
}
function footnote() {
  return `<footer class="page-footnote"><span>${icon('shield')}每次访问仍需校验当前身份、资源与授权状态</span><span>设计样例 / 无真实业务执行</span></footer>`;
}
function render() {
  document.body.dataset.theme = concept;
  root.dataset.theme = concept;
  document.querySelectorAll('[data-concept]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.concept === concept)));
  const workspace = `<main class="workspace" id="design-content" tabindex="-1">${concept === 'b' ? '<div class="paper-context"><span>工作记录 / MCPGuard 本机工作空间</span><span>2026 年 10 月 29 日 · 样例案卷</span></div>' : ''}${pageHeader()}${content()}${footnote()}</main>`;
  if (concept === 'a') root.innerHTML = `<div class="app-shell concept-a"><aside class="sidebar"><div class="brand"><span class="brand-mark">${icon('shield')}</span><div><strong>MCPGuard</strong><small>RESOURCE WORKSPACE</small></div></div><p class="nav-caption">WORKSPACE / 工作空间</p><nav aria-label="主导航">${navItems()}</nav><div class="sidebar-divider"></div><div class="sidebar-note"><strong>权限应有清晰边界</strong>申请、审批与执行分别校验，<br>每一步都有可核对的记录。</div><div class="sidebar-footer">本机工作空间<span>LOCAL / DESIGN PREVIEW</span></div></aside><div class="workspace-shell">${topbar()}${workspace}</div></div>`;
  else if (concept === 'b') root.innerHTML = `<div class="app-shell concept-b"><header class="masthead"><div class="paper-brand"><strong>MCPGuard</strong><span>授权案卷</span></div><nav aria-label="主导航">${navItems()}</nav>${identity()}</header>${workspace}</div>`;
  else root.innerHTML = `<div class="app-shell concept-c"><aside class="icon-rail"><div class="rail-logo">MG</div><nav aria-label="主导航">${navItems(true)}</nav><span class="rail-bottom">CONTROL / VERIFY / RECORD</span></aside><div class="workspace-shell">${topbar()}${workspace}</div></div>`;
  const stateSelect = document.getElementById('state-filter');
  if (stateSelect) stateSelect.value = statusFilter;
  const next = new URL(location.href);
  next.searchParams.set('concept', concept);
  next.searchParams.set('view', view);
  history.replaceState(null, '', next);
  document.title = `MCPGuard · 方案 ${concept.toUpperCase()} · ${views[view][0]}`;
}
function navigate(nextView) {
  view = nextView;
  query = '';
  statusFilter = '';
  render();
  document.getElementById('design-content').focus({ preventScroll: true });
}
function toast(text) {
  const el = document.getElementById('toast');
  clearTimeout(toastTimer);
  el.textContent = text;
  el.hidden = false;
  toastTimer = setTimeout(() => { el.hidden = true; }, 5200);
}
function openDecision(reject, trigger) {
  const a = actions.find(a => a.id === selected) || actions[0];
  const title = document.getElementById('decision-title');
  const confirm = dialog.querySelector('[value="confirm"]');
  title.textContent = reject ? '确认拒绝这份申请？' : '确认批准这份申请？';
  confirm.textContent = reject ? '确认拒绝 · 仅预览' : '确认批准 · 仅预览';
  dialog.querySelector('.dialog-summary strong').textContent = reject ? '拒绝会终止当前申请的审批路径' : '批准只记录决定';
  dialog.querySelector('.dialog-summary span').textContent = `${a.owner} / ${a.resource} / ${a.scope}。${reject ? '' : '原申请者仍需显式恢复执行。'}本预览不保存决定，也不发送业务请求。`;
  document.getElementById('decision-reason').value = '';
  dialog.returnValue = '';
  returnFocus = trigger;
  dialog.showModal();
}
function updateRows() {
  const body = root.querySelector('.action-table-body');
  if (body) body.innerHTML = actionRows();
  const count = root.querySelector('.list-count');
  if (count) count.textContent = `${filteredActions().length} 项样例记录`;
}

document.addEventListener('click', event => {
  const trigger = event.target.closest('button');
  if (!trigger || trigger.disabled) return;
  if (trigger.dataset.concept) { concept = trigger.dataset.concept; render(); return; }
  if (trigger.dataset.view) { navigate(trigger.dataset.view); return; }
  if (trigger.dataset.select) { selected = trigger.dataset.select; view = 'actions'; render(); root.querySelector('[aria-label="申请详情"]').scrollIntoView({ behavior: 'auto', block: 'nearest' }); return; }
  if (trigger.dataset.resource) { selectedResource = trigger.dataset.resource; render(); return; }
  const action = trigger.dataset.action;
  if (['resources', 'audit'].includes(action)) { navigate(action); return; }
  if (action === 'back') { navigate('actions'); return; }
  if (action === 'identity') { requesterView = !requesterView; render(); toast(requesterView ? '已切换为 Alice 的样例视角，本人申请不能自行审批。' : '已切换为独立审批人的样例视角。'); return; }
  if (action === 'approve' || action === 'reject') { openDecision(action === 'reject', trigger); return; }
  const messages = {
    refresh: '预览已刷新。展示的是固定样例，不代表真实服务状态。',
    apply: '资源申请设计：选择资源 → 说明账号与权限范围 → 他人审批 → 客户端私有领取与激活。本预览不提交申请。',
    'audit-help': '审计应展示当前身份可见的记录。审批已批准和业务已执行分别核对。',
    'next-page': '已到样例记录末页。正式实现会使用服务端游标读取。',
    'new-rule': '规则表单沿用统一组件；写入时仍需管理员权限、配置版本和幂等校验。本预览不创建规则。',
    'rule-detail': '规则详情应展示匹配范围、效果、版本和启用状态。本预览不修改策略。',
    groups: '用户组设计应区分成员、工具权限和审批资格。本预览不授予权限。',
    permissions: '权限按用户、资源和工具分别展示，审批资格不会隐含授予业务读取权。',
    'settings-help': '正式页面只展示安全配置状态；密钥经私有配置通道管理。',
    'notify-test': '仅演示通知反馈，本预览没有发送通知。',
    'component-demo': '这是组件交互演示，没有连接业务服务。',
  };
  if (messages[action]) toast(messages[action]);
});
root.addEventListener('input', event => { if (event.target.id === 'query') { query = event.target.value; updateRows(); } });
root.addEventListener('change', event => { if (event.target.id === 'state-filter') { statusFilter = event.target.value; updateRows(); } });
document.getElementById('show-components').addEventListener('click', () => navigate(view === 'components' ? 'actions' : 'components'));
dialog.addEventListener('close', () => {
  if (dialog.returnValue === 'confirm') toast('已完成审批确认演示。没有保存决定，也没有派发业务执行。');
  if (returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
});
render();
