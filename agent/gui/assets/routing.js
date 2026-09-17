let routingRules = [];

function escapeAttr(value) {
  return String(value == null ? '' : value).replace(/&/g, '&amp;').replace(/"/g, '&quot;')
    .replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/'/g, '&#39;');
}
function splitRuleList(value) {
  return String(value || '').split(/[,;，；\n]/).map(s => s.trim()).filter(Boolean);
}
function renderRoutingRules(rules) {
  routingRules = JSON.parse(JSON.stringify(rules || []));
  const body = $('routing-rules-body');
  body.replaceChildren();
  routingRules.forEach((rule, index) => {
    ['processes','targets','ports','protocols'].forEach(field => { rule[field] = rule[field] || []; });
    body.appendChild(createRuleRow(rule, index));
  });
  if (!routingRules.length) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 7;
    cell.className = 'p-6 text-center text-slate-500';
    cell.textContent = '还没有规则；所有连接使用默认动作。';
    row.appendChild(cell); body.appendChild(row);
  }
}
function createRuleRow(rule, index) {
  const row = document.createElement('tr');
  row.className = 'hover:bg-slate-50 dark:hover:bg-slate-800/40 align-top';
  const fieldClass = 'w-full p-1.5 bg-transparent border border-slate-300 dark:border-slate-600 rounded text-[12px] mono';
  const selector = (field, label, placeholder) => `<textarea rows="2" aria-label="${label}" placeholder="${placeholder}" oninput="routingRules[${index}].${field} = splitRuleList(this.value)" class="${fieldClass}">${escapeAttr((rule[field] || []).join(', '))}</textarea>`;
  const protocols = (rule.protocols || []).map(p => p.toLowerCase());
  const proto = protocols.length === 1 && protocols[0] !== '*' ? protocols[0] : 'any';
  row.innerHTML = `
    <td class="p-2 min-w-[150px]">
      <label class="flex items-center gap-2 mb-2"><input aria-label="启用规则" type="checkbox" ${rule.enabled ? 'checked' : ''} onchange="routingRules[${index}].enabled = this.checked"><span class="text-slate-500">${index + 1}</span></label>
      <input aria-label="规则名称" value="${escapeAttr(rule.name)}" oninput="routingRules[${index}].name = this.value" placeholder="规则名称" class="${fieldClass}">
    </td>
    <td class="p-2 min-w-[175px]">${selector('processes','进程名称或路径','任意进程；如 chrome*, *chrome*, *.exe')}</td>
    <td class="p-2 min-w-[235px]">${selector('targets','域名或 IP','任意目标；如 *example*, 192.168.*, *.1')}</td>
    <td class="p-2 min-w-[125px]">${selector('ports','目标端口','任意端口；如 80, 443, 8000-9000')}</td>
    <td class="p-2 min-w-[100px]"><select aria-label="协议" onchange="routingRules[${index}].protocols = this.value === 'any' ? [] : [this.value]" class="${fieldClass}">
      <option value="any" ${proto === 'any' ? 'selected' : ''}>TCP + UDP</option><option value="tcp" ${proto === 'tcp' ? 'selected' : ''}>TCP</option><option value="udp" ${proto === 'udp' ? 'selected' : ''}>UDP</option>
    </select></td>
    <td class="p-2 min-w-[185px]">
      <select aria-label="规则动作" onchange="routingRules[${index}].action = this.value" class="${fieldClass}">
        <option value="PROXY" ${rule.action === 'PROXY' ? 'selected' : ''}>PROXY · 代理</option><option value="DIRECT" ${rule.action === 'DIRECT' ? 'selected' : ''}>DIRECT · 直连</option><option value="REJECT" ${rule.action === 'REJECT' ? 'selected' : ''}>REJECT · 阻断</option>
      </select>
      <input aria-label="指定出口" value="${escapeAttr(rule.exit_id)}" oninput="routingRules[${index}].exit_id = this.value" placeholder="出口 ID；留空使用当前出口" class="${fieldClass} mt-2">
      <label class="block mt-2 text-[11px]"><input type="checkbox" ${rule.datagram_required ? 'checked' : ''} onchange="routingRules[${index}].datagram_required = this.checked"> UDP 必须使用原生数据报</label>
    </td>
    <td class="p-2 min-w-[65px] text-center">
      <button onclick="moveRuleRow(${index}, -1)" title="上移" aria-label="上移规则" class="p-1" ${index === 0 ? 'disabled' : ''}>↑</button><button onclick="moveRuleRow(${index}, 1)" title="下移" aria-label="下移规则" class="p-1" ${index === routingRules.length - 1 ? 'disabled' : ''}>↓</button>
      <button onclick="removeRuleRow(${index})" title="删除" aria-label="删除规则" class="p-1 text-red-500">删除</button>
    </td>`;
  return row;
}
function unconstrainedRule(rule) {
  return ['processes','targets','ports','protocols'].every(field => !rule[field] || !rule[field].length || rule[field].includes('*'));
}
function addRuleRow() {
  let at = routingRules.findIndex(unconstrainedRule);
  if (at < 0) at = routingRules.length;
  routingRules.splice(at, 0, {name:'新规则', enabled:true, processes:[], targets:[], ports:[], protocols:[], action:'PROXY', exit_id:'', datagram_required:false});
  renderRoutingRules(routingRules);
  window.markDrafts();
  const rows = $('routing-rules-body').children;
  rows[at]?.querySelector('[aria-label="进程名称或路径"]')?.focus();
}
function removeRuleRow(index) {
  routingRules.splice(index,1); renderRoutingRules(routingRules); window.markDrafts();
}
function moveRuleRow(index, delta) {
  const to = index + delta;
  if (to < 0 || to >= routingRules.length) return;
  routingRules.splice(to,0,routingRules.splice(index,1)[0]); renderRoutingRules(routingRules); window.markDrafts();
}
async function saveRouting() {
  return window.saveSettings({routing:{mode:$('cfg-routing-mode').value, default_action:$('cfg-routing-default').value, rules:routingRules},
    network:{excludeProcesses:splitRuleList($('cfg-network-excludes').value)}}, '组合路由规则已应用，新连接使用新规则');
}
async function openConnections() {
  try {
    if (typeof window.goOpenConnections !== 'function') throw new Error('请在 Windows Agent 中打开实时连接窗口');
    const result = await window.goOpenConnections();
    if (result && result !== 'ok') throw new Error(result);
  } catch (error) { window.showMonitorError(error.message); }
}
window.showMonitorError = message => {
  $('toast-msg').textContent = message;
  $('toast').style.opacity = '1';
  setTimeout(() => { $('toast').style.opacity = '0'; }, 5000);
};
