let routingRules = [];
let routingRuleEditorIndex = -1;

function escapeHTML(value) {
  return String(value == null ? '' : value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function splitRuleList(value) {
  return String(value || '').split(/[,;，；\n]/).map(s => s.trim()).filter(Boolean);
}

function normalizeRoutingRule(rule) {
  const normalized = Object.assign({
    name: '',
    enabled: true,
    processes: [],
    targets: [],
    ports: [],
    protocols: [],
    action: 'PROXY',
    exit_id: '',
    datagram_required: false
  }, rule || {});
  ['processes', 'targets', 'ports', 'protocols'].forEach(field => {
    normalized[field] = Array.isArray(normalized[field]) ? normalized[field] : [];
  });
  return normalized;
}

function ruleActionLabel(action) {
  if (action === 'DIRECT') return 'DIRECT · 直连';
  if (action === 'REJECT') return 'REJECT · 阻断';
  return 'PROXY · 代理';
}

function ruleActionClass(action) {
  if (action === 'DIRECT') return 'routing-action routing-action-direct';
  if (action === 'REJECT') return 'routing-action routing-action-reject';
  return 'routing-action routing-action-proxy';
}

function ruleProtocolLabel(rule) {
  const protocols = (rule.protocols || []).map(p => String(p).toLowerCase()).filter(Boolean);
  if (!protocols.length || protocols.includes('*') || protocols.length > 1) return 'TCP + UDP';
  return protocols[0] === 'udp' ? 'UDP' : 'TCP';
}

function ruleSummary(values, emptyLabel) {
  const items = (values || []).filter(Boolean);
  if (!items.length || items.includes('*')) {
    return '<span class="routing-any">' + escapeHTML(emptyLabel) + '</span>';
  }
  const shown = items.slice(0, 3);
  let html = shown.map(item => '<span class="routing-token mono">' + escapeHTML(item) + '</span>').join('');
  if (items.length > shown.length) {
    html += '<span class="routing-token routing-token-more">+' + (items.length - shown.length) + '</span>';
  }
  return html;
}

function renderRoutingRules(rules) {
  routingRules = JSON.parse(JSON.stringify(rules || [])).map(normalizeRoutingRule);
  const body = $('routing-rules-body');
  body.replaceChildren();

  routingRules.forEach((rule, index) => body.appendChild(createRuleRow(rule, index)));

  if (!routingRules.length) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 5;
    cell.className = 'routing-empty';
    cell.innerHTML = '<strong>还没有路由规则</strong><span>所有连接将使用上方配置的默认兜底动作。</span>';
    row.appendChild(cell);
    body.appendChild(row);
  }
}

function createRuleRow(rule, index) {
  const row = document.createElement('tr');
  row.className = 'routing-rule-row' + (rule.enabled ? '' : ' is-disabled');

  const name = String(rule.name || '').trim() || '未命名规则';
  const exit = String(rule.exit_id || '').trim();
  const datagram = !!rule.datagram_required;

  row.innerHTML = `
    <td class="routing-rule-main">
      <div class="routing-rule-title-line">
        <span class="routing-priority mono">#${index + 1}</span>
        <strong title="${escapeHTML(name)}">${escapeHTML(name)}</strong>
        <span class="routing-status ${rule.enabled ? 'is-on' : 'is-off'}">${rule.enabled ? '已启用' : '已停用'}</span>
      </div>
      <div class="routing-rule-meta">从上到下匹配，命中后停止继续检查。</div>
    </td>
    <td class="routing-match-cell">
      <div class="routing-match-line"><span class="routing-match-label">进程</span><div class="routing-token-list">${ruleSummary(rule.processes, '任意进程')}</div></div>
      <div class="routing-match-line"><span class="routing-match-label">目标</span><div class="routing-token-list">${ruleSummary(rule.targets, '任意域名 / IP')}</div></div>
      <div class="routing-match-line"><span class="routing-match-label">端口</span><div class="routing-token-list">${ruleSummary(rule.ports, '任意端口')}</div></div>
    </td>
    <td class="routing-protocol-cell"><span class="routing-protocol mono">${escapeHTML(ruleProtocolLabel(rule))}</span></td>
    <td class="routing-action-cell">
      <span class="${ruleActionClass(rule.action)}">${escapeHTML(ruleActionLabel(rule.action))}</span>
      ${exit ? '<span class="routing-exit mono" title="' + escapeHTML(exit) + '">出口 ' + escapeHTML(exit) + '</span>' : '<span class="routing-exit">当前出口</span>'}
      ${datagram ? '<span class="routing-datagram">UDP 原生数据报</span>' : ''}
    </td>
    <td class="routing-ops-cell">
      <div class="routing-order-buttons">
        <button type="button" class="routing-icon-button" onclick="moveRuleRow(${index}, -1)" title="上移规则" aria-label="上移规则" ${index === 0 ? 'disabled' : ''}>↑</button>
        <button type="button" class="routing-icon-button" onclick="moveRuleRow(${index}, 1)" title="下移规则" aria-label="下移规则" ${index === routingRules.length - 1 ? 'disabled' : ''}>↓</button>
      </div>
      <button type="button" class="routing-edit-button" onclick="editRuleRow(${index})">编辑</button>
      <button type="button" class="routing-delete-button" onclick="removeRuleRow(${index})">删除</button>
    </td>`;
  return row;
}

function unconstrainedRule(rule) {
  return ['processes', 'targets', 'ports', 'protocols'].every(field =>
    !rule[field] || !rule[field].length || rule[field].includes('*')
  );
}

function routingEditorElements() {
  return {
    modal: $('routing-rule-modal'),
    title: $('routing-rule-modal-title'),
    enabled: $('routing-rule-enabled'),
    name: $('routing-rule-name'),
    processes: $('routing-rule-processes'),
    targets: $('routing-rule-targets'),
    ports: $('routing-rule-ports'),
    protocol: $('routing-rule-protocol'),
    action: $('routing-rule-action'),
    exit: $('routing-rule-exit'),
    datagram: $('routing-rule-datagram'),
    datagramWrap: $('routing-rule-datagram-wrap'),
    save: $('routing-rule-save')
  };
}

function routingRuleActionsLocked() {
  const save = $('routing-rule-save');
  return !save || save.disabled;
}

function openRuleEditor(index) {
  const el = routingEditorElements();
  if (!el.modal || routingRuleActionsLocked()) return;

  routingRuleEditorIndex = Number.isInteger(index) ? index : -1;
  const editing = routingRuleEditorIndex >= 0 && routingRuleEditorIndex < routingRules.length;
  const rule = editing
    ? normalizeRoutingRule(JSON.parse(JSON.stringify(routingRules[routingRuleEditorIndex])))
    : normalizeRoutingRule({ name: '新规则' });

  const protocols = (rule.protocols || []).map(p => String(p).toLowerCase());
  el.title.textContent = editing ? '编辑路由规则' : '新增路由规则';
  el.enabled.checked = rule.enabled !== false;
  el.name.value = rule.name || '';
  el.processes.value = (rule.processes || []).join('\n');
  el.targets.value = (rule.targets || []).join('\n');
  el.ports.value = (rule.ports || []).join('\n');
  el.protocol.value = protocols.length === 1 && protocols[0] !== '*' ? protocols[0] : 'any';
  el.action.value = rule.action || 'PROXY';
  el.exit.value = rule.exit_id || '';
  el.datagram.checked = !!rule.datagram_required;

  updateRoutingEditorDependencies();
  el.modal.classList.remove('hidden');
  el.modal.setAttribute('aria-hidden', 'false');
  requestAnimationFrame(() => el.name.focus());
}

function closeRuleEditor() {
  const el = routingEditorElements();
  if (!el.modal) return;
  el.modal.classList.add('hidden');
  el.modal.setAttribute('aria-hidden', 'true');
  routingRuleEditorIndex = -1;
}

function updateRoutingEditorDependencies() {
  const el = routingEditorElements();
  if (!el.action) return;
  const proxy = el.action.value === 'PROXY';
  el.exit.disabled = !proxy;
  el.datagram.disabled = !proxy;
  el.datagramWrap.classList.toggle('is-disabled', !proxy);
}

function saveRuleEditor() {
  if (routingRuleActionsLocked()) return;
  const el = routingEditorElements();
  const name = el.name.value.trim();
  if (!name) {
    el.name.focus();
    el.name.setAttribute('aria-invalid', 'true');
    return;
  }
  el.name.removeAttribute('aria-invalid');

  const rule = normalizeRoutingRule({
    name,
    enabled: el.enabled.checked,
    processes: splitRuleList(el.processes.value),
    targets: splitRuleList(el.targets.value),
    ports: splitRuleList(el.ports.value),
    protocols: el.protocol.value === 'any' ? [] : [el.protocol.value],
    action: el.action.value,
    exit_id: el.action.value === 'PROXY' ? el.exit.value.trim() : '',
    datagram_required: el.action.value === 'PROXY' && el.datagram.checked
  });

  if (routingRuleEditorIndex >= 0 && routingRuleEditorIndex < routingRules.length) {
    routingRules[routingRuleEditorIndex] = rule;
  } else {
    let at = routingRules.findIndex(unconstrainedRule);
    if (at < 0) at = routingRules.length;
    routingRules.splice(at, 0, rule);
  }

  renderRoutingRules(routingRules);
  closeRuleEditor();
  window.markDrafts();
}

function addRuleRow() {
  openRuleEditor(-1);
}

function editRuleRow(index) {
  openRuleEditor(index);
}

function removeRuleRow(index) {
  if (routingRuleActionsLocked()) return;
  const rule = routingRules[index];
  const name = rule && String(rule.name || '').trim();
  if (!confirm('删除路由规则“' + (name || ('#' + (index + 1))) + '”？')) return;
  routingRules.splice(index, 1);
  renderRoutingRules(routingRules);
  window.markDrafts();
}

function moveRuleRow(index, delta) {
  if (routingRuleActionsLocked()) return;
  const to = index + delta;
  if (to < 0 || to >= routingRules.length) return;
  routingRules.splice(to, 0, routingRules.splice(index, 1)[0]);
  renderRoutingRules(routingRules);
  window.markDrafts();
}

async function saveRouting() {
  return window.saveSettings({
    routing: {
      mode: $('cfg-routing-mode').value,
      default_action: $('cfg-routing-default').value,
      rules: routingRules
    },
    network: {
      excludeProcesses: splitRuleList($('cfg-network-excludes').value)
    }
  }, '组合路由规则已应用，新连接使用新规则');
}

async function openConnections() {
  try {
    if (typeof window.goOpenConnections !== 'function') throw new Error('请在 Windows Agent 中打开实时连接窗口');
    const result = await window.goOpenConnections();
    if (result && result !== 'ok') throw new Error(result);
  } catch (error) {
    window.showMonitorError(error.message);
  }
}

window.showMonitorError = message => {
  $('toast-msg').textContent = message;
  $('toast').style.opacity = '1';
  setTimeout(() => { $('toast').style.opacity = '0'; }, 5000);
};

window.openRuleEditor = openRuleEditor;
window.closeRuleEditor = closeRuleEditor;
window.saveRuleEditor = saveRuleEditor;
window.updateRoutingEditorDependencies = updateRoutingEditorDependencies;

document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && $('routing-rule-modal') && !$('routing-rule-modal').classList.contains('hidden')) {
    closeRuleEditor();
  }
});
