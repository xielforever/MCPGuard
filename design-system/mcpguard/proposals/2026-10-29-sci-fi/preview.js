'use strict';

// Fixed, synthetic examples only. This file has no service or credential integration.
const app = document.getElementById('app');
const dialog = document.getElementById('confirmation');
const motionButton = document.getElementById('motion-toggle');
const urlState = new URLSearchParams(location.search);
const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)');
const compactView = matchMedia('(max-width:650px)');
let concept = urlState.get('concept') === 'orbit' ? 'orbit' : 'bridge';
let selected = urlState.get('request') || 'write';
let detailTab = 'change';
let requesterView = false;
let query = '';
let motion = urlState.get('motion') !== 'off' && !reduceMotion.matches;
let toastTimer;
let dialogTrigger;

const icons = {
  shield: '<path d="M12 2 4 5v6c0 5 8 10 8 10s8-5 8-10V5l-8-3Z"/><path d="m8 11 3 3 5-6"/>',
  database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v7c0 4 16 4 16 0V5M4 12v7c0 4 16 4 16 0v-7"/>',
  orbit: '<circle cx="12" cy="12" r="3"/><ellipse cx="12" cy="12" rx="10" ry="5" transform="rotate(-35 12 12)"/><path d="M12 2v3M12 19v3"/>',
  layers: '<path d="m12 3 10 6-10 6L2 9l10-6ZM2 14l10 6 10-6"/>',
  history: '<path d="M3 11a9 9 0 1 1 3 7M3 4v7h7M12 7v5l3 2"/>',
  arrow: '<path d="M4 12h16m-6-6 6 6-6 6"/>',
  search: '<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>',
  user: '<circle cx="12" cy="7" r="4"/><path d="M4 22v-3a8 8 0 0 1 16 0v3"/>',
  command: '<path d="m5 5 7 7-7 7M14 19h7"/>',
  check: '<path d="m4 12 5 5L20 6"/>',
};
const icon = key => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${icons[key] || icons.shield}</svg>`;
const escapeText = value => String(value).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const button = (label, action, cls = '', glyph = '') => `<button type="button" class="btn ${cls}" data-action="${action}">${glyph ? icon(glyph) : ''}${label}</button>`;
const status = r => `<span class="status ${r.tone}">${r.label}</span>`;

const requests = [
  {key: 'write', id: 'REQ-1029-0041', owner: 'Alice', title: '更新演示备注', tool: 'pg.update_demo_note', resource: '订单分析库', resourceCode: 'PG-01', scope: 'scope_a', type: 'write', state: 'PENDING_APPROVAL', label: '等待他人审批', tone: '', time: '09:42', center: '等待审批', after: '已核对的演示备注', before: '待核对的演示备注', impact: '最多 1 行', native: 'Alice 独立账号', expectedVersion: '3'},
  {key: 'access', id: 'REQ-1029-0042', owner: 'Bob', title: '申请报表库只读权限', tool: '资源账号及权限申请', resource: '客户报表库', resourceCode: 'PG-02', scope: 'scope_b', type: 'access', state: 'PENDING_APPROVAL', label: '等待他人审批', tone: '', time: '09:37', center: '等待授权', impact: '只读访问', native: '尚未分配原生账号', purpose: '核对本周客户报表'},
  {key: 'unknown', id: 'REQ-1029-0037', owner: 'Alice', title: '预留演示库存', tool: 'inventory.reserve_demo', resource: '库存服务', resourceCode: 'API-01', scope: 'scope_a', type: 'unknown', state: 'OUTCOME_UNKNOWN', label: '结果待核对', tone: 'danger', time: '09:18', center: '结果未知', impact: '数量 2', native: '服务端执行'},
  {key: 'ready', id: 'REQ-1029-0038', owner: 'Bob', title: '更新演示备注', tool: 'pg.update_demo_note', resource: '客户报表库', resourceCode: 'PG-02', scope: 'scope_b', type: 'write', state: 'READY', label: '已批准 · 待恢复', tone: 'positive', time: '09:12', center: '等待恢复', after: '本周报表已核对', before: '等待本周报表核对', impact: '最多 1 行', native: 'Bob 独立账号', expectedVersion: '3'},
];
if (!requests.some(r => r.key === selected)) selected = 'write';
const visibleRequests = () => requesterView ? requests.filter(r => r.owner === 'Alice') : requests;
const currentRequest = () => visibleRequests().find(r => r.key === selected) || visibleRequests()[0];

function identity() {
  return `<button class="identity-button" data-action="identity" aria-label="切换审批人和申请人样例视角"><span class="avatar">${requesterView ? 'AL' : 'LC'}</span><span>${requesterView ? 'Alice · 申请人' : '林辰 · 审批人'}</span></button>`;
}
function scenarioTabs() {
  const r = currentRequest();
  return `<nav class="scenario-tabs" aria-label="审批场景">${[['write', '受控写审批', 'command'], ['access', '资源权限申请', 'database'], ['unknown', '未知结果核对', 'history']].map(([key, label, glyph]) => `<button data-request="${key}" aria-pressed="${r.key === key}" ${!visibleRequests().some(r => r.key === key) ? 'disabled title="当前样例身份没有此类可见请求"' : ''}>${icon(glyph)}${label}</button>`).join('')}</nav>`;
}
function counts() {
  const list = visibleRequests();
  return `<div class="hero-counts" aria-label="当前身份的样例请求数量">${[['PENDING_APPROVAL', '等待审批'], ['READY', '等待恢复'], ['OUTCOME_UNKNOWN', '结果待核对']].map(([state, text]) => `<div class="hero-stat"><strong>${String(list.filter(r => r.state === state).length).padStart(2, '0')}</strong><span>${text}</span></div>`).join('')}</div>`;
}
function hero() {
  return `<div class="hero"><div><p class="eyebrow">${concept === 'bridge' ? '授权边界 · 决定中枢' : '轨道中枢 · 请求审阅'}</p><h1>${concept === 'bridge' ? '审批舰桥' : '每一次授权，边界清晰。'}</h1><p class="hero-description">让申请、审批与执行沿着清晰的轨迹前进。</p></div>${counts()}</div>`;
}
function queueSearch() {
  return `<label class="queue-search" aria-label="搜索可见申请">${icon('search')}<input type="search" id="queue-search" value="${escapeText(query)}" placeholder="搜索申请或资源" autocomplete="off"></label>`;
}
function queueItems() {
  const term = query.trim().toLowerCase();
  const list = visibleRequests().filter(r => !term || `${r.title} ${r.owner} ${r.resource} ${r.id}`.toLowerCase().includes(term));
  if (!list.length) return '<div class="empty-queue" role="status">没有匹配的可见申请。请更换关键词。</div>';
  return list.map(r => `<button class="request-button" data-request="${r.key}" aria-pressed="${currentRequest().key === r.key}" aria-label="查看${r.title}，${r.id}"><span class="queue-index"><span>${r.id}</span><span>${r.time}</span></span><strong>${r.title}</strong><span class="resource-line">${r.resource} / ${r.scope}</span><span class="queue-meta"><span>${r.owner} 发起</span>${status(r)}</span></button>`).join('');
}
function queue() {
  return `<aside class="queue-panel panel" aria-label="申请队列"><div class="queue-header"><h2>请求舱 / 可见申请</h2><span>${String(visibleRequests().length).padStart(2, '0')} 份样例</span>${concept === 'orbit' ? queueSearch() : ''}</div>${concept === 'bridge' ? queueSearch() : ''}<div class="queue-list">${queueItems()}</div><div class="queue-footer">按当前身份显示请求。<br>审批资格不等于业务读取权。</div></aside>`;
}
function connection(r) {
  return `<div class="connection-view"><svg class="connection-figure" viewBox="${compactView.matches ? '230 0 320 266' : '0 0 780 266'}" role="img" aria-labelledby="map-title map-description"><title id="map-title">申请人、审批阶段与目标资源</title><desc id="map-description">${r.owner} 为 ${r.resource} 提交申请，当前${r.label}。轨道图为装饰性关系示意，不代表网络或数据库实时状态。</desc><defs><pattern id="map-grid" width="30" height="30" patternUnits="userSpaceOnUse"><path d="M 30 0 L 0 0 0 30" fill="none" stroke="currentColor" stroke-opacity=".06" stroke-width=".7"/></pattern></defs><rect x="0" y="12" width="780" height="242" fill="url(#map-grid)"/><path class="orbit-line" d="M113 133h139l24-24h24"/><path class="orbit-line pending" d="M482 133h45l27 25h114"/><circle class="orbit-outline" cx="390" cy="133" r="110"/><circle class="orbit-outline orbit-dashed" cx="390" cy="133" r="96"/><ellipse class="orbit-outline" cx="390" cy="133" rx="144" ry="55" transform="rotate(-25 390 133)"/><g class="rotating-orbit"><circle class="orbit-outline" cx="390" cy="133" r="121" stroke-dasharray="45 16 2 24 30 640"/><circle class="orbit-point" cx="390" cy="12" r="3.6"/></g><path class="orbit-core" d="m390 64 60 34v70l-60 34-60-34V98Z"/><path class="orbit-core-inner" d="m390 74 51 29v60l-51 29-51-29v-60Z"/><text class="map-center" x="390" y="131" text-anchor="middle">${r.center}</text><text class="map-caption" x="390" y="155" text-anchor="middle">${r.state === 'OUTCOME_UNKNOWN' ? '请勿重复提交' : r.type === 'access' ? '授权尚未生效' : '决定与执行分离'}</text><rect class="orbit-node" x="63" y="104" width="61" height="58" rx="11"/><path d="M87 129a8 8 0 1 1 14 0m-21 18a14 14 0 0 1 28 0" fill="none" stroke="currentColor" stroke-width="1.3"/><text class="map-label" x="94" y="189" text-anchor="middle">${r.owner}</text><text class="map-caption" x="94" y="210" text-anchor="middle">独立申请身份</text><rect class="orbit-node" x="662" y="130" width="61" height="58" rx="11"/><ellipse cx="693" cy="148" rx="13" ry="5" fill="none" stroke="currentColor"/><path d="M680 148v15c0 7 26 7 26 0v-15m-26 7c0 7 26 7 26 0" fill="none" stroke="currentColor"/><text class="map-label" x="692" y="212" text-anchor="middle">${r.resource}</text><text class="map-mono" x="692" y="234" text-anchor="middle">${r.resourceCode} / ${r.scope}</text><text class="map-caption" x="203" y="119" text-anchor="middle">冻结的申请</text><text class="map-caption" x="575" y="142" text-anchor="middle">受控访问</text><path d="M17 25h16M25 17v16M747 241h16M755 233v16" stroke="currentColor" stroke-opacity=".4" stroke-width="1"/></svg><div class="connection-caption">${concept === 'orbit' ? '<strong>先决定，再前行。</strong>' : ''}<span>身份与资源分别绑定</span><span>轨道示意 / 无实时连接</span></div></div>`;
}
function brief(r) {
  return `<dl class="brief-grid"><div><dt>申请人 / 身份</dt><dd>${r.owner}<small>${r.type === 'access' ? '账号尚未开通' : r.native}</small></dd></div><div><dt>目标资源 / 范围</dt><dd>${r.resource}<small class="code">${r.scope}</small></dd></div><div><dt>操作边界</dt><dd>${r.impact}<small>${r.type === 'access' ? '无写入与管理权限' : r.type === 'unknown' ? '不能推断是否生效' : '执行前校验预期版本'}</small></dd></div></dl>`;
}
function frozenArguments(r) {
  if (r.type === 'access') return {resource: 'pg-customers', requestedScope: r.scope, permission: 'READ_ONLY', purpose: r.purpose};
  if (r.type === 'unknown') return {itemId: 'item_demo_1', quantity: 2};
  return {itemId: 'item_demo_1', note: r.after, expectedVersion: r.expectedVersion};
}
function timeline(r) {
  const events = r.state === 'OUTCOME_UNKNOWN' ? [['09:16', '申请获他人批准', '批准决定已记录，随后由原申请者恢复。'], ['09:18', '执行响应中断', '无法仅凭网络错误确定副作用是否发生。'], ['待核对', '依据原生记录确认结果', '保留原申请与执行资格，禁止自动重放。']] : r.state === 'READY' ? [['09:10', '申请已记录', '目标、参数与范围已经冻结。'], ['09:12', '他人审批已批准', '阶段为 READY，业务执行尚未发生。'], ['尚未发生', '原申请者显式恢复', '执行前重新校验当前权限与前置条件。']] : [['已发生', '申请已记录', '以下均为固定样例，不表示真实服务状态。'], ['当前阶段', '等待合格他人审批', '当前页面只记录决定，不派发业务执行。'], ['尚未发生', r.type === 'access' ? '开通、私有交付与激活' : '原申请者显式恢复', r.type === 'access' ? '批准不等于已开通访问，更不等于已领取凭据。' : '恢复时仍需重验身份、资源与授权。']];
  return `<ol class="timeline">${events.map(([time, title, description]) => `<li><time>${time}</time><div><strong>${title}</strong><p>${description}</p></div></li>`).join('')}</ol>`;
}
function payload(r) {
  if (detailTab === 'parameters') return `<div class="section-title"><h3>冻结参数 / 设计样例</h3><span>只读文本</span></div><pre>${escapeText(JSON.stringify(frozenArguments(r), null, 2))}</pre><p class="field-note">${r.type === 'access' ? '这里是流程设计字段示意，不作为已实现 API 契约。' : '冻结参数用于审阅，不能在审批时直接修改业务输入。'}</p>`;
  if (detailTab === 'timeline') return timeline(r);
  if (r.type === 'unknown') return `<div class="notice danger" style="margin-top:0"><strong>结果未知 · 请勿重复提交</strong><span>请求可能已经产生副作用。应依据原生记录核对结果，不能通过再次执行来判断上一次是否成功。</span></div><div class="bounds"><span>保留原执行记录</span><span>无自动重放</span><span>尚未确认最终结果</span></div>`;
  if (r.type === 'access') return `<div class="section-title"><h3>申请的账号与权限</h3><span>尚未生效</span></div><dl class="permission-list"><dt>原生身份</dt><dd>为 Bob 分配独立资源账号</dd><dt>权限范围</dt><dd>客户报表库 / ${r.scope} / 只读</dd><dt>不包含</dt><dd>写入、管理、其他资源权限</dd><dt>申请用途</dt><dd>${r.purpose}</dd></dl><div class="bounds"><span>他人审批</span><span>账号开通</span><span>私有领取</span><span>激活后访问</span></div>`;
  return `<div class="section-title"><h3>变更内容 / note</h3><span>预期版本 ${r.expectedVersion}</span></div><div class="diff-grid"><div class="diff-box"><div class="diff-caption"><span aria-hidden="true">−</span>变更前 · 样例快照</div><p>${r.before}</p></div><div class="diff-box new"><div class="diff-caption"><span aria-hidden="true">＋</span>变更后 · 申请内容</div><p>${r.after}</p></div></div><p class="field-note">快照仅辅助审阅。执行时仍以 expectedVersion 校验，影响上限为一行。</p><div class="bounds"><span>固定目标 item_demo_1</span><span>禁止扩大范围</span><span>批准后仍需恢复</span></div>`;
}
function requestBody() {
  const r = currentRequest();
  return `<article class="request-body panel" aria-labelledby="request-title"><header class="request-title"><div><div class="overline">申请 ${r.id}</div><h2 id="request-title" tabindex="-1">${r.title}</h2><span class="code secondary">${r.tool}</span></div>${status(r)}</header>${connection(r)}${brief(r)}<div class="detail-tabs" role="tablist" aria-label="申请详情">${[['change', r.type === 'access' ? '账号与权限' : '变更内容'], ['parameters', '冻结参数'], ['timeline', '决定轨迹']].map(([key, name]) => `<button role="tab" id="tab-${key}" data-tab="${key}" aria-controls="payload" aria-selected="${detailTab === key}" tabindex="${detailTab === key ? '0' : '-1'}">${name}</button>`).join('')}</div><section class="payload" id="payload" role="tabpanel" aria-labelledby="tab-${detailTab}">${payload(r)}</section></article>`;
}
function decisionPanel() {
  const r = currentRequest();
  const pending = r.state === 'PENDING_APPROVAL';
  const title = pending ? '等待你的决定' : r.state === 'READY' ? '决定已记录' : '等待结果核对';
  const note = r.state === 'OUTCOME_UNKNOWN' ? ['结果未知 · 请勿重复提交', '保留原申请与执行记录，依据原生证据确认结果。'] : r.state === 'READY' ? ['已批准，业务尚未执行', '原申请者仍需显式恢复，审批人不能代为派发。'] : r.type === 'access' ? ['批准不等于已开通访问', '后续还需账号开通、客户端私有领取和激活。'] : ['批准只记录决定，不会立即执行', '原申请者仍需显式恢复，并重新校验当前权限。'];
  const actions = pending ? requesterView ? '<button type="button" class="btn" disabled>本人申请不能自行审批</button>' : `${button('批准申请', 'approve', 'primary', 'arrow')}<button class="reject-link" data-action="reject">拒绝这份申请</button>` : button(r.state === 'READY' ? '查看决定轨迹' : '查看核对记录', 'timeline', '', 'history');
  return `<aside class="decision-panel panel" aria-label="审批决定"><div class="decision-heading"><h2>决定舱</h2>${icon('shield')}</div><div class="decision-body"><div class="gate-symbol" aria-hidden="true">${icon(r.state === 'READY' ? 'check' : 'shield')}</div><h3 class="decision-state">${title}</h3><p class="decision-context">${r.state === 'READY' ? 'READY / 等待原申请者恢复' : r.state === 'OUTCOME_UNKNOWN' ? 'OUTCOME_UNKNOWN / 尚未确认副作用' : 'PENDING_APPROVAL / 业务尚未执行'}</p><dl class="decision-details"><dt>决定主体</dt><dd>${requesterView ? 'Alice（本人）' : '林辰（他人）'}</dd><dt>当前阶段</dt><dd>${pending ? '审批中' : r.state === 'READY' ? '已批准待恢复' : '结果未知'}</dd><dt>计划版本</dt><dd class="code">v3 / 固定样例</dd><dt>到期说明</dt><dd>30 分钟后（样例）</dd></dl><div class="notice ${r.type === 'unknown' ? 'danger' : ''}"><strong>${note[0]}</strong><span>${note[1]}</span></div></div><div class="decision-actions">${actions}</div><p class="decision-identity">${requesterView ? '此为 Alice 的本人申请。审批决定必须由其他合格人员作出。' : '审批资格与业务读取权限分别管理。这里没有授予业务数据读取权。'}</p></aside>`;
}
function rail() {
  return `<aside class="icon-rail"><div class="brand-sign">${icon('shield')}</div><nav aria-label="场景快捷导航">${[['write', '受控写审批', 'orbit'], ['access', '资源权限申请', 'database'], ['unknown', '未知结果核对', 'history']].map(([key, title, glyph]) => `<button class="rail-button" data-request="${key}" title="${title}" aria-label="${title}" ${currentRequest().key === key ? 'aria-current="page"' : ''} ${!visibleRequests().some(r => r.key === key) ? 'disabled' : ''}>${icon(glyph)}</button>`).join('')}</nav><span class="rail-caption">控制 · 授权 · 核对</span></aside>`;
}
function appHeader() {
  return `<header class="app-header"><div class="${concept === 'orbit' ? 'orbit-brand' : ''}">${concept === 'orbit' ? `<span class="brand-sign">${icon('orbit')}</span>` : ''}<div class="wordmark"><strong>MCPGuard</strong><span>${concept === 'bridge' ? '授权指挥 / 审批舰桥' : '授权控制 / 轨道中枢'}</span></div></div><div class="header-controls"><span class="workspace-name">本机工作空间 / 样例</span>${identity()}</div></header>`;
}
function syncMotion() {
  const active = motion && !reduceMotion.matches;
  document.body.dataset.motion = active ? 'on' : 'off';
  document.body.dataset.paused = String(document.hidden);
  motionButton.setAttribute('aria-pressed', String(active));
  motionButton.textContent = reduceMotion.matches ? '已减少动效' : active ? '关闭动效' : '开启动效';
  motionButton.disabled = reduceMotion.matches;
}
function render() {
  document.body.dataset.concept = concept;
  document.querySelectorAll('button[data-concept]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.concept === concept)));
  const footer = `<footer class="scene-footer"><span>${icon('shield')}身份、资源与授权在实际执行前仍须重验</span><span>设计样例 · 无真实请求与凭据</span></footer>`;
  const scene = concept === 'bridge' ? `<div class="scene-grid">${queue()}${requestBody()}${decisionPanel()}</div>` : `${queue()}<div class="approval-layout">${requestBody()}${decisionPanel()}</div>`;
  const main = `<main id="review-main" class="review-main" tabindex="-1">${hero()}${scenarioTabs()}${scene}${footer}</main>`;
  app.innerHTML = concept === 'bridge' ? `<div class="app-shell bridge-shell">${rail()}<div class="shell-main">${appHeader()}${main}</div></div>` : `<div class="app-shell orbit-shell">${appHeader()}${main}</div>`;
  syncMotion();
  const next = new URL(location.href);
  next.searchParams.set('concept', concept);
  next.searchParams.set('request', currentRequest().key);
  history.replaceState(null, '', next);
  document.title = `MCPGuard · ${concept === 'bridge' ? '星舰审批舰桥' : '光舱轨道中枢'}`;
}
function setTab(key, moveFocus = true) {
  detailTab = key;
  app.querySelectorAll('[data-tab]').forEach(tab => { const active = tab.dataset.tab === key; tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1; });
  const panel = document.getElementById('payload');
  panel.setAttribute('aria-labelledby', 'tab-' + key);
  panel.innerHTML = payload(currentRequest());
  if (moveFocus) document.getElementById('tab-' + key).focus({preventScroll: true});
}
function toast(text) {
  const el = document.getElementById('toast');
  clearTimeout(toastTimer);
  el.textContent = text;
  el.hidden = false;
  toastTimer = setTimeout(() => { el.hidden = true; }, 4800);
}
function confirmation(reject, trigger) {
  const r = currentRequest();
  if (requesterView || r.state !== 'PENDING_APPROVAL') return;
  dialog.returnValue = '';
  document.getElementById('decision-reason').value = '';
  document.getElementById('confirmation-title').textContent = reject ? '拒绝这份申请？' : '批准这份申请？';
  document.getElementById('confirmation-summary').innerHTML = `<dl class="dialog-details"><dt>申请人</dt><dd>${r.owner}</dd><dt>目标资源</dt><dd>${r.resource} / <span class="code">${r.scope}</span></dd><dt>操作边界</dt><dd>${r.impact}</dd><dt>申请编号</dt><dd class="code">${r.id}</dd></dl>`;
  document.getElementById('confirmation-effect').textContent = reject ? '拒绝当前申请，不会创建账号或执行操作' : r.type === 'access' ? '批准后仍需完成账号开通、私有交付与激活' : '批准只记录决定，原申请者仍需显式恢复';
  dialog.querySelector('[value="confirm"]').textContent = reject ? '确认拒绝 · 仅预览' : '确认批准 · 仅预览';
  dialogTrigger = trigger;
  dialog.showModal();
}

document.addEventListener('click', event => {
  const trigger = event.target.closest('button');
  if (!trigger || trigger.disabled) return;
  if (trigger.dataset.concept) { concept = trigger.dataset.concept; render(); return; }
  if (trigger.dataset.request) {
    if (!visibleRequests().some(r => r.key === trigger.dataset.request)) return;
    selected = trigger.dataset.request; detailTab = 'change'; query = ''; render();
    if (matchMedia('(max-width:650px)').matches) document.getElementById('request-title').scrollIntoView({block: 'start', behavior: 'auto'});
    return;
  }
  if (trigger.dataset.tab) { setTab(trigger.dataset.tab); return; }
  if (trigger.dataset.action === 'identity') {
    requesterView = !requesterView; selected = visibleRequests()[0].key; detailTab = 'change'; query = ''; render();
    toast(requesterView ? '已切换为 Alice 的样例视角，仅显示本人可见申请，不能自行审批。' : '已切换为林辰的独立审批视角，未授予业务读取权。'); return;
  }
  if (trigger.dataset.action === 'approve' || trigger.dataset.action === 'reject') { confirmation(trigger.dataset.action === 'reject', trigger); return; }
  if (trigger.dataset.action === 'timeline') { setTab('timeline'); document.getElementById('payload').scrollIntoView({block: 'nearest', behavior: 'auto'}); }
});
app.addEventListener('input', event => { if (event.target.id === 'queue-search') { query = event.target.value; app.querySelector('.queue-list').innerHTML = queueItems(); } });
app.addEventListener('keydown', event => {
  if (!event.target.matches('[data-tab]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const tabs = ['change', 'parameters', 'timeline'];
  const offset = event.key === 'ArrowRight' ? 1 : -1;
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (tabs.indexOf(detailTab) + offset + tabs.length) % tabs.length;
  setTab(tabs[index]);
});
motionButton.addEventListener('click', () => { motion = !motion; syncMotion(); });
document.addEventListener('visibilitychange', syncMotion);
reduceMotion.addEventListener('change', syncMotion);
compactView.addEventListener('change', () => {
  const figure = app.querySelector('.connection-figure');
  if (figure) figure.setAttribute('viewBox', compactView.matches ? '230 0 320 266' : '0 0 780 266');
});
dialog.addEventListener('close', () => {
  if (dialog.returnValue === 'confirm') toast('审批确认演示完成。未保存决定、未开通账号、未派发业务执行。');
  const trigger = dialogTrigger?.isConnected ? dialogTrigger : app.querySelector(`[data-action="${dialogTrigger?.dataset.action || 'approve'}"]`);
  if (trigger) trigger.focus();
});
render();
