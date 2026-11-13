'use strict';

(() => {
  let serial = 0;
  let activeDialog = null;
  let toastTimer;
  let errorHandler = null;
  const SVG = 'http://www.w3.org/2000/svg';
  const paths = {
    shield: 'M12 2 4 5v6c0 5 8 10 8 10s8-5 8-10V5l-8-3Z M8 11l3 3 5-6',
    actions: 'M5 3h14v18H5z M8 7h8 M8 11h8 M8 15h5',
    database: 'M4 5c0-4 16-4 16 0s-16 4-16 0v14c0 4 16 4 16 0V5 M4 12c0 4 16 4 16 0',
    rules: 'M4 5h16 M4 12h16 M4 19h16 M8 2v6 M16 9v6 M9 16v6',
    users: 'M9 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6 M3 21v-3a6 6 0 0 1 12 0v3 M16 4a3 3 0 0 1 0 6 M18 15a5 5 0 0 1 3 5',
    history: 'M3 11a9 9 0 1 1 3 7 M3 4v7h7 M12 7v5l3 2',
    settings: 'M9 3 8 6 5 7v4l2 1v3l-2 1 2 4 3-1 2 2 3-2 3 1 2-4-2-2v-3l2-1-1-4-3-1-1-3Z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0',
    refresh: 'M20 8A8 8 0 0 0 6 5L3 8 M3 3v5h5 M4 16a8 8 0 0 0 14 3l3-3 M16 16h5v5',
    search: 'M16 10a6 6 0 1 1-12 0 6 6 0 0 1 12 0 M15 15l6 6',
    arrow: 'M4 12h16 M14 6l6 6-6 6',
    menu: 'M4 6h16 M4 12h16 M4 18h16',
    close: 'M6 6l12 12 M18 6 6 18',
    copy: 'M8 8h12v13H8z M16 8V3H3v13h5',
    empty: 'M3 15h5l2 3h4l2-3h5 M3 15 6 5h12l3 10v6H3Z',
    check: 'M4 12l5 5L20 6',
    exit: 'M10 4H4v16h6 M9 12h12 M16 7l5 5-5 5'
  };

  function el(tag, text, className) {
    const element = document.createElement(tag);
    if (text !== undefined && text !== null) element.textContent = String(text);
    if (className) element.className = className;
    return element;
  }

  function svgElement(tag, attributes = {}) {
    const element = document.createElementNS(SVG, tag);
    for (const [key, value] of Object.entries(attributes)) element.setAttribute(key, String(value));
    return element;
  }

  function icon(name) {
    const element = svgElement('svg', {viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.5', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', focusable: 'false'});
    element.classList.add('mg-icon');
    element.append(svgElement('path', {d: paths[name] || paths.shield}));
    return element;
  }

  async function runButton(button, task) {
    if (button.dataset.busy === 'true' || button.disabled) return;
    const wasDisabled = button.disabled;
    const spinner = el('span', '', 'mg-spinner');
    spinner.setAttribute('aria-hidden', 'true');
    button.disabled = true;
    button.dataset.busy = 'true';
    button.setAttribute('aria-busy', 'true');
    button.prepend(spinner);
    try { return await task(button); }
    catch (error) { if (errorHandler) errorHandler(error); }
    finally {
      spinner.remove();
      button.dataset.busy = 'false';
      button.removeAttribute('aria-busy');
      if (button.isConnected) button.disabled = wasDisabled;
    }
  }

  function button(label, task, options = {}) {
    const element = el('button', null, 'mg-btn' + (options.variant ? ' mg-btn--' + options.variant : '') + (options.small ? ' mg-btn--small' : ''));
    element.type = 'button';
    if (options.icon) element.append(icon(options.icon));
    element.append(el('span', label));
    if (options.label) element.setAttribute('aria-label', options.label);
    if (options.disabled) element.disabled = true;
    if (options.action) element.dataset.action = options.action;
    if (task) element.addEventListener('click', () => runButton(element, task));
    return element;
  }

  function badge(text, tone = 'neutral') {
    const allowed = ['neutral', 'info', 'success', 'warning', 'danger'];
    return el('span', text, 'mg-badge mg-badge--' + (allowed.includes(tone) ? tone : 'warning'));
  }

  function stateBadge(domain, code) {
    const labels = globalThis.MCPGuardUIStateLabels;
    const data = labels && Object.hasOwn(labels, domain) && Object.hasOwn(labels[domain], code) ? labels[domain][code] : null;
    const element = badge(data ? data.label : '状态未确认', data ? data.tone : 'warning');
    element.title = String(code || '未取得状态');
    element.dataset.state = String(code || 'UNKNOWN');
    return element;
  }

  function notice(text, tone = 'info', title) {
    const element = el('div', null, 'mg-notice mg-notice--' + tone);
    if (title) element.append(el('strong', title));
    element.append(el('span', text));
    return element;
  }

  function empty(title, description, glyph = 'empty') {
    const element = el('div', null, 'mg-empty');
    element.append(icon(glyph), el('strong', title));
    if (description) element.append(el('p', description));
    return element;
  }

  function panel(title, description, className = '') {
    const element = el('section', null, 'mg-panel ' + className);
    const head = el('div', null, 'mg-panel-heading');
    head.append(el('h2', title));
    if (description) head.append(el('p', description, 'mg-muted'));
    element.append(head);
    return element;
  }

  function table(headers, rows, options = {}) {
    const region = el('div', null, 'mg-table-region');
    region.tabIndex = 0;
    region.setAttribute('role', 'region');
    region.setAttribute('aria-label', options.label || headers.join('、') + '，可横向滚动');
    const element = el('table', null, 'mg-table');
    const head = el('thead'), header = el('tr'), body = el('tbody');
    for (const title of headers) { const th = el('th', title); th.scope = 'col'; header.append(th); }
    head.append(header);
    for (const row of rows) {
      const tr = el('tr');
      for (const value of row) { const td = el('td'); td.append(value instanceof Node ? value : el('span', value ?? '—')); tr.append(td); }
      body.append(tr);
    }
    if (!rows.length) {
      const tr = el('tr'), td = el('td'); td.colSpan = headers.length;
      td.append(empty(options.empty || '暂无记录', options.description || '仅在取得真实记录后显示内容。'));
      tr.append(td); body.append(tr);
    }
    element.append(head, body); region.append(element);
    return region;
  }

  function field(parent, label, kind = 'text', options = {}) {
    const wrapper = el('label', null, kind === 'checkbox' ? 'mg-check-field' : 'mg-field');
    const input = el(kind === 'select' ? 'select' : kind === 'textarea' ? 'textarea' : 'input');
    input.id = 'mg-field-' + (++serial);
    if (kind !== 'select' && kind !== 'textarea') input.type = kind;
    if (options.required) input.required = true;
    if (options.maxLength) input.maxLength = options.maxLength;
    if (options.placeholder) input.placeholder = options.placeholder;
    if (kind === 'select') {
      for (const choice of options.choices || []) {
        const option = el('option', typeof choice === 'string' ? choice : choice.label);
        option.value = typeof choice === 'string' ? choice : choice.value;
        input.append(option);
      }
    }
    if (options.value !== undefined) input.value = options.value;
    const labelText = el('span', label, kind === 'checkbox' ? '' : 'mg-field-label');
    labelText.id = input.id + '-label'; wrapper.htmlFor = input.id; input.setAttribute('aria-labelledby', labelText.id);
    if (kind === 'checkbox') { input.checked = Boolean(options.checked); wrapper.append(input, labelText); }
    else { wrapper.append(labelText, input); }
    if (options.hint) { const hint = el('small', options.hint, 'mg-muted'); hint.id = input.id + '-hint'; input.setAttribute('aria-describedby', hint.id); wrapper.append(hint); }
    parent.append(wrapper);
    return input;
  }

  function form(title, description, label, task) {
    const element = el('form', null, 'mg-form mg-panel');
    element.dataset.form = title;
    const heading = el('div', null, 'mg-panel-heading');
    heading.append(el('h2', title));
    if (description) heading.append(el('p', description, 'mg-muted'));
    const fields = el('div', null, 'mg-form-fields');
    const error = el('p', '', 'mg-form-error'); error.hidden = true; error.setAttribute('role', 'alert');
    const actions = el('div', null, 'mg-form-actions');
    const submit = button(label, null, {variant: 'primary'}); submit.type = 'submit';
    actions.append(error, submit); element.append(heading, fields, actions);
    element.addEventListener('submit', event => {
      event.preventDefault();
      if (!element.reportValidity()) return;
      error.hidden = true;
      runButton(submit, async () => {
        try { await task(submit); }
        catch (failure) { if (!failure.silent) { error.textContent = ui.errorText(failure); error.hidden = false; } throw failure; }
      });
    });
    return {element, fields, submit};
  }

  function keyValues(values) {
    const element = el('dl', null, 'mg-details');
    for (const [label, value] of values) {
      const dd = el('dd'); dd.append(value instanceof Node ? value : el('span', value ?? '—'));
      element.append(el('dt', label), dd);
    }
    return element;
  }

  function shortID(value) { const text = String(value || ''); return text.length > 18 ? text.slice(0, 8) + '…' + text.slice(-6) : text || '尚未取得'; }

  function copyID(value, description = '复制安全编号') {
    const group = el('span', null, 'mg-copy-id');
    const text = el('span', shortID(value), 'mg-code'); text.title = String(value || '');
    group.append(text);
    if (value) group.append(button('复制', async () => { await navigator.clipboard.writeText(String(value)); toast('已复制安全编号。'); }, {small: true, icon: 'copy', label: description}));
    return group;
  }

  function date(value) {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? '尚未取得时间' : parsed.toLocaleString('zh-CN', {hour12: false});
  }

  function tabs(entries) {
    const container = el('div', null, 'mg-tabs-container'), bar = el('div', null, 'mg-tabs');
    const content = el('section', null, 'mg-tab-content');
    const id = 'mg-tabs-' + (++serial); content.id = id + '-content';
    bar.setAttribute('role', 'tablist'); bar.setAttribute('aria-label', '申请详情');
    content.setAttribute('role', 'tabpanel');
    const buttons = [];
    function select(index, focus) {
      buttons.forEach((item, i) => { item.setAttribute('aria-selected', String(i === index)); item.tabIndex = i === index ? 0 : -1; });
      content.setAttribute('aria-labelledby', buttons[index].id);
      content.replaceChildren(entries[index].render());
      if (focus) buttons[index].focus({preventScroll: true});
    }
    entries.forEach((entry, index) => {
      const item = el('button', entry.label); item.type = 'button'; item.id = id + '-' + index;
      item.setAttribute('role', 'tab'); item.setAttribute('aria-controls', content.id);
      item.addEventListener('click', () => select(index, false));
      item.addEventListener('keydown', event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        event.preventDefault();
        const target = event.key === 'Home' ? 0 : event.key === 'End' ? entries.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + entries.length) % entries.length;
        select(target, true);
      });
      buttons.push(item); bar.append(item);
    });
    container.append(bar, content); select(0, false);
    return container;
  }

  function orbit({owner, resource, phase, description}) {
    const wrapper = el('div', null, 'mg-orbit');
    const diagram = svgElement('svg', {viewBox: '0 0 780 250', role: 'img'});
    const labelID = 'mg-orbit-' + (++serial); diagram.setAttribute('aria-labelledby', labelID);
    const title = svgElement('title', {id: labelID}); title.textContent = `${owner}，${resource}，${phase}。关系示意，不表示实时连接或派发。`;
    diagram.append(title);
    const add = (tag, attributes) => { const item = svgElement(tag, attributes); diagram.append(item); return item; };
    add('path', {d: 'M123 119h143l26-22h30 M468 126h55l27 25h116', class: 'mg-orbit-line'});
    add('circle', {cx: 390, cy: 124, r: 106, class: 'mg-orbit-outline'});
    add('circle', {cx: 390, cy: 124, r: 94, class: 'mg-orbit-outline mg-orbit-dashed'});
    add('ellipse', {cx: 390, cy: 124, rx: 143, ry: 53, transform: 'rotate(-25 390 124)', class: 'mg-orbit-outline'});
    const rotating = add('g', {class: 'mg-orbit-rotating'});
    rotating.append(svgElement('circle', {cx: 390, cy: 124, r: 117, class: 'mg-orbit-outline', 'stroke-dasharray': '55 17 2 28 30 620'}), svgElement('circle', {cx: 390, cy: 7, r: 3.5, class: 'mg-orbit-point'}));
    add('path', {d: 'm390 58 58 33v66l-58 33-58-33V91Z', class: 'mg-orbit-core'});
    add('rect', {x: 63, y: 91, width: 61, height: 57, rx: 8, class: 'mg-orbit-node'});
    add('path', {d: 'M86 115a8 8 0 1 1 15 0 M80 134a14 14 0 0 1 28 0', class: 'mg-orbit-line'});
    add('rect', {x: 665, y: 123, width: 61, height: 57, rx: 8, class: 'mg-orbit-node'});
    add('path', {d: 'M681 140c0-6 28-6 28 0s-28 6-28 0v19c0 7 28 7 28 0v-19 M681 149c0 7 28 7 28 0', class: 'mg-orbit-line'});
    for (const [text, x, y, cls] of [[owner, 94, 180, 'mg-map-label'], ['冻结的申请', 214, 105, 'mg-map-caption'], [phase, 390, 124, 'mg-map-center'], [description, 390, 148, 'mg-map-caption'], [resource, 694, 211, 'mg-map-label']]) {
      const item = add('text', {x, y, 'text-anchor': 'middle', class: cls}); item.textContent = text;
    }
    wrapper.append(diagram, el('p', '身份与资源分别绑定 · 关系示意，无实时连接', 'mg-orbit-caption'));
    return wrapper;
  }

  function confirm(options) {
    if (activeDialog) return Promise.resolve(false);
    const dialog = document.getElementById('confirmDialog');
    const trigger = options.returnTo || document.activeElement;
    dialog.replaceChildren(); dialog.returnValue = '';
    const title = el('h2', options.title); title.id = 'confirm-title'; title.tabIndex = -1;
    dialog.setAttribute('aria-labelledby', title.id);
    dialog.append(el('p', 'MCPGuard · 操作确认', 'mg-eyebrow'), title, keyValues(options.details || []));
    if (options.content) dialog.append(options.content);
    if (options.notice) dialog.append(notice(options.notice, options.danger ? 'warning' : 'info'));
    const actions = el('div', null, 'mg-dialog-actions');
    const cancel = button('返回核对', () => dialog.close('cancel'));
    const submit = button(options.confirmLabel || '确认提交', () => dialog.close('confirm'), {variant: options.danger ? 'danger' : 'primary'});
    actions.append(cancel, submit); dialog.append(actions);
    return new Promise(resolve => {
      activeDialog = dialog;
      dialog.addEventListener('close', () => {
        const accepted = dialog.returnValue === 'confirm';
        activeDialog = null; dialog.replaceChildren();
        resolve(accepted);
        requestAnimationFrame(() => {
          if (!activeDialog && (!options.canRestore || options.canRestore())) {
            if (trigger?.isConnected && !trigger.disabled) trigger.focus();
            else document.getElementById('page-title')?.focus();
          }
        });
      }, {once: true});
      dialog.showModal(); cancel.focus();
    });
  }

  function closeDialog() { if (activeDialog?.open) activeDialog.close('cancel'); }
  function toast(text) {
    const element = document.getElementById('toast');
    clearTimeout(toastTimer); element.replaceChildren(el('span', text), button('关闭', () => { element.hidden = true; }, {small: true, label: '关闭提示'}));
    element.hidden = false;
    toastTimer = setTimeout(() => { element.hidden = true; element.replaceChildren(); }, 4800);
  }

  const ui = {el, icon, button, runButton, badge, stateBadge, notice, empty, panel, table, field, form, keyValues, shortID, copyID, date, tabs, orbit, confirm, closeDialog, toast,
    dialogOpen: () => Boolean(activeDialog),
    errorText: error => error?.publicMessage || '操作未完成，请核对当前状态后再处理。',
    setErrorHandler: handler => { errorHandler = handler; }};
  globalThis.MCPGuardUI = Object.freeze(ui);
})();
