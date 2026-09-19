'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const all = selector => Array.from(document.querySelectorAll(selector));
  const state = { user: null, devices: [], enrollments: [], exits: [], sessions: [], ingress: [], settings: null, settingsUserID: null, dirty: false, saving: false, refreshing: false, editVersion: 0, selectedDevice: null, selectedEnrollment: null, deviceBusy: false, enrollmentBusy: false, passwordSaving: false };
  const titles = { overview: '总览', devices: '设备管理', exits: '出口节点', sessions: '活跃会话', 'rdp-ingress': 'RDP 公网入口', settings: '服务配置' };
  const sectionPages = {
    overview: [{ page: 'overview', label: '运行总览' }],
    devices: [{ page: 'devices', label: '设备管理' }, { page: 'exits', label: '出口节点' }],
    connections: [{ page: 'sessions', label: '活跃会话' }],
    rdp: [{ page: 'rdp-ingress', label: '公网入口', admin: true }],
    settings: [
      { page: 'settings', label: '管理访问', admin: true, settingsTab: 'admin' },
      { page: 'settings', label: '隧道', admin: true, settingsTab: 'tunnel' },
      { page: 'settings', label: 'RDP', admin: true, settingsTab: 'rdp' },
      { page: 'settings', label: '证书', admin: true, settingsTab: 'certificate' },
      { page: 'settings', label: 'ACL', admin: true, settingsTab: 'acl' }
    ]
  };
  const pageSections = { overview:'overview', devices:'devices', exits:'devices', sessions:'connections', 'rdp-ingress':'rdp', settings:'settings' };
  const restartNames = { 'server.admin.listen': '管理监听地址', 'server.admin.tls_enabled': '管理访问协议', 'server.tls_enabled': '隧道 TLS', 'server.tls.listen': 'TCP 监听地址', 'server.quic.listen': 'QUIC 监听地址', 'server.cert_file': '证书路径', 'server.key_file': '私钥路径', 'tunnel.heartbeat_sec': '心跳间隔', 'tunnel.max_connections': '设备连接上限', 'tunnel.max_connections_per_device': '每设备并发流上限', relay_acl: '目标访问权限', rdp: 'RDP 公网入口', database: '数据库' };
  const roleNames = { CLIENT: '客户端', EXIT: '出口节点', BOTH: '客户端 + 出口' };
  const capabilityOrder = ['proxy.client', 'proxy.exit', 'rdp.controller', 'rdp.host', 'rdp.public'];
  const capabilityNames = { 'proxy.client': '代理客户端', 'proxy.exit': '出口节点', 'rdp.controller': 'RDP 控制端', 'rdp.host': 'RDP 主机', 'rdp.public': 'RDP 公网入口' };
  const capabilityDescriptions = { 'proxy.client': '通过其他已授权出口转发本机流量', 'proxy.exit': '接收其他设备的代理转发请求', 'rdp.controller': '发起到已授权 RDP 主机的远程桌面连接', 'rdp.host': '向其他已授权设备提供本机 RDP 服务', 'rdp.public': '允许服务端为本机 RDP 分配公网入口' };
  let settingsSubtab = 'admin';
  let toastTimer;
  const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
  const date = value => value && Number.isFinite(new Date(value).getTime()) && new Date(value).getFullYear() > 1970 ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '尚未连接';
  function bytes(n) {
    n = Math.max(0, Number(n) || 0);
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let index = 0;
    while (n >= 1024 && index < units.length - 1) { n /= 1024; index++; }
    return n.toLocaleString('zh-CN', { maximumFractionDigits: index ? 1 : 0 }) + ' ' + units[index];
  }
  function toast(message) {
    $('toast').textContent = message;
    $('toast').hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { $('toast').hidden = true; }, 3600);
  }
  function errorAt(id, message) {
    $(id).textContent = message || '';
    $(id).hidden = !message;
  }
  async function api(path, options = {}) {
    const headers = { Accept: 'application/json', ...options.headers };
    if (options.body != null) { headers['Content-Type'] = 'application/json'; }
    const response = await fetch('/api/v1' + path, { credentials: 'same-origin', cache: 'no-store', ...options, headers });
    let data;
    try { data = await response.json(); } catch (_) { throw new Error('服务器未返回有效数据（HTTP ' + response.status + '）'); }
    if (!response.ok) {
      const err = new Error(data.error || ('请求失败：HTTP ' + response.status));
      err.status = response.status;
      throw err;
    }
    return data;
  }
  async function copy(text) {
    if (!text) { return; }
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        const previous = document.activeElement;
        const input = document.createElement('textarea');
        input.value = text;
        input.style.cssText = 'position:fixed;left:0;top:0;opacity:0;width:1px;height:1px';
        (document.querySelector('dialog[open]') || document.body).appendChild(input);
        input.select();
        let copied;
        try { copied = document.execCommand('copy'); } finally { input.remove(); if (previous) previous.focus(); }
        if (!copied) { throw new Error('clipboard unavailable'); }
      }
      toast('已复制');
    } catch (_) { toast('复制失败，请选择文字手动复制'); }
  }
  function showLogin(message, notice = '') {
    state.user = null;
    all('dialog[open]').forEach(dialog => dialog.close());
    $('app-view').hidden = true;
    $('login-view').hidden = false;
    errorAt('login-error', message);
    errorAt('login-notice', notice);
  }
  async function signedIn(user) {
    if (state.settingsUserID && state.settingsUserID !== user.id) { clearSettings(); }
    state.user = user;
    $('login-view').hidden = true;
    $('app-view').hidden = false;
    $('user-name').textContent = user.username;
    $('user-role').textContent = user.role === 'admin' ? '管理员' : '普通用户';
    $('user-avatar').textContent = (user.username || '?').slice(0, 1).toUpperCase();
    all('[data-admin]').forEach(el => { el.hidden = user.role !== 'admin'; });
    navigate();
    await refresh();
  }
  function applySettingsSubtab() {
    all('#page-settings [data-settings-panel]').forEach(panel => {
      panel.hidden = panel.dataset.settingsPanel !== settingsSubtab;
    });
  }
  function setSettingsSubtab(name) {
    settingsSubtab = name || 'admin';
    applySettingsSubtab();
    renderSectionTabs('settings');
    const first = document.querySelector('#page-settings [data-settings-panel="' + settingsSubtab + '"] input, #page-settings [data-settings-panel="' + settingsSubtab + '"] select, #page-settings [data-settings-panel="' + settingsSubtab + '"] textarea');
    if (first && document.activeElement && document.activeElement.closest && document.activeElement.closest('#secondary-tabs')) {
      first.focus({preventScroll:true});
    }
  }
  function renderSectionTabs(page) {
    const host = $('secondary-tabs');
    if (!host) { return; }
    const section = pageSections[page] || 'overview';
    host.innerHTML = '';
    (sectionPages[section] || []).forEach(tab => {
      if (tab.admin && (!state.user || state.user.role !== 'admin')) { return; }
      const control = document.createElement(tab.settingsTab ? 'button' : 'a');
      if (tab.settingsTab) {
        control.type = 'button';
        control.onclick = function () { setSettingsSubtab(tab.settingsTab); };
      } else {
        control.href = '#' + tab.page;
      }
      const selected = tab.settingsTab ? page === 'settings' && tab.settingsTab === settingsSubtab : tab.page === page;
      control.className = 'rp-tab' + (selected ? ' active' : '');
      control.setAttribute('role', 'tab');
      control.setAttribute('aria-selected', String(selected));
      control.tabIndex = selected ? 0 : -1;
      if (!tab.settingsTab) control.setAttribute('aria-controls', 'page-' + tab.page);
      control.textContent = tab.label;
      host.appendChild(control);
    });
  }
  function navigate() {
    let page = location.hash.slice(1);
    if (!titles[page] || ((page === 'settings' || page === 'rdp-ingress') && (!state.user || state.user.role !== 'admin'))) { page = 'overview'; }
    const section = pageSections[page] || 'overview';
    all('.page').forEach(el => {
      el.hidden = el.id !== 'page-' + page;
      el.setAttribute('role', 'tabpanel');
    });
    all('[data-section]').forEach(el => {
      const active = el.dataset.section === section;
      el.classList.toggle('active', active);
      el.setAttribute('aria-current', active ? 'page' : 'false');
    });
    if (page === 'settings') { applySettingsSubtab(); }
    renderSectionTabs(page);
    $('page-label').textContent = titles[page];
    document.title = titles[page] + ' · RelayProxy';
  }
  function badge(text, tone = 'neutral') { return '<span class="badge ' + tone + '">' + esc(text) + '</span>'; }
  function deviceState(device) { return device.approvalState === 'revoked' ? badge('已撤销', 'warning-badge') : device.status === 'online' ? badge('在线', 'success') : badge('离线'); }
  function transport(value) { return value ? badge(String(value).toUpperCase(), 'transport') : '<span class="muted">—</span>'; }
  function nameCell(name, id) { return '<span class="device-name">' + esc(name || '未命名设备') + '</span><span class="device-id mono">' + esc(id) + '</span>'; }
  function emptyRow(cols, title, subtitle) { return '<tr><td colspan="' + cols + '" class="empty"><strong>' + esc(title) + '</strong>' + esc(subtitle || '') + '</td></tr>'; }
  function details(rows) { return rows.map(([key, val]) => '<div class="detail-row"><span>' + esc(key) + '</span><span>' + esc(val) + '</span></div>').join(''); }
  function orderedCapabilities(capabilities) {
    const unique = Array.from(new Set((capabilities || []).map(value => String(value || '').trim()).filter(Boolean)));
    return capabilityOrder.filter(capability => unique.includes(capability)).concat(unique.filter(capability => !capabilityOrder.includes(capability)));
  }
  function capabilityOptionsHTML(requested, approved) {
    const requestedSet = new Set(requested || []);
    const approvedSet = new Set(approved || []);
    const capabilities = orderedCapabilities(requested);
    if (!capabilities.length) { return '<p class="field-hint">客户端没有声明可审批的能力。</p>'; }
    return capabilities.map(capability => {
      const disabled = capability === 'rdp.public' && !requestedSet.has('rdp.host');
      const checked = approvedSet.has(capability) && !(capability === 'rdp.public' && !approvedSet.has('rdp.host'));
      return '<label class="capability-option"><input type="checkbox" data-capability="' + esc(capability) + '"' + (checked ? ' checked' : '') + (disabled ? ' disabled' : '') + '><span><strong>' + esc(capabilityNames[capability] || capability) + '</strong><small>' + esc(capabilityDescriptions[capability] || '服务端授权的设备能力') + '</small></span></label>';
    }).join('');
  }
  function selectedCapabilities(containerID) {
    return all('#' + containerID + ' [data-capability]:checked').map(input => input.dataset.capability);
  }
  function bindCapabilityDependencies(containerID) {
    const container = $(containerID);
    container.addEventListener('change', event => {
      const input = event.target.closest('[data-capability]');
      if (!input) { return; }
      const host = container.querySelector('[data-capability="rdp.host"]');
      const publicIngress = container.querySelector('[data-capability="rdp.public"]');
      if (input.dataset.capability === 'rdp.public' && input.checked && host) { host.checked = true; }
      if (input.dataset.capability === 'rdp.host' && !input.checked && publicIngress) { publicIngress.checked = false; }
    });
  }
  function renderDevices() {
    const needle = $('device-search').value.trim().toLowerCase();
    const mode = $('device-mode').value, status = $('device-status').value;
    const filtered = state.devices.filter(d => (!needle || (d.name + ' ' + d.id).toLowerCase().includes(needle)) && (!mode || d.deviceMode === mode) && (!status || (status === 'disabled' ? d.approvalState === 'revoked' : d.approvalState === 'approved' && d.status === status)));
    $('devices-body').innerHTML = filtered.length ? filtered.map(d => '<tr><td>' + nameCell(d.name, d.id) + '</td><td>' + esc(roleNames[d.deviceMode] || d.deviceMode) + '<small>' + esc([d.platform, d.arch].filter(Boolean).join(' / ')) + '</small></td><td>' + deviceState(d) + '</td><td>' + transport(d.transport) + '</td><td class="muted">' + esc(date(d.lastSeenAt)) + '</td><td class="right"><button class="small-button" data-manage="' + esc(d.id) + '">管理</button></td></tr>').join('') : emptyRow(6, state.devices.length ? '没有匹配的设备' : '还没有已授权设备', state.devices.length ? '调整搜索或筛选条件' : '启动客户端并连接此服务器，申请会自动出现');
    const recent = state.devices.slice().sort((a, b) => (b.status === 'online') - (a.status === 'online') || (Date.parse(b.lastSeenAt) || 0) - (Date.parse(a.lastSeenAt) || 0)).slice(0, 5);
    $('overview-devices').innerHTML = recent.length ? recent.map(d => '<tr><td>' + nameCell(d.name, d.id) + '</td><td>' + esc(roleNames[d.deviceMode] || d.deviceMode) + '</td><td>' + deviceState(d) + '</td><td>' + transport(d.transport) + '</td><td class="muted">' + esc(date(d.lastSeenAt)) + '</td></tr>').join('') : emptyRow(5, '连接你的第一台设备', '客户端连接后在设备管理中审批');
    $('nav-device-count').textContent = state.devices.length;
    $('stat-total').textContent = '共 ' + state.devices.length + ' 台已注册设备';
    $('device-filter-count').textContent = filtered.length + ' / ' + state.devices.length + ' 台设备';
  }
  function renderEnrollments() {
    if (!$('enrollments-body')) { return; }
    $('enrollment-count').textContent = state.enrollments.length;
    $('enrollments-body').innerHTML = state.enrollments.length ? state.enrollments.map(item => {
      const requested = item.requestedCapabilities || [];
      const caps = orderedCapabilities(requested).map(capability => capabilityNames[capability] || capability).join('、') || '未声明';
      return '<tr><td><span class="device-name">' + esc(item.deviceName || '未命名设备') + '</span><span class="device-id mono">' + esc(item.fingerprint.slice(0, 16)) + '…</span></td><td>' + esc([item.platform, item.arch, item.clientVersion].filter(Boolean).join(' / ') || '—') + '</td><td>' + esc(caps) + '</td><td><small>' + esc(date(item.firstSeenAt)) + '<br>' + esc(date(item.lastSeenAt)) + '</small></td><td class="right"><button class="small-button primary" data-enrollment-manage="' + esc(item.id) + '">审批能力</button> <button class="small-button" data-enrollment-reject="' + esc(item.id) + '">拒绝</button></td></tr>';
    }).join('') : emptyRow(5, '没有待审批申请', '客户端连接后会自动出现在这里');
  }
  function renderExits() {
    $('exits-grid').innerHTML = state.exits.length ? state.exits.map(e => '<article class="panel exit-card"><div class="exit-header"><div><h3>' + esc(e.deviceName || '未命名出口') + '</h3><span class="device-id mono">' + esc(e.deviceId) + '</span></div>' + badge('在线', 'success') + '</div><div class="detail-list">' + details([['传输方式', String(e.transport || '—').toUpperCase()], ['活跃流', e.activeStreams], ['目标权限', '服务端与出口本地共同限制']]) + '</div><button data-copy-exit="' + esc(e.deviceId) + '">复制出口 ID</button></article>').join('') : '<div class="panel empty"><strong>暂无在线出口</strong>将已配对设备设为「出口」或「客户端 + 出口」，并开启出口服务。</div>';
  }
  function renderSessions() {
    const nameFor = id => { const device = state.devices.find(d => d.id === id); return device ? device.name : id; };
    $('sessions-body').innerHTML = state.sessions.length ? state.sessions.map(s => '<tr><td>' + nameCell(s.clientDeviceName, s.clientDeviceId) + '</td><td>' + esc(roleNames[s.mode] || s.mode) + '</td><td>' + esc(s.exitDeviceId ? nameFor(s.exitDeviceId) : '未指定') + '</td><td>' + transport(s.transport) + '</td><td>' + esc(s.activeStreams) + '</td><td class="mono">' + bytes(s.bytesUp) + ' / ' + bytes(s.bytesDown) + '</td></tr>').join('') : emptyRow(6, '当前没有活跃流', '设备发起代理连接后会显示在这里');
  }
  function renderIngress() {
    if (!$('rdp-ingress-body')) { return; }
    const runtimeIngress = state.settings && state.settings.runtime && state.settings.runtime.rdpIngress;
    const desiredIngress = state.settings && state.settings.config && state.settings.config.rdpIngress;
    const managerEnabled = !runtimeIngress || runtimeIngress.enabled;
    const globalHint = $('rdp-ingress-global-hint');
    const createButton = $('rdp-ingress-form').querySelector('button[type="submit"]');
    if (globalHint) {
      globalHint.textContent = managerEnabled ? '入口服务正在运行；创建后会立即绑定同号 TCP/UDP 端口。' : desiredIngress && desiredIngress.enabled ? '配置已保存但服务尚未重启；重启后才能创建并监听公网入口。' : '入口服务未启用；请到“服务配置”开启 RDP 公网入口并重启服务。';
      globalHint.className = 'field-hint' + (managerEnabled ? '' : ' warning-text');
    }
    if (createButton) { createButton.disabled = !managerEnabled; }
    const targets = state.devices.filter(d => d.approvalState === 'approved' && (d.approvedCapabilities || []).includes('rdp.host') && (d.approvedCapabilities || []).includes('rdp.public'));
    $('rdp-ingress-target').innerHTML = targets.length ? targets.map(d => '<option value="' + esc(d.id) + '">' + esc(d.name || d.id) + ' · ' + esc(d.id) + '</option>').join('') : '<option value="">没有已批准公网 RDP 主机</option>';
    $('rdp-ingress-body').innerHTML = state.ingress.length ? state.ingress.map(item => {
      const tcp = item.tcpListening ? badge('TCP', 'success') : badge('TCP 未监听', 'warning-badge');
      const udp = item.udpEnabled ? badge(item.activeUdp > 0 ? 'UDP · 活跃' : 'UDP 已启用', 'success') : badge('UDP：' + (item.udpReason || '不可用'), 'warning-badge');
      return '<tr><td>' + nameCell(item.targetName, item.targetDeviceId) + '<small>' + esc(item.targetTransport ? String(item.targetTransport).toUpperCase() : '目标离线') + '</small></td><td class="mono">' + esc(item.listenPort) + '</td><td class="rdp-ingress-source mono">' + esc((item.sourceCidrs || []).join(', ') || '全部来源') + '</td><td>' + esc(item.rateLimitPerMinute) + ' / 分钟<small>' + esc(item.expiresAt ? date(item.expiresAt) : '不自动过期') + '</small></td><td>' + tcp + ' ' + udp + '</td><td>' + badge(item.status === 'enabled' ? '启用' : '停用', item.status === 'enabled' ? 'success' : 'neutral') + '</td><td class="right"><div class="table-actions"><button type="button" class="small-button" data-rdp-ingress-toggle="' + esc(item.id) + '" data-enabled="' + (item.status === 'enabled' ? 'true' : 'false') + '">' + (item.status === 'enabled' ? '停用' : '启用') + '</button><button type="button" class="small-button danger" data-rdp-ingress-delete="' + esc(item.id) + '">删除</button></div></td></tr>';
    }).join('') : emptyRow(7, '尚未分配公网入口', '创建后服务端会绑定固定 TCP/UDP 端口');
  }
  function renderRuntime() {
    const rows = [['管理访问', location.origin], ['当前账号', state.user && state.user.username]];
    if (state.settings) {
      const runtime = state.settings.runtime;
      rows.push(['TCP 隧道', (runtime.tunnel.tlsEnabled ? 'TLS · ' : 'TCP · ') + runtime.tunnel.tcpListen]);
      rows.push(['QUIC 隧道', runtime.tunnel.tlsEnabled ? runtime.tunnel.quicListen : '未启用']);
      rows.push(['启动时间', date(state.settings.info.startedAt)]);
      const cert = state.settings.info.certificate;
      $('certificate-summary').innerHTML = cert ? '<strong>' + esc(cert.dnsNames.length ? cert.dnsNames.join(' · ') : cert.subject) + '</strong><br>签发者：' + esc(cert.issuer) + '<br>有效期：' + esc(date(cert.notBefore)) + ' — ' + esc(date(cert.notAfter)) + '<div class="mono">SHA256 ' + esc(cert.sha256) + '</div>' : '当前进程没有加载 TLS 证书。';
    }
    $('network-summary').innerHTML = details(rows);
  }
  async function refresh(manual = false) {
    if (!state.user || state.refreshing || state.passwordSaving) { return; }
    state.refreshing = true;
    $('refresh').disabled = true;
    const user = state.user, editVersion = state.editVersion;
    const jobs = [
      ['dashboard', '/dashboard', data => {
        $('stat-devices').textContent = data.onlineDevices;
        $('stat-exits').textContent = data.onlineExits;
        $('stat-streams').textContent = data.activeConnections;
        $('stat-traffic').textContent = bytes(data.todayUpload + data.todayDownload);
        $('stat-traffic-detail').textContent = '↑ ' + bytes(data.todayUpload) + '  ↓ ' + bytes(data.todayDownload);
      }],
      ['devices', '/devices', data => { state.devices = data; renderDevices(); }],
      ['exits', '/exits', data => { state.exits = data; renderExits(); }],
      ['sessions', '/sessions/active', data => { state.sessions = data; renderSessions(); }]
    ];
    if (user.role === 'admin') {
      jobs.push(['enrollments', '/enrollments?state=pending', data => { state.enrollments = data; renderEnrollments(); }]);
      jobs.push(['rdpIngress', '/rdp/ingress', data => { state.ingress = data; renderIngress(); }]);
    } else {
      state.enrollments = [];
      renderEnrollments();
    }
    if (user.role === 'admin' && !state.dirty && !state.saving) {
      jobs.push(['settings', '/server/config', data => {
        if (!state.dirty && !state.saving && state.editVersion === editVersion) { acceptSettings(data); }
      }]);
    }
    const results = await Promise.allSettled(jobs.map(async ([name, path, render]) => {
      const data = await api(path);
      if (state.user === user && !state.passwordSaving) { render(data); }
      return name;
    }));
    state.refreshing = false;
    $('refresh').disabled = false;
    if (state.user !== user || state.passwordSaving) { return; }
    const errors = results.filter(r => r.status === 'rejected').map(r => r.reason);
    if (errors.some(e => e.status === 401)) { showLogin('登录已过期，请重新登录。'); return; }
    errorAt('global-error', errors.length ? '部分数据未能更新：' + [...new Set(errors.map(e => e.message))].join('；') : '');
    $('service-state').textContent = errors.length ? '更新异常' : '服务在线';
    $('service-state').className = 'badge ' + (errors.length ? 'warning-badge' : 'success');
    if (!errors.length) { $('last-refresh').textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN', { hour12: false }); }
    renderRuntime();
    renderSessions();
    renderIngress();
    if (manual && !errors.length) { toast(state.dirty ? '数据已刷新，未保存的配置已保留' : '数据已刷新'); }
  }
  function readSettingsForm() {
    const cfg = { admin: {}, tunnel: {}, certificate: {}, relayACL: {}, rdpIngress: {} };
    all('[data-setting]').forEach(el => {
      const [group, key] = el.dataset.setting.split('.');
      let value = el.type === 'checkbox' ? el.checked : el.value.trim();
      if (el.hasAttribute('data-boolean')) { value = value === 'true'; }
      if (el.type === 'number') { value = Number(el.value); }
      if (el.hasAttribute('data-lines')) { value = value.split('\n').map(s => s.trim()).filter(Boolean); }
      cfg[group][key] = value;
    });
    return cfg;
  }
  function settingsEqual(a, b) {
    return all('[data-setting]').every(el => {
      const [group, key] = el.dataset.setting.split('.');
      return JSON.stringify(a[group][key]) === JSON.stringify(b[group][key]);
    });
  }
  function acceptSettings(data) {
    state.settings = data;
    state.settingsUserID = state.user && state.user.id;
    state.dirty = false;
    all('[data-setting]').forEach(el => {
      const [group, key] = el.dataset.setting.split('.');
      const value = data.config[group][key];
      if (el.type === 'checkbox') { el.checked = value; } else { el.value = Array.isArray(value) ? value.join('\n') : String(value); }
    });
    $('settings-fields').disabled = state.saving;
    $('config-path').textContent = data.configPath;
    $('restart-banner').hidden = !data.restartRequired;
    $('restart-summary').textContent = data.restartFields.map(f => restartNames[f] || f).join('、');
    errorAt('settings-error', '');
    updateSettingState();
    renderRuntime();
    renderIngress();
  }
  function clearSettings() {
    state.settings = null; state.settingsUserID = null; state.dirty = false; state.editVersion++;
    $('settings-fields').disabled = true;
    $('restart-banner').hidden = true;
  }
  function managementURL(listen, tls) {
    const match = /^(\[[^\]]+\]|[^:]*):(\d{1,5})$/.exec(listen.trim());
    if (!match || +match[2] > 65535 || +match[2] < 1) { return ''; }
    let host = match[1];
    if (!host || host === '0.0.0.0' || host === '[::]') { host = location.hostname; }
    return (tls ? 'https://' : 'http://') + host + ':' + Number(match[2]);
  }
  function updateSettingState() {
    if (!state.settings) { return; }
    const cfg = readSettingsForm();
    state.dirty = !settingsEqual(cfg, state.settings.config);
    $('settings-save').disabled = !state.dirty || state.saving;
    $('settings-discard').disabled = !state.dirty || state.saving;
    document.querySelector('.save-bar').classList.toggle('is-clean', !state.dirty && !state.saving);
    $('settings-save').textContent = state.saving ? '正在保存…' : '保存配置';
    $('save-state').textContent = state.saving ? '正在校验并写入配置…' : state.dirty ? '有未保存的修改 · 保存后重启生效' : state.settings.restartRequired ? '已保存 · 等待服务重启' : '没有未保存的修改';
    $('config-status').textContent = state.dirty ? '未保存' : state.settings.restartRequired ? '待重启' : '与运行配置一致';
    $('config-status').className = 'badge ' + (state.dirty || state.settings.restartRequired ? 'warning-badge' : 'success');
    $('settings-dot').hidden = !state.dirty && !state.settings.restartRequired;
    const preview = managementURL(cfg.admin.listen, cfg.admin.tlsEnabled);
    $('admin-preview').textContent = preview || '请填写固定的监听地址和端口';
    $('copy-admin-url').disabled = !preview;
    $('listen-hint').textContent = cfg.admin.listen.startsWith('127.') || cfg.admin.listen.startsWith('[::1]') ? '当前地址仅允许从服务端本机访问。' : '监听所有接口时，预览使用你当前访问的主机名或 IP。更改协议或端口后，请在重启完成后使用新地址。';
    $('quic-listen').disabled = !cfg.tunnel.tlsEnabled;
    $('tunnel-tls-help').textContent = cfg.tunnel.tlsEnabled ? '开启后同时提供加密 TCP 和 QUIC 隧道。' : '当前将仅提供明文 TCP 隧道，QUIC 关闭。';
    $('acl-mode-hint').textContent = cfg.relayACL.accessMode === 'allow' ? '允许列表为空时，所有目标都会被拒绝。匹配目标仍需满足上方互联网 / 私网 / 回环权限。' : cfg.relayACL.accessMode === 'deny' ? '拒绝列表匹配项会被拦截；其余目标仍需满足上方权限。' : '域名和 IP 列表暂不参与筛选，保留内容便于下次启用。上方网络权限仍然有效。';
  }
  async function saveSettings(event) {
    event.preventDefault();
    if (!state.settings || !state.dirty || state.saving) { return; }
    const cfg = readSettingsForm(), revision = state.settings.revision;
    state.saving = true;
    state.editVersion++;
    $('settings-fields').disabled = true;
    errorAt('settings-error', '');
    updateSettingState();
    try {
      const result = await api('/server/config', { method: 'PUT', body: JSON.stringify({ revision, config: cfg }) });
      acceptSettings(result);
      toast(result.restartRequired ? '配置已保存，重启服务后生效' : '配置已保存');
    } catch (err) {
      errorAt('settings-error', err.status === 409 ? err.message + '。当前输入已保留；请先复制需要的修改，再放弃修改并刷新以读取新版本。' : err.message);
    } finally {
      state.saving = false;
      state.editVersion++;
      $('settings-fields').disabled = false;
      updateSettingState();
    }
  }
  function openDevice(id) {
    const device = state.devices.find(d => d.id === id);
    if (!device) { return; }
    state.selectedDevice = device;
    const requested = device.requestedCapabilities && device.requestedCapabilities.length ? device.requestedCapabilities : device.approvedCapabilities;
    const approved = device.approvedCapabilities || [];
    $('device-title').textContent = device.name || '未命名设备';
    $('device-details').innerHTML = details([['设备 ID', device.id], ['角色', roleNames[device.deviceMode] || device.deviceMode], ['系统', [device.platform, device.arch].filter(Boolean).join(' / ') || '—'], ['客户端版本', device.clientVersion || '—'], ['授权状态', device.approvalState === 'approved' ? '已授权' : '已撤销'], ['当前能力', orderedCapabilities(approved).map(capability => capabilityNames[capability] || capability).join('、') || '无'], ['RDP UDP', device.rdpUdpReady ? 'QUIC Datagram 可用' : '不可用（需 QUIC 隧道）'], ['最近在线', date(device.lastSeenAt)]]);
    $('device-capabilities').innerHTML = capabilityOptionsHTML(requested, approved);
    $('device-capability-editor').hidden = !state.user || state.user.role !== 'admin' || device.approvalState !== 'approved';
    $('device-revoke').hidden = device.approvalState !== 'approved' || !state.user || state.user.role !== 'admin';
    errorAt('device-action-error', '');
    errorAt('device-capability-error', '');
    $('device-dialog').showModal();
  }
  function openEnrollment(id) {
    const item = state.enrollments.find(entry => entry.id === id);
    if (!item) { return; }
    state.selectedEnrollment = item;
    $('enrollment-title').textContent = item.deviceName || '未命名设备';
    $('enrollment-summary').textContent = [item.platform, item.arch, item.clientVersion].filter(Boolean).join(' / ') || '客户端申请';
    $('enrollment-capabilities').innerHTML = capabilityOptionsHTML(item.requestedCapabilities || [], item.requestedCapabilities || []);
    $('enrollment-approve').disabled = !(item.requestedCapabilities || []).length;
    errorAt('enrollment-capability-error', '');
    $('enrollment-dialog').showModal();
  }
  async function revokeDevice() {
    if (state.deviceBusy || !state.selectedDevice) { return; }
    const device = state.selectedDevice;
    if (!confirm('撤销「' + (device.name || device.id) + '」的授权并立即断开当前连接？')) { return; }
    state.deviceBusy = true;
    all('#device-dialog button').forEach(button => { button.disabled = true; });
    errorAt('device-action-error', '');
    try {
      await api('/devices/' + encodeURIComponent(device.id) + '/revoke', { method: 'POST', body: '{}' });
      $('device-dialog').close();
      toast('设备授权已撤销');
      await refresh();
    } catch (err) { errorAt('device-action-error', err.message); }
    finally { state.deviceBusy = false; all('#device-dialog button').forEach(button => { button.disabled = false; }); }
  }
  async function saveDeviceCapabilities() {
    if (state.deviceBusy || !state.selectedDevice) { return; }
    const device = state.selectedDevice;
    const capabilities = selectedCapabilities('device-capabilities');
    if (!capabilities.length) {
      errorAt('device-capability-error', '至少保留一项能力；如需完全停用，请撤销设备授权。');
      return;
    }
    if (!confirm('保存设备「' + (device.name || device.id) + '」的新授权？当前连接会立即断开并自动重连。')) { return; }
    state.deviceBusy = true;
    all('#device-dialog button').forEach(button => { button.disabled = true; });
    errorAt('device-capability-error', '');
    try {
      await api('/devices/' + encodeURIComponent(device.id) + '/capabilities', { method: 'PUT', body: JSON.stringify({ capabilities }) });
      $('device-dialog').close();
      toast('设备授权已更新，客户端将自动重连');
      await refresh();
    } catch (err) { errorAt('device-capability-error', err.message); }
    finally { state.deviceBusy = false; all('#device-dialog button').forEach(button => { button.disabled = false; }); }
  }
  async function approveEnrollment() {
    if (state.enrollmentBusy || !state.selectedEnrollment) { return; }
    const item = state.selectedEnrollment;
    const capabilities = selectedCapabilities('enrollment-capabilities');
    if (!capabilities.length) {
      errorAt('enrollment-capability-error', '至少选择一项能力。');
      return;
    }
    if (!confirm('批准设备「' + (item.deviceName || item.fingerprint.slice(0, 12)) + '」选中的能力？')) { return; }
    state.enrollmentBusy = true;
    all('#enrollment-dialog button').forEach(button => { button.disabled = true; });
    errorAt('enrollment-capability-error', '');
    try {
      await api('/enrollments/' + encodeURIComponent(item.id) + '/approve', { method: 'POST', body: JSON.stringify({ capabilities }) });
      $('enrollment-dialog').close();
      toast('设备已批准，客户端将自动重连');
      await refresh();
    } catch (err) { errorAt('enrollment-capability-error', err.message); }
    finally { state.enrollmentBusy = false; all('#enrollment-dialog button').forEach(button => { button.disabled = false; }); }
  }
  async function rejectEnrollment(id) {
    const item = state.enrollments.find(entry => entry.id === id);
    if (!item) { return; }
    if (!confirm('拒绝设备「' + (item.deviceName || item.fingerprint.slice(0, 12)) + '」？')) { return; }
    try {
      await api('/enrollments/' + encodeURIComponent(id) + '/reject', { method: 'POST', body: '{}' });
      if (state.selectedEnrollment && state.selectedEnrollment.id === id && $('enrollment-dialog').open) { $('enrollment-dialog').close(); }
      toast('设备申请已拒绝');
      await refresh();
    } catch (err) { toast(err.message); }
  }
  async function createRDPIngress(event) {
    event.preventDefault();
    errorAt('rdp-ingress-error', '');
    const targetDeviceId = $('rdp-ingress-target').value;
    if (!targetDeviceId) { errorAt('rdp-ingress-error', '请先批准一台具备 rdp.public 能力的 RDP 主机'); return; }
    const body = { targetDeviceId, listenPort: Number($('rdp-ingress-port').value) || 0, sourceCidrs: $('rdp-ingress-cidrs').value.split('\n').map(s => s.trim()).filter(Boolean), rateLimitPerMinute: Number($('rdp-ingress-rate').value) || 120 };
    const expires = $('rdp-ingress-expires').value;
    if (expires) { body.expiresAt = new Date(expires).toISOString(); }
    try { await api('/rdp/ingress', { method: 'POST', body: JSON.stringify(body) }); $('rdp-ingress-form').reset(); $('rdp-ingress-rate').value = 120; toast('RDP 公网入口已创建'); await refresh(true); }
    catch (err) { errorAt('rdp-ingress-error', err.message); }
  }
  async function deleteRDPIngress(id) {
    if (!confirm('删除这个公网 RDP 入口并释放固定端口？')) { return; }
    try { await api('/rdp/ingress/' + encodeURIComponent(id), { method: 'DELETE' }); toast('RDP 公网入口已删除'); await refresh(true); }
    catch (err) { toast(err.message); }
  }
  async function toggleRDPIngress(id, enabled) {
    try { await api('/rdp/ingress/' + encodeURIComponent(id) + '/' + (enabled ? 'disable' : 'enable'), { method: 'POST', body: '{}' }); toast(enabled ? 'RDP 公网入口已停用' : 'RDP 公网入口已启用'); await refresh(true); }
    catch (err) { toast(err.message); }
  }
  $('login-origin').textContent = location.origin;
  $('access-origin').textContent = location.origin;
  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    $('login-submit').disabled = true;
    errorAt('login-error', '');
    try {
      const data = await api('/auth/login', { method: 'POST', body: JSON.stringify({ username: $('username').value.trim(), password: $('password').value }) });
      $('password').value = '';
      await signedIn(data.user);
    } catch (err) { errorAt('login-error', err.message); }
    finally { $('login-submit').disabled = false; }
  });
  async function logout() {
    if (state.saving || state.passwordSaving || (state.dirty && !confirm('有未保存的配置，退出登录并放弃这些修改？'))) { return; }
    try {
      await api('/auth/logout', { method: 'POST' });
      clearSettings();
      showLogin('');
    } catch (err) { toast(err.message); }
  }
  function resetPasswordForm() {
    $('password-form').reset();
    ['current-password', 'new-password', 'confirm-password'].forEach(id => { $(id).value = ''; $(id).type = 'password'; });
    errorAt('password-error', '');
  }
  function openPassword() {
    if (!state.user || state.passwordSaving) { return; }
    if (state.saving) { toast('服务配置正在保存，请稍候再修改密码'); return; }
    resetPasswordForm();
    $('password-account').value = state.user.username;
    $('password-draft-note').hidden = !state.dirty;
    $('password-dialog').showModal();
    $('current-password').focus();
  }
  async function changePassword(event) {
    event.preventDefault();
    if (!state.user || state.passwordSaving || state.saving) { return; }
    errorAt('password-error', '');
    const currentPassword = $('current-password').value, newPassword = $('new-password').value;
    const length = Array.from(newPassword).length;
    if (length < 8 || length > 128 || /^\p{White_Space}*$/u.test(newPassword)) {
      errorAt('password-error', '新密码须为 8–128 个字符，不能全部为空白'); $('new-password').focus(); return;
    }
    if (currentPassword === newPassword) {
      errorAt('password-error', '新密码不能与原密码相同'); $('new-password').focus(); return;
    }
    if (newPassword !== $('confirm-password').value) {
      errorAt('password-error', '两次输入的新密码不一致'); $('confirm-password').focus(); return;
    }
    const user = state.user;
    state.passwordSaving = true;
    $('password-fields').disabled = true;
    all('#password-dialog button').forEach(button => { button.disabled = true; });
    $('password-submit').textContent = '正在保存…';
    try {
      const data = await api('/auth/password', { method: 'PUT', body: JSON.stringify({ currentPassword, newPassword }) });
      $('username').value = user.username;
      $('password').value = '';
      showLogin('', data.message);
      $('password').focus();
    } catch (err) {
      if (err.status === 401) { showLogin('登录已过期，请重新登录。'); }
      else { errorAt('password-error', err.message); }
    } finally {
      state.passwordSaving = false;
      $('password-fields').disabled = false;
      all('#password-dialog button').forEach(button => { button.disabled = false; });
      $('password-submit').textContent = '修改密码并重新登录';
    }
  }
  all('[data-open-password]').forEach(button => button.addEventListener('click', openPassword));
  $('password-form').addEventListener('submit', changePassword);
  $('password-cancel').addEventListener('click', () => { if (!state.passwordSaving) $('password-dialog').close(); });
  $('password-dialog').addEventListener('cancel', event => { if (state.passwordSaving) event.preventDefault(); });
  $('password-dialog').addEventListener('close', resetPasswordForm);
  $('password-visible').addEventListener('change', () => {
    ['current-password', 'new-password', 'confirm-password'].forEach(id => { $(id).type = $('password-visible').checked ? 'text' : 'password'; });
  });
  $('logout').addEventListener('click', logout);
  $('logout-mobile').addEventListener('click', logout);
  $('refresh').addEventListener('click', () => refresh(true));
  window.addEventListener('hashchange', navigate);
  ['device-search', 'device-status', 'device-mode'].forEach(id => $(id).addEventListener('input', renderDevices));
  $('devices-body').addEventListener('click', event => { const button = event.target.closest('[data-manage]'); if (button) openDevice(button.dataset.manage); });
  $('enrollments-body').addEventListener('click', event => {
    const manage = event.target.closest('[data-enrollment-manage]');
    const reject = event.target.closest('[data-enrollment-reject]');
    if (manage) openEnrollment(manage.dataset.enrollmentManage);
    if (reject) rejectEnrollment(reject.dataset.enrollmentReject);
  });
  $('exits-grid').addEventListener('click', event => { const button = event.target.closest('[data-copy-exit]'); if (button) copy(button.dataset.copyExit); });
  $('refresh-enrollments').addEventListener('click', () => refresh(true));
  $('copy-admin-url').addEventListener('click', () => copy(managementURL($('admin-listen').value, $('admin-protocol').value === 'true')));
  $('device-revoke').addEventListener('click', revokeDevice);
  $('device-capabilities-save').addEventListener('click', saveDeviceCapabilities);
  $('enrollment-approve').addEventListener('click', approveEnrollment);
  $('enrollment-reject').addEventListener('click', () => { if (state.selectedEnrollment) rejectEnrollment(state.selectedEnrollment.id); });
  bindCapabilityDependencies('device-capabilities');
  bindCapabilityDependencies('enrollment-capabilities');
  $('device-dialog').addEventListener('cancel', event => { if (state.deviceBusy) event.preventDefault(); });
  $('settings-form').addEventListener('submit', saveSettings);
  $('rdp-ingress-form').addEventListener('submit', createRDPIngress);
  $('rdp-ingress-refresh').addEventListener('click', () => refresh(true));
  $('rdp-ingress-body').addEventListener('click', event => { const toggle = event.target.closest('[data-rdp-ingress-toggle]'); if (toggle) toggleRDPIngress(toggle.dataset.rdpIngressToggle, toggle.dataset.enabled === 'true'); const button = event.target.closest('[data-rdp-ingress-delete]'); if (button) deleteRDPIngress(button.dataset.rdpIngressDelete); });
  $('settings-fields').addEventListener('input', () => { state.editVersion++; updateSettingState(); });
  $('settings-fields').addEventListener('change', updateSettingState);
  $('settings-form').addEventListener('invalid', event => { let parent = event.target.parentElement; while (parent) { if (parent.tagName === 'DETAILS') parent.open = true; parent = parent.parentElement; } }, true);
  $('settings-discard').addEventListener('click', () => { if (state.settings && confirm('放弃尚未保存的配置修改？')) { state.editVersion++; acceptSettings(state.settings); refresh(true); } });
  window.addEventListener('beforeunload', event => { if (state.dirty || state.saving || state.passwordSaving) { event.preventDefault(); event.returnValue = ''; } });
  setInterval(() => { if (!document.hidden) refresh(); }, 15000);
  api('/auth/me').then(signedIn).catch(err => showLogin(err.status === 401 ? '' : '无法连接服务：' + err.message));
})();
