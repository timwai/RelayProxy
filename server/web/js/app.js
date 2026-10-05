'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const all = selector => Array.from(document.querySelectorAll(selector));
  const state = { user: null, devices: [], identities: [], identityGrants: [], systemIdentityGrants: [], enrollments: [], exits: [], sessions: [], p2pSessions: [], messages: [], channels: [], ingress: [], nativeUdp: null, settings: null, settingsUserID: null, dirty: false, saving: false, refreshing: false, editVersion: 0, selectedDevice: null, selectedIdentity: null, selectedEnrollment: null, selectedChannel: null, messageChannel: '', deviceBusy: false, identityBusy: false, identityAssignmentBusy: false, identityGrantBusy: false, systemIdentityGrantBusy: false, rdpTargetBusy: false, enrollmentBusy: false, channelBusy: false, passwordSaving: false };
  const titles = { overview: '总览', devices: '设备管理', identities: '身份管理', exits: '出口节点', sessions: '活跃会话', messages: '消息历史', 'rdp-ingress': 'RDP 公网入口', settings: '服务配置' };
  const sectionPages = {
    overview: [{ page: 'overview', label: '运行总览' }],
    devices: [{ page: 'devices', label: '设备管理' }, { page: 'exits', label: '出口节点' }],
    identities: [{ page: 'identities', label: '身份管理', admin: true }],
    connections: [{ page: 'sessions', label: '活跃会话' }],
    messages: [{ page: 'messages', label: '消息历史' }],
    rdp: [{ page: 'rdp-ingress', label: '公网入口', admin: true }],
    settings: [
      { page: 'settings', label: '管理访问', admin: true, settingsTab: 'admin' },
      { page: 'settings', label: '隧道', admin: true, settingsTab: 'tunnel' },
      { page: 'settings', label: '公网直连', admin: true, settingsTab: 'direct' },
      { page: 'settings', label: 'P2P', admin: true, settingsTab: 'p2p' },
      { page: 'settings', label: 'RDP', admin: true, settingsTab: 'rdp' },
      { page: 'settings', label: 'Server 出口', admin: true, settingsTab: 'exit' },
      { page: 'settings', label: '证书', admin: true, settingsTab: 'certificate' },
      { page: 'settings', label: 'ACL', admin: true, settingsTab: 'acl' }
    ]
  };
  const pageSections = { overview:'overview', devices:'devices', identities:'identities', exits:'devices', sessions:'connections', messages:'messages', 'rdp-ingress':'rdp', settings:'settings' };
  const restartNames = { 'server.admin.listen': '管理监听地址', 'server.admin.tls_enabled': '管理访问协议', 'server.tls_enabled': '隧道 TLS', 'server.tls.listen': 'TCP 监听地址', 'server.quic.listen': 'QUIC 监听地址', 'server.cert_file': '证书路径', 'server.key_file': '私钥路径', 'tunnel.heartbeat_sec': '心跳间隔', 'tunnel.max_connections': '设备连接上限', 'tunnel.max_connections_per_device': '每设备并发流上限', relay_acl: '目标访问权限', exit: 'Server 网络出口', direct: '公网直连', p2p: 'P2P 直连', rdp: 'RDP 公网入口', database: '数据库' };
  const roleNames = { CLIENT: '客户端', EXIT: '出口节点', BOTH: '客户端 + 出口' };
  const capabilityOrder = ['proxy.client', 'proxy.exit', 'rdp.controller', 'rdp.host', 'rdp.public'];
  const capabilityNames = { 'proxy.client': '代理客户端', 'proxy.exit': '出口节点', 'rdp.controller': 'RDP 控制端', 'rdp.host': 'RDP 主机', 'rdp.public': 'RDP 公网入口' };
  const capabilityDescriptions = { 'proxy.client': '通过其他已授权出口转发本机流量', 'proxy.exit': '接收其他设备的代理转发请求', 'rdp.controller': '发起到已授权 RDP 主机的远程桌面连接', 'rdp.host': '向其他已授权设备提供本机 RDP 服务', 'rdp.public': '允许服务端为本机 RDP 分配公网入口' };
  const identityGrantFeatureNames = { 'proxy.use': 'Proxy 出口', 'rdp.connect': 'RDP 连接' };
  const settingsSubtabKey = 'relayproxy-server-settings-tab';
  const validSettingsSubtabs = new Set(sectionPages.settings.map(item => item.settingsTab).filter(Boolean));
  let settingsSubtab = (() => {
    try {
      const saved = localStorage.getItem(settingsSubtabKey);
      return validSettingsSubtabs.has(saved) ? saved : 'admin';
    } catch (_) {
      return 'admin';
    }
  })();
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
    const keepTabFocus = document.activeElement && document.activeElement.closest && document.activeElement.closest('#secondary-tabs');
    settingsSubtab = validSettingsSubtabs.has(name) ? name : 'admin';
    try { localStorage.setItem(settingsSubtabKey, settingsSubtab); } catch (_) {}
    if (location.hash !== '#settings/' + settingsSubtab) {
      history.replaceState(null, '', '#settings/' + settingsSubtab);
    }
    applySettingsSubtab();
    renderSectionTabs('settings');
    if (keepTabFocus) {
      const active = document.querySelector('#secondary-tabs [role="tab"][aria-selected="true"]');
      if (active) active.focus({preventScroll:true});
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
    const rawHash = location.hash.slice(1);
    const parts = rawHash.split('/').filter(Boolean);
    let page = parts[0] || 'overview';
    if (page === 'settings' && parts[1] && validSettingsSubtabs.has(parts[1])) {
      settingsSubtab = parts[1];
      try { localStorage.setItem(settingsSubtabKey, settingsSubtab); } catch (_) {}
    }
    const adminOnlyPage = page === 'settings' || page === 'rdp-ingress' || page === 'identities';
    if (!titles[page] || (adminOnlyPage && (!state.user || state.user.role !== 'admin'))) { page = 'overview'; }
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
  function deviceState(device) { return device.approvalState === 'revoked' ? badge('已撤销', 'warning-badge') : !device.identityId ? badge('待迁移', 'warning-badge') : device.status === 'online' ? badge('在线', 'success') : badge('离线'); }
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
  function renderIdentities() {
    if (!$('identities-body')) { return; }
    $('identity-count').textContent = state.identities.length;
    $('nav-identity-count').textContent = state.identities.length;
    $('identities-body').innerHTML = state.identities.length ? state.identities.map(item => {
      const status = item.status === 'active' ? badge('启用', 'success') : badge('禁用', 'warning-badge');
      const login = item.loginUsername ? ' · 登录：' + item.loginUsername : ' · 登录待配置';
      return '<tr><td><span class="device-name">' + esc(item.name) + '</span><span class="device-id mono">ID：' + esc(item.shortId || item.id) + esc(login) + '</span></td><td>' + status + '</td><td class="mono">' + esc(item.policyRevision) + '</td><td class="muted">' + esc(date(item.updatedAt)) + '</td><td class="right"><button type="button" class="small-button" data-identity-manage="' + esc(item.id) + '">管理</button></td></tr>';
    }).join('') : emptyRow(5, '尚未创建身份', '创建身份后，设备使用自动生成的 16 位 ID 连接，管理员使用自定义用户名登录');
  }

  async function refreshIdentities() {
    if (!state.user) { return; }
    const data = await api(state.user.role === 'admin' ? '/identities' : '/identity-options');
    state.identities = Array.isArray(data) ? data : [];
    renderIdentities();
    syncIdentityGrantFormOptions();
  }

  function localDateTimeValue(value) {
    if (!value) { return ''; }
    const parsed = new Date(value);
    if (!Number.isFinite(parsed.getTime())) { return ''; }
    const local = new Date(parsed.getTime() - parsed.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
  }

  function identityGrantTargetCandidates() {
    return state.devices.filter(device => {
      const capabilities = device.approvedCapabilities || [];
      return device.approvalState === 'approved' && device.identityId &&
        (capabilities.includes('proxy.exit') || capabilities.includes('rdp.host'));
    });
  }

  function syncIdentityGrantCapabilities() {
    if (!$('identity-grant-target')) { return; }
    const target = state.devices.find(device => device.id === $('identity-grant-target').value);
    const granteeID = $('identity-grant-grantee').value;
    const capabilities = target ? target.approvedCapabilities || [] : [];
    const proxy = $('identity-grant-proxy');
    const rdp = $('identity-grant-rdp');
    proxy.disabled = !target || !capabilities.includes('proxy.exit');
    rdp.disabled = !target || !capabilities.includes('rdp.host');
    if (proxy.disabled) { proxy.checked = false; }
    if (rdp.disabled) { rdp.checked = false; }

    all('#identity-grant-grantee option').forEach(option => {
      option.disabled = !!target && option.value === target.identityId;
    });
    if (target && granteeID && granteeID === target.identityId) {
      errorAt('identity-grant-error', '同身份访问自动允许，不需要创建显式授权');
    } else if (!$('identity-grant-error').dataset.serverError) {
      errorAt('identity-grant-error', '');
    }
  }

  function syncIdentityGrantFormOptions() {
    if (!$('identity-grant-target') || !$('identity-grant-grantee')) { return; }
    const editingID = $('identity-grant-id').value;
    const editing = state.identityGrants.find(item => item.id === editingID);
    const targetValue = editing ? editing.targetDeviceId : $('identity-grant-target').value;
    const granteeValue = editing ? editing.granteeIdentityId : $('identity-grant-grantee').value;
    const targets = identityGrantTargetCandidates();

    $('identity-grant-target').innerHTML = '<option value="">请选择目标设备</option>' + targets.map(device => {
      const caps = device.approvedCapabilities || [];
      const services = [];
      if (caps.includes('proxy.exit')) { services.push('Proxy Exit'); }
      if (caps.includes('rdp.host')) { services.push('RDP Host'); }
      const owner = device.identityName || device.identityId;
      return '<option value="' + esc(device.id) + '">' + esc((device.name || device.id) + ' · ' + owner + ' · ' + services.join(' / ')) + '</option>';
    }).join('');

    $('identity-grant-grantee').innerHTML = '<option value="">请选择被授权身份</option>' + state.identities.map(identity => {
      const suffix = identity.status === 'disabled' ? ' · 已禁用' : '';
      return '<option value="' + esc(identity.id) + '">' + esc(identity.name + suffix) + '</option>';
    }).join('');

    if (targetValue && targets.some(item => item.id === targetValue)) { $('identity-grant-target').value = targetValue; }
    if (granteeValue && state.identities.some(item => item.id === granteeValue)) { $('identity-grant-grantee').value = granteeValue; }
    $('identity-grant-target').disabled = !!editing;
    syncIdentityGrantCapabilities();
  }

  function renderIdentityGrants() {
    if (!$('identity-grants-body')) { return; }
    $('identity-grant-count').textContent = state.identityGrants.length;
    const now = Date.now();
    $('identity-grants-body').innerHTML = state.identityGrants.length ? state.identityGrants.map(item => {
      const features = (item.features || []).map(feature => identityGrantFeatureNames[feature] || feature).join('、') || '—';
      const expired = item.expiresAt && Date.parse(item.expiresAt) <= now;
      const expiry = item.expiresAt ? esc(date(item.expiresAt)) + (expired ? ' ' + badge('已过期', 'warning-badge') : '') : '<span class="muted">永不过期</span>';
      return '<tr><td><span class="device-name">' + esc(item.granteeIdentityName || item.granteeIdentityId) + '</span><span class="device-id mono">' + esc(item.granteeIdentityId) + '</span></td>' +
        '<td>' + esc(features) + '</td><td class="muted">' + expiry + '</td>' +
        '<td class="right"><button type="button" class="small-button" data-identity-grant-edit="' + esc(item.id) + '">编辑</button> <button type="button" class="small-button danger" data-identity-grant-delete="' + esc(item.id) + '">删除</button></td></tr>';
    }).join('') : emptyRow(4, '尚未配置跨身份授权', '同身份设备自动互通；只有确实需要跨身份访问时才添加授权');
    syncIdentityGrantFormOptions();
  }

  async function refreshIdentityGrants(deviceID) {
    if (!state.user || !deviceID) { return; }
    const data = await api('/device-identity-grants?targetDeviceId=' + encodeURIComponent(deviceID));
    state.identityGrants = Array.isArray(data) ? data : [];
    renderIdentityGrants();
  }

  function resetIdentityGrantForm() {
    if (!$('identity-grant-form')) { return; }
    $('identity-grant-id').value = '';
    $('identity-grant-revision').value = '';
    $('identity-grant-target').disabled = false;
    $('identity-grant-target').value = '';
    $('identity-grant-grantee').value = '';
    $('identity-grant-proxy').checked = false;
    $('identity-grant-rdp').checked = false;
    $('identity-grant-expires').value = '';
    $('identity-grant-mode').value = '新建授权';
    $('identity-grant-submit').textContent = '创建授权';
    $('identity-grant-cancel').hidden = true;
    delete $('identity-grant-error').dataset.serverError;
    errorAt('identity-grant-error', '');
    syncIdentityGrantFormOptions();
  }

  function editIdentityGrant(id) {
    const item = state.identityGrants.find(grant => grant.id === id);
    if (!item || state.identityGrantBusy) { return; }
    $('identity-grant-id').value = item.id;
    $('identity-grant-revision').value = item.revision;
    $('identity-grant-target').value = item.targetDeviceId;
    $('identity-grant-grantee').value = item.granteeIdentityId;
    $('identity-grant-proxy').checked = (item.features || []).includes('proxy.use');
    $('identity-grant-rdp').checked = (item.features || []).includes('rdp.connect');
    $('identity-grant-expires').value = localDateTimeValue(item.expiresAt);
    $('identity-grant-mode').value = '编辑授权 · rev ' + item.revision;
    $('identity-grant-submit').textContent = '保存授权';
    $('identity-grant-cancel').hidden = false;
    delete $('identity-grant-error').dataset.serverError;
    errorAt('identity-grant-error', '');
    syncIdentityGrantFormOptions();
    $('identity-grant-form').scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  async function submitIdentityGrant(event) {
    event.preventDefault();
    if (state.identityGrantBusy) { return; }
    const id = $('identity-grant-id').value;
    const targetDeviceId = $('identity-grant-target').value;
    const granteeIdentityId = $('identity-grant-grantee').value;
    const target = state.devices.find(device => device.id === targetDeviceId);
    const features = [];
    if ($('identity-grant-proxy').checked) { features.push('proxy.use'); }
    if ($('identity-grant-rdp').checked) { features.push('rdp.connect'); }

    delete $('identity-grant-error').dataset.serverError;
    errorAt('identity-grant-error', '');
    if (!targetDeviceId || !granteeIdentityId) { errorAt('identity-grant-error', '请选择目标设备和被授权身份'); return; }
    if (target && target.identityId === granteeIdentityId) { errorAt('identity-grant-error', '同身份访问自动允许，不需要显式授权'); return; }
    if (!features.length) { errorAt('identity-grant-error', '至少选择一个授权功能'); return; }

    const expiresRaw = $('identity-grant-expires').value;
    let expiresAt = '';
    if (expiresRaw) {
      const parsed = new Date(expiresRaw);
      if (!Number.isFinite(parsed.getTime()) || parsed.getTime() <= Date.now()) {
        errorAt('identity-grant-error', '过期时间必须晚于当前时间');
        return;
      }
      expiresAt = parsed.toISOString();
    }

    state.identityGrantBusy = true;
    $('identity-grant-submit').disabled = true;
    $('identity-grant-cancel').disabled = true;
    try {
      if (id) {
        const payload = {
          granteeIdentityId,
          features,
          revision: Number($('identity-grant-revision').value) || 0
        };
        if (expiresAt) { payload.expiresAt = expiresAt; } else { payload.clearExpiresAt = true; }
        await api('/device-identity-grants/' + encodeURIComponent(id), { method: 'PATCH', body: JSON.stringify(payload) });
        toast('跨身份授权已更新；被授权身份的在线设备将重新认证');
      } else {
        await api('/device-identity-grants', {
          method: 'POST',
          body: JSON.stringify({ targetDeviceId, granteeIdentityId, features, expiresAt })
        });
        toast('跨身份授权已创建；被授权身份的在线设备将重新认证');
      }
      resetIdentityGrantForm();
      $('identity-grant-target').value = targetDeviceId;
      await refreshIdentityGrants(targetDeviceId);
    } catch (err) {
      $('identity-grant-error').dataset.serverError = 'true';
      errorAt('identity-grant-error', err.status === 409 ? err.message + '，请刷新后重试' : err.message);
    } finally {
      state.identityGrantBusy = false;
      $('identity-grant-submit').disabled = false;
      $('identity-grant-cancel').disabled = false;
    }
  }

  async function deleteIdentityGrant(id) {
    const item = state.identityGrants.find(grant => grant.id === id);
    if (!item || state.identityGrantBusy || !confirm('删除此跨身份授权？现有 Relay/P2P/RDP 访问将按最新策略重新校验。')) { return; }
    state.identityGrantBusy = true;
    errorAt('identity-grant-error', '');
    try {
      await api('/device-identity-grants/' + encodeURIComponent(item.id) + '?revision=' + encodeURIComponent(item.revision), { method: 'DELETE' });
      if ($('identity-grant-id').value === item.id) { resetIdentityGrantForm(); }
      toast('跨身份授权已删除；被授权身份的在线设备将重新认证');
      await refreshIdentityGrants(item.targetDeviceId);
    } catch (err) {
      $('identity-grant-error').dataset.serverError = 'true';
      errorAt('identity-grant-error', err.status === 409 ? err.message + '，请刷新后重试' : err.message);
    } finally {
      state.identityGrantBusy = false;
    }
  }


  function syncServerExitGrantIdentityOptions() {
    if (!$('server-exit-grant-identity')) { return; }
    const current = $('server-exit-grant-identity').value;
    const editingID = $('server-exit-grant-id').value;
    const editing = state.systemIdentityGrants.find(item => item.id === editingID);
    const selected = editing ? editing.granteeIdentityId : current;
    const granted = new Set(state.systemIdentityGrants.filter(item => item.id !== editingID).map(item => item.granteeIdentityId));
    $('server-exit-grant-identity').innerHTML = '<option value="">请选择身份</option>' + state.identities.map(identity => {
      const disabled = granted.has(identity.id);
      const suffix = identity.status === 'active' ? '' : ' · 已禁用';
      return '<option value="' + esc(identity.id) + '"' + (disabled ? ' disabled' : '') + '>' + esc(identity.name + suffix) + '</option>';
    }).join('');
    if (selected && state.identities.some(identity => identity.id === selected)) {
      $('server-exit-grant-identity').value = selected;
    }
  }

  function renderSystemIdentityGrants() {
    if (!$('server-exit-grants-body')) { return; }
    const grants = state.systemIdentityGrants.filter(item => item.resourceId === 'server');
    $('server-exit-grant-count').textContent = grants.length + ' 个身份';
    const now = Date.now();
    $('server-exit-grants-body').innerHTML = grants.length ? grants.map(item => {
      const expired = item.expiresAt && Date.parse(item.expiresAt) <= now;
      const expiry = item.expiresAt ? esc(date(item.expiresAt)) + (expired ? ' ' + badge('已过期', 'warning-badge') : '') : '<span class="muted">永不过期</span>';
      return '<tr><td><span class="device-name">' + esc(item.granteeIdentityName || item.granteeIdentityId) + '</span><span class="device-id mono">' + esc(item.granteeIdentityId) + '</span></td>' +
        '<td>Proxy 出口</td><td class="muted">' + expiry + '</td><td class="mono">' + esc(item.revision) + '</td>' +
        '<td class="right"><button type="button" class="small-button" data-server-exit-grant-edit="' + esc(item.id) + '">编辑</button> <button type="button" class="small-button danger" data-server-exit-grant-delete="' + esc(item.id) + '">删除</button></td></tr>';
    }).join('') : emptyRow(5, '尚未授权身份', '启用 Server Exit 后，在这里选择允许使用它的身份');
    syncServerExitGrantIdentityOptions();
  }

  function resetServerExitGrantForm() {
    if (!$('server-exit-grant-id')) { return; }
    $('server-exit-grant-id').value = '';
    $('server-exit-grant-revision').value = '';
    $('server-exit-grant-identity').value = '';
    $('server-exit-grant-identity').disabled = false;
    $('server-exit-grant-expires').value = '';
    $('server-exit-grant-save').textContent = '授权身份';
    $('server-exit-grant-cancel').hidden = true;
    errorAt('server-exit-grant-error', '');
    syncServerExitGrantIdentityOptions();
  }

  function editServerExitGrant(id) {
    const item = state.systemIdentityGrants.find(grant => grant.id === id && grant.resourceId === 'server');
    if (!item || state.systemIdentityGrantBusy) { return; }
    $('server-exit-grant-id').value = item.id;
    $('server-exit-grant-revision').value = item.revision;
    $('server-exit-grant-identity').value = item.granteeIdentityId;
    $('server-exit-grant-identity').disabled = false;
    $('server-exit-grant-expires').value = localDateTimeValue(item.expiresAt);
    $('server-exit-grant-save').textContent = '保存授权';
    $('server-exit-grant-cancel').hidden = false;
    errorAt('server-exit-grant-error', '');
    syncServerExitGrantIdentityOptions();
  }

  async function saveServerExitGrant() {
    if (state.systemIdentityGrantBusy) { return; }
    const id = $('server-exit-grant-id').value;
    const granteeIdentityId = $('server-exit-grant-identity').value;
    if (!granteeIdentityId) {
      errorAt('server-exit-grant-error', '请选择被授权身份');
      return;
    }
    const expiresRaw = $('server-exit-grant-expires').value;
    let expiresAt = '';
    if (expiresRaw) {
      const parsed = new Date(expiresRaw);
      if (!Number.isFinite(parsed.getTime()) || parsed.getTime() <= Date.now()) {
        errorAt('server-exit-grant-error', '过期时间必须晚于当前时间');
        return;
      }
      expiresAt = parsed.toISOString();
    }

    state.systemIdentityGrantBusy = true;
    $('server-exit-grant-save').disabled = true;
    $('server-exit-grant-cancel').disabled = true;
    errorAt('server-exit-grant-error', '');
    try {
      if (id) {
        const payload = {
          granteeIdentityId,
          features: ['proxy.use'],
          revision: Number($('server-exit-grant-revision').value) || 0
        };
        if (expiresAt) { payload.expiresAt = expiresAt; } else { payload.clearExpiresAt = true; }
        await api('/system-identity-grants/' + encodeURIComponent(id), { method: 'PATCH', body: JSON.stringify(payload) });
        toast('Server Exit 身份授权已更新；相关在线设备将重新认证');
      } else {
        await api('/system-identity-grants', {
          method: 'POST',
          body: JSON.stringify({ resourceId: 'server', granteeIdentityId, features: ['proxy.use'], expiresAt })
        });
        toast('Server Exit 已授权给该身份；相关在线设备将重新认证');
      }
      resetServerExitGrantForm();
      await refresh(true);
    } catch (err) {
      errorAt('server-exit-grant-error', err.status === 409 ? err.message + '，请刷新后重试' : err.message);
    } finally {
      state.systemIdentityGrantBusy = false;
      $('server-exit-grant-save').disabled = false;
      $('server-exit-grant-cancel').disabled = false;
    }
  }

  async function deleteServerExitGrant(id) {
    const item = state.systemIdentityGrants.find(grant => grant.id === id && grant.resourceId === 'server');
    if (!item || state.systemIdentityGrantBusy || !confirm('删除此 Server Exit 身份授权？该身份下设备将立即失去 server 出口权限。')) { return; }
    state.systemIdentityGrantBusy = true;
    errorAt('server-exit-grant-error', '');
    try {
      await api('/system-identity-grants/' + encodeURIComponent(item.id) + '?revision=' + encodeURIComponent(item.revision), { method: 'DELETE' });
      if ($('server-exit-grant-id').value === item.id) { resetServerExitGrantForm(); }
      toast('Server Exit 身份授权已删除；相关在线设备将重新认证');
      await refresh(true);
    } catch (err) {
      errorAt('server-exit-grant-error', err.status === 409 ? err.message + '，请刷新后重试' : err.message);
    } finally {
      state.systemIdentityGrantBusy = false;
    }
  }

  async function createIdentity(event) {
    event.preventDefault();
    if (state.identityBusy) { return; }
    const username = $('identity-create-username').value.trim().toLowerCase();
    const name = $('identity-create-name').value.trim();
    const password = $('identity-create-password').value;
    if (!/^[a-z0-9._-]{3,64}$/.test(username)) { errorAt('identity-create-error', '登录用户名必须是 3–64 位小写字母、数字、点、下划线或连字符'); return; }
    if (!name) { errorAt('identity-create-error', '请输入身份名称'); $('identity-create-name').focus(); return; }
    if (password.length < 8) { errorAt('identity-create-error', '初始密码至少需要 8 个字符'); return; }
    state.identityBusy = true;
    $('identity-create-submit').disabled = true;
    errorAt('identity-create-error', '');
    try {
      const created = await api('/identities', { method: 'POST', body: JSON.stringify({ username, name, password }) });
      $('identity-create-username').value = '';
      $('identity-create-name').value = '';
      $('identity-create-password').value = '';
      await refreshIdentities();
      toast('身份已创建，连接 ID：' + created.shortId);
    } catch (err) {
      errorAt('identity-create-error', err.message);
    } finally {
      state.identityBusy = false;
      $('identity-create-submit').disabled = false;
    }
  }

  async function openIdentity(id) {
    if (!state.user || state.user.role !== 'admin' || state.identityBusy) { return; }
    const item = state.identities.find(candidate => candidate.id === id);
    if (!item) { toast('身份不存在或已刷新'); return; }
    state.selectedIdentity = item;
    $('identity-dialog-title').textContent = item.name;
    $('identity-dialog-summary').textContent = '身份 ID：' + item.shortId;
    $('identity-name').value = item.name;
    $('identity-status').value = item.status;
    $('identity-login-username').value = item.loginUsername || '';
    $('identity-reset-password').value = '';
    $('identity-login-state').textContent = item.loginConfigured ? '用户名：' + item.loginUsername : '待配置';
    $('identity-policy-revision').textContent = '版本 ' + item.policyRevision + '；设备能力请在设备管理中配置。';
    errorAt('identity-error', '');
    errorAt('identity-password-error', '');
    $('identity-dialog').showModal();
  }

  async function saveIdentity() {
    const current = state.selectedIdentity;
    if (!current || state.identityBusy) { return; }
    const name = $('identity-name').value.trim();
    const status = $('identity-status').value;
    if (!name) { errorAt('identity-error', '请输入身份名称'); return; }
    state.identityBusy = true;
    $('identity-save').disabled = true;
    errorAt('identity-error', '');
    try {
      const updated = await api('/identities/' + encodeURIComponent(current.id), {
        method: 'PATCH',
        body: JSON.stringify({ name, status, policyRevision: current.policyRevision })
      });
      state.selectedIdentity = updated;
      state.identities = state.identities.map(item => item.id === updated.id ? updated : item);
      renderIdentities();
      $('identity-dialog-title').textContent = updated.name;
      $('identity-policy-revision').textContent = '版本 ' + updated.policyRevision + '；设备能力请在设备管理中配置。';
      toast(status === 'disabled' ? '身份已禁用，在线会话将失效' : '身份配置已保存');
    } catch (err) {
      errorAt('identity-error', err.status === 409 ? err.message + '，请关闭窗口并刷新后重试' : err.message);
    } finally {
      state.identityBusy = false;
      $('identity-save').disabled = false;
    }
  }

  async function resetIdentityPassword() {
    const identity = state.selectedIdentity;
    const username = $('identity-login-username').value.trim().toLowerCase();
    const password = $('identity-reset-password').value;
    if (!identity || state.identityBusy) return;
    if (!/^[a-z0-9._-]{3,64}$/.test(username)) { errorAt('identity-password-error', '登录用户名必须是 3–64 位小写字母、数字、点、下划线或连字符'); return; }
    if (password.length < 8 || password.length > 128 || !password.trim()) { errorAt('identity-password-error', '密码须为 8–128 个字符，且不能全部为空白'); return; }
    state.identityBusy = true;
    $('identity-password-reset').disabled = true;
    errorAt('identity-password-error', '');
    try {
      await api('/identities/' + encodeURIComponent(identity.id) + '/password', { method: 'PUT', body: JSON.stringify({ username, password }) });
      $('identity-reset-password').value = '';
      $('identity-login-state').textContent = '用户名：' + username;
      toast('身份登录配置已保存，原管理会话已注销');
      await refreshIdentities();
    } catch (err) { errorAt('identity-password-error', err.message); }
    finally { state.identityBusy = false; $('identity-password-reset').disabled = false; }
  }

  function renderDevices() {
    const needle = $('device-search').value.trim().toLowerCase();
    const mode = $('device-mode').value, status = $('device-status').value;
    const filtered = state.devices.filter(d => {
      const matchesSearch = !needle || (d.name + ' ' + d.id + ' ' + (d.identityName || '') + ' ' + (d.identityId || '')).toLowerCase().includes(needle);
      const matchesMode = !mode || d.deviceMode === mode;
      const matchesStatus = !status || (status === 'disabled'
        ? d.approvalState === 'revoked'
        : status === 'migration'
          ? d.approvalState === 'approved' && !d.identityId
          : d.approvalState === 'approved' && !!d.identityId && d.status === status);
      return matchesSearch && matchesMode && matchesStatus;
    });
    $('devices-body').innerHTML = filtered.length ? filtered.map(d => {
      const identity = d.identityId ? '<span class="device-name">' + esc(d.identityName || d.identityId) + '</span><span class="device-id mono">' + esc(d.identityId) + '</span>' : '<span class="muted">未分配身份</span>';
      const assign = state.user && state.user.role === 'admin' ? ' <button class="small-button" data-assign-identity="' + esc(d.id) + '">' + (d.identityId ? '更改归属' : '分配身份') + '</button>' : '';
      return '<tr><td>' + nameCell(d.name, d.id) + '</td><td>' + identity + '</td><td>' + esc(roleNames[d.deviceMode] || d.deviceMode) + '<small>' + esc([d.platform, d.arch].filter(Boolean).join(' / ')) + '</small></td><td>' + deviceState(d) + '</td><td>' + transport(d.transport) + '</td><td class="muted">' + esc(date(d.lastSeenAt)) + '</td><td class="right"><button class="small-button" data-manage="' + esc(d.id) + '">管理</button>' + assign + '</td></tr>';
    }).join('') : emptyRow(7, state.devices.length ? '没有匹配的设备' : '还没有已授权设备', state.devices.length ? '调整搜索或筛选条件' : '客户端填写身份 ID 后，设备会进入对应身份的待审批列表');
    const recent = state.devices.slice().sort((a, b) => (b.status === 'online') - (a.status === 'online') || (Date.parse(b.lastSeenAt) || 0) - (Date.parse(a.lastSeenAt) || 0)).slice(0, 5);
    $('overview-devices').innerHTML = recent.length ? recent.map(d => '<tr><td>' + nameCell(d.name, d.id) + '</td><td>' + esc(roleNames[d.deviceMode] || d.deviceMode) + '</td><td>' + deviceState(d) + '</td><td>' + transport(d.transport) + '</td><td class="muted">' + esc(date(d.lastSeenAt)) + '</td></tr>').join('') : emptyRow(5, '连接你的第一台设备', '先创建身份，并在客户端填写身份 ID');
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
      const identity = item.identityName || item.identityId || '未分配身份';
      return '<tr><td><span class="device-name">' + esc(item.deviceName || '未命名设备') + '</span><span class="device-id mono">' + esc(identity) + ' · ' + esc(item.fingerprint.slice(0, 16)) + '…</span></td><td>' + esc([item.platform, item.arch, item.clientVersion].filter(Boolean).join(' / ') || '—') + '</td><td>' + esc(caps) + '</td><td><small>' + esc(date(item.firstSeenAt)) + '<br>' + esc(date(item.lastSeenAt)) + '</small></td><td class="right"><button class="small-button primary" data-enrollment-manage="' + esc(item.id) + '">审批能力</button> <button class="small-button" data-enrollment-reject="' + esc(item.id) + '">拒绝</button></td></tr>';
    }).join('') : emptyRow(5, '没有待审批设备', '新设备使用身份 ID 发起申请，审批后才能连接');
  }
  function publicDirectStatusHTML(value) {
    if (!value) return '<div class="notice subtle"><strong>Public Direct</strong><small>Server 本机出口不使用设备公网直连。</small></div>';
    const endpoints = Array.isArray(value.endpoints) ? value.endpoints : [];
    const stateNames = {unknown:'待验证', verifying:'验证中', verified:'已验证', failed:'验证失败', expired:'已过期'};
    const rows = endpoints.map(item => {
      const usable = !!item.verified;
      const stateName = item.state === 'verifying' && usable
        ? '复验中 · 可用'
        : (stateNames[item.state] || item.state || '未知');
      const stateClass = usable ? 'success' : item.state === 'failed' ? 'warning-badge' : 'neutral';
      const time = item.verifiedAt ? ' · 验证 ' + esc(date(item.verifiedAt)) : '';
      const dial = item.dialAddress && item.dialAddress !== item.address ? '<small>实际拨号：<span class="mono">' + esc(item.dialAddress) + '</span></small>' : '';
      const reason = item.lastError ? '<small>' + esc(item.lastError) + '</small>' : '';
      return '<div><span class="mono">' + esc(item.address) + '</span> ' + badge(stateName, stateClass) + '<small>' + esc(item.source || '') + time + '</small>' + dial + reason + '</div>';
    }).join('');
    const summary = value.available ? badge('Public Direct 可用', 'success') : badge(endpoints.length ? 'Public Direct 未就绪' : 'Public Direct 未注册', endpoints.length ? 'warning-badge' : 'neutral');
    return '<div class="notice subtle"><strong>' + summary + '</strong><small>已验证端点 ' + esc(value.verifiedEndpointCount || 0) + ' 个</small>' + (rows ? '<div class="direct-endpoints">' + rows + '</div>' : '') + '</div>';
  }
  function renderExits() {
    $('exits-grid').innerHTML = state.exits.length ? state.exits.map(e => {
      const direct = publicDirectStatusHTML(e.publicDirect);
      return '<article class="panel exit-card"><div class="exit-header"><div><h3>' + esc(e.deviceName || '未命名出口') + '</h3><span class="device-id mono">' + esc(e.deviceId) + '</span></div>' + badge('在线', 'success') + '</div><div class="detail-list">' + details([['传输方式', String(e.transport || '—').toUpperCase()], ['活跃流', e.activeStreams], ['目标权限', '服务端与出口本地共同限制']]) + '</div>' + direct + '<button data-copy-exit="' + esc(e.deviceId) + '">复制出口 ID</button></article>';
    }).join('') : '<div class="panel empty"><strong>暂无在线出口</strong>将已配对设备设为「出口」或「客户端 + 出口」，并开启出口服务。</div>';
  }
  function renderSessions() {
    const nameFor = id => { const device = state.devices.find(d => d.id === id); return device ? device.name : id; };
    const directNames = {public_direct_quic:'Public Direct QUIC',p2p_quic:'P2P QUIC',relay_quic:'Relay QUIC',relay_tls:'Relay TLS'};
    $('sessions-body').innerHTML = state.sessions.length ? state.sessions.map(s => {
      const relay = s.tunnelDiagnostics && s.tunnelDiagnostics.quic;
      const peer = s.peerDiagnostics && s.peerDiagnostics.payload;
      const peerStatus = peer && peer.status;
      const peerQuic = peerStatus && peerStatus.tunnelDiagnostics && peerStatus.tunnelDiagnostics.quic;
      const directPath = peerStatus && peerStatus.directPath;
      const directState = peerStatus && peerStatus.directState;
      const directRttMs = peerStatus && Number(peerStatus.directRttMs || 0);
      const directError = peerStatus && String(peerStatus.directError || '');
      const directEndpoint = peerStatus && String(peerStatus.directEndpoint || '');
      const directFallbackCount = peerStatus && Number(peerStatus.directFallbackCount || 0);
      const directBytesUp = peerStatus && Number(peerStatus.directBytesUp || 0);
      const directBytesDown = peerStatus && Number(peerStatus.directBytesDown || 0);
      const direct = directPath
        ? '<small>当前路径：' + esc(directNames[directPath] || directPath) +
          (directState ? ' · ' + esc(directState) : '') +
          (directRttMs > 0 ? ' · RTT ' + esc(directRttMs) + ' ms' : '') +
          (directFallbackCount > 0 ? ' · 回退 ' + esc(directFallbackCount) + ' 次' : '') +
          '</small>' +
          (directEndpoint ? '<small>直连端点：<span class="mono">' + esc(directEndpoint) + '</span></small>' : '') +
          ((directBytesUp > 0 || directBytesDown > 0) ? '<small>直连流量：<span class="mono">' + bytes(directBytesUp) + ' / ' + bytes(directBytesDown) + '</span></small>' : '') +
          (directError ? '<small>直连错误：' + esc(directError) + '</small>' : '')
        : '';
      const diagnostic = relay ? '<span class="mono">Relay RTT ' + esc(relay.smoothed_rtt_ms || 0) + ' ms · 丢包 ' + esc(relay.sent_packets_lost || 0) + '</span>' +
        (peerQuic ? '<small>设备 Relay RTT ' + esc(peerQuic.smoothed_rtt_ms || 0) + ' ms · 丢包 ' + esc(peerQuic.sent_packets_lost || 0) + '</small>' : '<small>等待设备心跳诊断</small>') + direct : '<span class="muted">当前传输无 QUIC 统计</span>' + direct;
      return '<tr><td>' + nameCell(s.clientDeviceName, s.clientDeviceId) + '</td><td>' + esc(roleNames[s.mode] || s.mode) + '</td><td>' + esc(s.exitDeviceId ? nameFor(s.exitDeviceId) : '未指定') + '</td><td>' + transport(s.transport) + '</td><td>' + esc(s.activeStreams) + '</td><td class="mono">' + bytes(s.bytesUp) + ' / ' + bytes(s.bytesDown) + '</td><td>' + diagnostic + '</td></tr>';
    }).join('') : emptyRow(7, '当前没有活跃流', '设备发起代理连接后会显示在这里');
  }

  function downloadJSON(filename, value) {
    const blob = new Blob([JSON.stringify(value, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob), link = document.createElement('a');
    link.href = url; link.download = filename; document.body.appendChild(link); link.click(); link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function captureDiagnostics() {
    const button = $('capture-diagnostics'), progress = $('diagnostic-progress');
    if (!button || button.disabled) return;
    button.disabled = true; progress.hidden = false;
    const report = { schemaVersion: 1, startedAt: new Date().toISOString(), durationSeconds: 30, samples: [] };
    try {
      for (let i = 0; i <= 30; i++) {
        progress.textContent = '正在采集三端诊断：' + i + ' / 30 秒。请保持问题流量持续传输。';
        report.samples.push({ collectedAt: new Date().toISOString(), sessions: await api('/sessions/active') });
        if (i < 30) await new Promise(resolve => setTimeout(resolve, 1000));
      }
      report.finishedAt = new Date().toISOString();
      const stamp = report.startedAt.replace(/[-:]/g, '').replace(/\..*/, '').replace('T', '-');
      downloadJSON('relay-diagnostics-' + stamp + '.json', report);
      progress.textContent = '诊断报告已下载。';
      toast('三端诊断采集完成');
    } catch (error) {
      progress.textContent = '诊断采集失败：' + error.message;
    } finally {
      button.disabled = false;
    }
  }
  function p2pReportHTML(report) {
    report = report || {};
    const names = {p2p_quic:'P2P QUIC',relay_quic:'Relay QUIC',relay_tls:'Relay TLS'};
    const parts = [];
    if (report.rttMs > 0) parts.push('RTT ' + esc(report.rttMs) + ' ms');
    if ((report.bytesUp || 0) > 0 || (report.bytesDown || 0) > 0) parts.push('↑ ' + bytes(report.bytesUp) + ' ↓ ' + bytes(report.bytesDown));
    if ((report.fallbackCount || 0) > 0) parts.push('fallback ' + esc(report.fallbackCount));
    if (report.candidateSummary) parts.push(esc(report.candidateSummary));
    const detail = parts.length ? '<small>' + parts.join(' · ') + '</small>' : '';
    if (report.path) {
      const label = names[report.path] || report.path;
      return badge(label, report.path === 'p2p_quic' ? 'success' : 'transport') +
        (report.reason ? '<small>' + esc(report.reason) + '</small>' : '') + detail;
    }
    if (report.reason) return badge('已降级', 'warning-badge') + '<small>' + esc(report.reason) + '</small>' + detail;
    return detail || '<span class="muted">等待报告</span>';
  }
  function renderP2PSessions() {
    const nameFor = id => { const device = state.devices.find(d => d.id === id); return device ? (device.name || id) : id; };
    $('p2p-session-count').textContent = state.p2pSessions.length;
    $('p2p-sessions-body').innerHTML = state.p2pSessions.length ? state.p2pSessions.map(s => {
      const client = nameFor(s.clientDeviceId), exit = nameFor(s.exitDeviceId);
      const ready = s.clientReport && s.clientReport.path === 'p2p_quic' && s.exitReport && s.exitReport.path === 'p2p_quic';
      const status = ready ? badge('DIRECT', 'success') : s.answered ? badge('已应答', 'transport') : badge('协商中', 'neutral');
      return '<tr><td><span class="device-name">' + esc(client) + ' → ' + esc(exit) + '</span><span class="device-id mono">' + esc(s.clientDeviceId) + ' → ' + esc(s.exitDeviceId) + '</span></td><td>' + status + '</td><td>' + p2pReportHTML(s.clientReport) + '</td><td>' + p2pReportHTML(s.exitReport) + '</td><td class="muted">' + esc(date(s.leaseExpiresAt)) + '</td></tr>';
    }).join('') : emptyRow(5, '当前没有 P2P 会话', 'Client 选择支持 P2P 的 Exit 后会在后台建立直连');
  }
  function relayPushOrigin() {
    const tunnel = state.settings && state.settings.runtime && state.settings.runtime.tunnel;
    if (!tunnel || !tunnel.tcpListen) return '';
    const match = /^(?:\[[^\]]+\]|[^:]*):(\d{1,5})$/.exec(String(tunnel.tcpListen).trim());
    if (!match) return '';
    const port = Number(match[1]);
    let host = location.hostname;
    if (host.includes(':') && !host.startsWith('[')) host = '[' + host + ']';
    const secure = !!tunnel.tlsEnabled;
    const scheme = secure ? 'https://' : 'http://';
    const suffix = (secure && port === 443) || (!secure && port === 80) ? '' : ':' + port;
    return scheme + host + suffix;
  }
  function channelPushURL(id) {
    const origin = relayPushOrigin();
    return (origin || '中继地址读取中') + '/api/v1/push/' + encodeURIComponent(id || '');
  }
  function currentChannelIdentityID() {
    if (!state.user) return '';
    if (state.user.role !== 'admin') return state.user.identityId || '';
    if (state.selectedChannel && state.selectedChannel.identityId) return state.selectedChannel.identityId;
    return $('channel-identity') ? $('channel-identity').value : '';
  }
  function channelScopedDevices() {
    const identityId = currentChannelIdentityID();
    if (!identityId) return [];
    return state.devices.filter(device => device.identityId === identityId);
  }
  function renderChannelIdentityOptions(selected = '') {
    const select = $('channel-identity');
    if (!select) return;
    const identities = state.identities.filter(identity => identity && identity.id);
    select.innerHTML = '<option value="">请选择身份</option>' + identities.map(identity =>
      '<option value="' + esc(identity.id) + '">' + esc(identity.name || identity.shortId || identity.id) + '</option>'
    ).join('');
    select.value = selected || '';
  }
  function channelDeviceLabel(channel) {
    if (channel.allDevices) return '本身份全部已批准设备';
    const ids = Array.isArray(channel.deviceIds) ? channel.deviceIds : [];
    const names = ids.map(id => {
      const device = state.devices.find(item => item.id === id);
      return device ? (device.name || id) : id;
    });
    return names.length ? names.join('、') : '未绑定设备';
  }
  function renderChannels() {
    const host = $('channel-list');
    if (!host) return;
    $('channel-count').textContent = state.channels.length;
    if (!state.channels.length) {
      host.innerHTML = '<div class="channel-empty"><strong>还没有推送渠道</strong><span>创建渠道后，外部系统只需要调用渠道 URL，不需要传设备 ID。</span></div>';
      return;
    }
    host.innerHTML = state.channels.map(channel => {
      const url = channelPushURL(channel.id);
      const identity = state.identities.find(item => item.id === channel.identityId);
      const identityBadge = state.user && state.user.role === 'admin'
        ? '<span class="badge neutral">' + esc(identity ? (identity.name || identity.shortId) : (channel.identityId || '未绑定身份')) + '</span>'
        : '';
      const fallback = channel.allDevices ? '<span class="badge success">兜底：本身份全部设备</span>' : '<span class="badge neutral">兜底：' + esc((channel.deviceIds || []).length) + ' 台</span>';
      const routeCount = (channel.routeRules || []).length;
      const customCount = (channel.verificationRules || []).length;
      const ruleBadges = (routeCount ? '<span class="badge transport">分流 ' + esc(routeCount) + '</span>' : '') +
        (customCount ? '<span class="badge transport">识别 ' + esc(customCount) + '</span>' : '');
      return '<article class="channel-card"><div class="channel-card-head"><div><strong>' + esc(channel.name) + '</strong><code class="mono">' + esc(channel.id) + '</code></div><div>' + identityBadge + fallback + ruleBadges + '</div></div><p>' + esc(channelDeviceLabel(channel)) + (routeCount ? ' · ' + routeCount + ' 条内容分流' : '') + '</p><div class="channel-url"><code class="mono">' + esc(url) + '</code></div><div class="channel-actions"><button type="button" class="small-button" data-channel-messages="' + esc(channel.id) + '">查看消息</button><button type="button" class="small-button" data-channel-copy="' + esc(channel.id) + '">复制接口</button><button type="button" class="small-button" data-channel-edit="' + esc(channel.id) + '">编辑</button></div></article>';
    }).join('');
  }
  function renderChannelDevices(selected) {
    const selectedSet = new Set(selected || []);
    const host = $('channel-devices');
    const devices = channelScopedDevices().slice().sort((a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id), 'zh-CN'));
    host.innerHTML = devices.length ? devices.map(device => {
      const checked = selectedSet.has(device.id);
      const status = device.approvalState === 'approved' ? (device.status === 'online' ? '在线' : '离线') : '已撤销';
      return '<label class="channel-device-option"><input type="checkbox" value="' + esc(device.id) + '"' + (checked ? ' checked' : '') + '><span><strong>' + esc(device.name || device.id) + '</strong><small class="mono">' + esc(device.id) + ' · ' + esc(status) + '</small></span></label>';
    }).join('') : '<div class="channel-empty"><span>还没有可绑定设备</span></div>';
    updateChannelDeviceState();
  }
  function routeDeviceOptions(selected) {
    const selectedSet = new Set(selected || []);
    return channelScopedDevices().slice().sort((a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id), 'zh-CN')).map(device => {
      const status = device.approvalState === 'approved' ? (device.status === 'online' ? '在线' : '离线') : '已撤销';
      return '<option value="' + esc(device.id) + '"' + (selectedSet.has(device.id) ? ' selected' : '') + '>' + esc((device.name || device.id) + ' · ' + status) + '</option>';
    }).join('');
  }
  function verificationRuleHTML(rule = {}, index = 0) {
    const keywords = Array.isArray(rule.keywords) ? rule.keywords.join(', ') : '';
    const distance = Number(rule.maxDistance) > 0 ? Number(rule.maxDistance) : 64;
    return '<article class="channel-rule-card" data-verification-rule><div class="channel-rule-card-head"><strong>自定义识别规则 ' + (index + 1) + '</strong><button type="button" class="small-button danger" data-verification-remove>删除</button></div><div class="channel-rule-grid">' +
      '<label>名称<input data-verification-name maxlength="80" value="' + esc(rule.name || '') + '" placeholder="例如：四川移动附加码"></label>' +
      '<label>最大距离（字符附近）<input data-verification-distance type="number" min="0" max="1024" value="' + esc(distance) + '"></label>' +
      '<label class="wide">关键词（逗号分隔）<input data-verification-keywords value="' + esc(keywords) + '" placeholder="附加码, 动态密钥, 登录口令"></label>' +
      '<label class="wide">验证码正则<input data-verification-pattern class="mono" maxlength="500" value="' + esc(rule.pattern || '') + '" placeholder="例如：([A-Z0-9]{4,8})；留空使用默认候选格式"></label>' +
      '</div><label class="channel-rule-check"><input data-verification-case type="checkbox"' + (rule.caseSensitive ? ' checked' : '') + '>区分大小写</label><p class="channel-fallback-note">正则含捕获组时返回第一个捕获组；没有捕获组时返回整个匹配。</p></article>';
  }
  function routeRuleHTML(rule = {}, index = 0) {
    const matchType = rule.matchType === 'regex' ? 'regex' : 'contains';
    return '<article class="channel-rule-card" data-route-rule><div class="channel-rule-card-head"><strong>分流规则 ' + (index + 1) + '</strong><button type="button" class="small-button danger" data-route-remove>删除</button></div><div class="channel-rule-grid">' +
      '<label>名称<input data-route-name maxlength="120" value="' + esc(rule.name || '') + '" placeholder="例如：4A 系统"></label>' +
      '<label>匹配方式<select data-route-type><option value="contains"' + (matchType === 'contains' ? ' selected' : '') + '>包含文本</option><option value="regex"' + (matchType === 'regex' ? ' selected' : '') + '>正则表达式</option></select></label>' +
      '<label class="wide">匹配内容<input data-route-pattern class="mono" maxlength="500" value="' + esc(rule.pattern || '') + '" placeholder="例如：4A系统 或 四川移动.*EIP"></label>' +
      '<label class="wide">目标设备<select data-route-devices class="channel-route-devices" multiple>' + routeDeviceOptions(rule.deviceIds || []) + '</select></label>' +
      '</div><label class="channel-rule-check"><input data-route-case type="checkbox"' + (rule.caseSensitive ? ' checked' : '') + '>区分大小写</label>' +
      '<label class="channel-rule-check"><input data-route-all type="checkbox"' + (rule.allDevices ? ' checked' : '') + '>命中后推送到全部已批准设备</label></article>';
  }
  function renderChannelRules(channel) {
    const verificationRules = channel && Array.isArray(channel.verificationRules) ? channel.verificationRules : [];
    const routeRules = channel && Array.isArray(channel.routeRules) ? channel.routeRules : [];
    $('channel-use-default-verification').checked = !channel || channel.useDefaultVerification !== false;
    $('channel-verification-rules').innerHTML = verificationRules.length ? verificationRules.map(verificationRuleHTML).join('') : '<div class="channel-rule-empty">没有自定义规则，将使用默认验证码识别。</div>';
    $('channel-route-rules').innerHTML = routeRules.length ? routeRules.map(routeRuleHTML).join('') : '<div class="channel-rule-empty">没有内容分流，消息会直接推送到上方兜底设备。</div>';
    syncRouteRuleDeviceStates();
  }
  function addVerificationRule(rule = {}) {
    const host = $('channel-verification-rules');
    if (host.querySelector('.channel-rule-empty')) host.innerHTML = '';
    const index = host.querySelectorAll('[data-verification-rule]').length;
    host.insertAdjacentHTML('beforeend', verificationRuleHTML(rule, index));
  }
  function addRouteRule(rule = {}) {
    const host = $('channel-route-rules');
    if (host.querySelector('.channel-rule-empty')) host.innerHTML = '';
    const index = host.querySelectorAll('[data-route-rule]').length;
    host.insertAdjacentHTML('beforeend', routeRuleHTML(rule, index));
    syncRouteRuleDeviceStates();
  }
  function renumberChannelRules() {
    all('#channel-verification-rules [data-verification-rule]').forEach((card, index) => { const title = card.querySelector('.channel-rule-card-head strong'); if (title) title.textContent = '自定义识别规则 ' + (index + 1); });
    all('#channel-route-rules [data-route-rule]').forEach((card, index) => { const title = card.querySelector('.channel-rule-card-head strong'); if (title) title.textContent = '分流规则 ' + (index + 1); });
    if (!$('channel-verification-rules').children.length) $('channel-verification-rules').innerHTML = '<div class="channel-rule-empty">没有自定义规则，将使用默认验证码识别。</div>';
    if (!$('channel-route-rules').children.length) $('channel-route-rules').innerHTML = '<div class="channel-rule-empty">没有内容分流，消息会直接推送到上方兜底设备。</div>';
  }
  function syncRouteRuleDeviceStates() {
    all('#channel-route-rules [data-route-rule]').forEach(card => {
      const allDevices = card.querySelector('[data-route-all]').checked;
      const select = card.querySelector('[data-route-devices]');
      if (select) select.disabled = allDevices || state.channelBusy;
    });
  }
  function readVerificationRules() {
    return all('#channel-verification-rules [data-verification-rule]').map(card => ({
      name: card.querySelector('[data-verification-name]').value.trim(),
      keywords: card.querySelector('[data-verification-keywords]').value.split(/[,，\n]+/).map(value => value.trim()).filter(Boolean),
      pattern: card.querySelector('[data-verification-pattern]').value.trim(),
      maxDistance: Number(card.querySelector('[data-verification-distance]').value) || 0,
      caseSensitive: card.querySelector('[data-verification-case]').checked
    }));
  }
  function readRouteRules() {
    return all('#channel-route-rules [data-route-rule]').map(card => {
      const allDevices = card.querySelector('[data-route-all]').checked;
      return {
        name: card.querySelector('[data-route-name]').value.trim(),
        matchType: card.querySelector('[data-route-type]').value,
        pattern: card.querySelector('[data-route-pattern]').value.trim(),
        caseSensitive: card.querySelector('[data-route-case]').checked,
        allDevices,
        deviceIds: allDevices ? [] : Array.from(card.querySelector('[data-route-devices]').selectedOptions).map(option => option.value)
      };
    });
  }
  function updateChannelDeviceState() {
    const allDevices = $('channel-all-devices').checked;
    $('channel-device-section').hidden = allDevices;
    const selected = all('#channel-devices input:checked');
    $('channel-device-count').textContent = selected.length + ' 台';
    const id = $('channel-id').value.trim();
    $('channel-url-preview').textContent = id ? channelPushURL(id) : (relayPushOrigin() || '中继地址读取中') + '/api/v1/push/{保存后生成的渠道ID}';
  }
  function openChannel(id) {
    const channel = id ? state.channels.find(item => item.id === id) : null;
    state.selectedChannel = channel || null;
    $('channel-dialog-title').textContent = channel ? '编辑推送渠道' : '新建推送渠道';
    renderChannelIdentityOptions(channel ? channel.identityId : '');
    $('channel-identity').disabled = !!(channel && channel.identityId);
    $('channel-name').value = channel ? channel.name : '';
    $('channel-id').value = channel ? channel.id : '';
    $('channel-id').disabled = !!channel;
    $('channel-all-devices').checked = channel ? !!channel.allDevices : false;
    $('channel-delete').hidden = !channel;
    renderChannelDevices(channel ? channel.deviceIds : []);
    renderChannelRules(channel);
    errorAt('channel-error', '');
    updateChannelDeviceState();
    $('channel-dialog').showModal();
    setTimeout(() => $('channel-name').focus(), 0);
  }
  async function saveChannel(event) {
    event.preventDefault();
    if (state.channelBusy) return;
    const allDevices = $('channel-all-devices').checked;
    const deviceIds = allDevices ? [] : all('#channel-devices input:checked').map(input => input.value);
    const body = {
      name: $('channel-name').value.trim(),
      allDevices,
      deviceIds,
      useDefaultVerification: $('channel-use-default-verification').checked,
      verificationRules: readVerificationRules(),
      routeRules: readRouteRules()
    };
    if (state.user && state.user.role === 'admin') {
      body.identityId = $('channel-identity').value;
      if (!body.identityId) {
        errorAt('channel-error', '请选择渠道所属身份。');
        return;
      }
    }
    if (!body.useDefaultVerification && !body.verificationRules.length) {
      errorAt('channel-error', '请启用默认验证码识别，或至少添加一条自定义识别规则。');
      return;
    }
    if (!allDevices && !deviceIds.length && !body.routeRules.length) {
      errorAt('channel-error', '请选择兜底设备、启用全部设备，或至少添加一条内容分流规则。');
      return;
    }
    if (!state.selectedChannel) body.id = $('channel-id').value.trim();
    state.channelBusy = true;
    all('#channel-dialog button, #channel-dialog input, #channel-dialog select, #channel-dialog textarea').forEach(el => { el.disabled = true; });
    errorAt('channel-error', '');
    try {
      if (state.selectedChannel) {
        await api('/message-channels/' + encodeURIComponent(state.selectedChannel.id), { method: 'PUT', body: JSON.stringify(body) });
      } else {
        await api('/message-channels', { method: 'POST', body: JSON.stringify(body) });
      }
      $('channel-dialog').close();
      toast(state.selectedChannel ? '渠道已更新' : '渠道已创建');
      state.selectedChannel = null;
      await refresh(true);
    } catch (err) {
      errorAt('channel-error', err.message);
    } finally {
      state.channelBusy = false;
      all('#channel-dialog button, #channel-dialog input, #channel-dialog select, #channel-dialog textarea').forEach(el => { el.disabled = false; });
      $('channel-id').disabled = !!state.selectedChannel;
      $('channel-identity').disabled = !!(state.selectedChannel && state.selectedChannel.identityId);
      syncRouteRuleDeviceStates();
    }
  }

    async function deleteChannel() {
    if (state.channelBusy || !state.selectedChannel) return;
    const channel = state.selectedChannel;
    if (!confirm('删除渠道「' + channel.name + '」？删除后该渠道 URL 将立即失效。')) return;
    state.channelBusy = true;
    try {
      await api('/message-channels/' + encodeURIComponent(channel.id), { method: 'DELETE' });
      $('channel-dialog').close();
      state.selectedChannel = null;
      if (state.messageChannel === channel.id) state.messageChannel = '';
      toast('渠道已删除');
      await refresh(true);
    } catch (err) {
      errorAt('channel-error', err.message);
    } finally {
      state.channelBusy = false;
    }
  }

  function selectedMessageChannel() {
    return state.messageChannel ? state.channels.find(channel => channel.id === state.messageChannel) || null : null;
  }
  function messageListPath() {
    const channel = state.messageChannel ? '&channelId=' + encodeURIComponent(state.messageChannel) : '';
    return '/messages?limit=500' + channel;
  }
  async function showChannelMessages(id) {
    if (!id || state.refreshing) return;
    state.messageChannel = id;
    $('message-search').value = '';
    $('message-kind').value = '';
    $('message-status').value = '';
    await refresh(true);
  }
  async function showAllMessages() {
    if (!state.messageChannel || state.refreshing) return;
    state.messageChannel = '';
    $('message-search').value = '';
    $('message-kind').value = '';
    $('message-status').value = '';
    await refresh(true);
  }
  async function clearServerMessages(channelID = '') {
    if (!state.user || state.user.role !== 'admin') return;
    const channel = channelID ? state.channels.find(item => item.id === channelID) : null;
    const label = channelID ? '渠道「' + (channel ? channel.name : channelID) + '」的全部消息' : '服务端全部消息';
    if (!confirm('确定清空' + label + '？此操作会同时删除对应投递记录，且不可恢复。')) return;
    try {
      const query = channelID ? '?channelId=' + encodeURIComponent(channelID) : '';
      const result = await api('/messages' + query, { method: 'DELETE' });
      toast('已清除 ' + (Number(result.deleted) || 0) + ' 条消息');
      await refresh(true);
    } catch (err) {
      toast(err.message);
    }
  }
  async function deleteServerMessage(id) {
    if (!id || !state.user || state.user.role !== 'admin') return;
    if (!confirm('删除这条消息？对应设备投递记录也会一并删除。')) return;
    try {
      await api('/messages/' + encodeURIComponent(id), { method: 'DELETE' });
      toast('消息已删除');
      await refresh(true);
    } catch (err) {
      toast(err.message);
    }
  }

  function renderMessages() {
    if (!$('messages-body')) { return; }
    const selectedChannel = selectedMessageChannel();
    const context = $('message-channel-context');
    if (context) {
      context.hidden = !state.messageChannel;
      if (state.messageChannel) {
        $('message-channel-title').textContent = selectedChannel ? selectedChannel.name + ' · 消息' : '渠道消息';
        $('message-channel-id').textContent = state.messageChannel;
      }
    }
    if ($('messages-clear')) $('messages-clear').hidden = !state.user || state.user.role !== 'admin';
    if ($('messages-clear-channel')) $('messages-clear-channel').hidden = !state.user || state.user.role !== 'admin';
    const needle = ($('message-search') && $('message-search').value || '').trim().toLowerCase();
    const kind = $('message-kind') ? $('message-kind').value : '';
    const status = $('message-status') ? $('message-status').value : '';
    const filtered = state.messages.filter(message => {
      const code = String(message.verificationCode || '');
      const deliveries = Array.isArray(message.deliveries) ? message.deliveries : [];
      if (kind === 'code' && !code) return false;
      if (kind === 'normal' && code) return false;
      if (status && !deliveries.some(delivery => delivery.status === status)) return false;
      if (!needle) return true;
      return [
        message.title, message.content, message.source, message.channelId, message.routeRule, code,
        ...deliveries.flatMap(delivery => [delivery.deviceName, delivery.deviceId, delivery.status])
      ].join(' ').toLowerCase().includes(needle);
    });
    $('nav-message-count').textContent = state.messages.length;
    $('message-summary').textContent = '显示 ' + filtered.length + ' / ' + state.messages.length + ' 条';
    $('messages-body').innerHTML = filtered.length ? filtered.map(message => {
      const code = String(message.verificationCode || '').trim();
      const deliveries = Array.isArray(message.deliveries) ? message.deliveries : [];
      const deliveryHTML = deliveries.length ? deliveries.map(delivery => {
        const tone = delivery.status === 'delivered' ? 'success' : delivery.status === 'failed' ? 'warning-badge' : delivery.status === 'offline' ? 'neutral' : 'transport';
        const label = delivery.status === 'delivered' ? '已送达' : delivery.status === 'failed' ? '失败' : delivery.status === 'offline' ? '离线' : '等待';
        const error = delivery.error ? '<small title="' + esc(delivery.error) + '">' + esc(delivery.error) + '</small>' : '';
        return '<div class="message-delivery"><span><strong>' + esc(delivery.deviceName || delivery.deviceId) + '</strong><small class="mono">' + esc(delivery.deviceId) + '</small></span>' + badge(label, tone) + error + '</div>';
      }).join('') : '<span class="muted">—</span>';
      const codeHTML = code ? '<div class="message-code"><span class="mono">' + esc(code) + '</span><button type="button" class="small-button" data-copy-message-code="' + esc(code) + '">复制</button></div>' : '<span class="muted">—</span>';
      const actionHTML = state.user && state.user.role === 'admin' ? '<button type="button" class="small-button danger" data-delete-message="' + esc(message.id) + '">删除</button>' : '<span class="muted">—</span>';
      return '<tr><td><strong>' + esc(date(message.createdAt)) + '</strong><small>' + esc(message.source || '渠道推送') + (message.channelId ? ' · ' + esc(message.channelId) : '') + (message.routeRule ? ' · 分流：' + esc(message.routeRule) : '') + '</small></td><td><strong>' + esc(message.title || 'RelayProxy 消息') + '</strong><small class="message-content">' + esc(message.content || '') + '</small></td><td>' + codeHTML + '</td><td><div class="message-deliveries">' + deliveryHTML + '</div></td><td class="right">' + actionHTML + '</td></tr>';
    }).join('') : emptyRow(5, '暂无匹配消息', state.messageChannel ? '这个渠道还没有消息' : '渠道推送后会显示在这里');
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
      rows.push(['Server 出口', runtime.serverExit && runtime.serverExit.enabled ? '已启用 · ID server · ' + String(runtime.serverExit.upstreamMode || 'direct').toUpperCase() : '未启用']);
      rows.push(['公网直连', runtime.direct && runtime.direct.enabled ? '已启用 · UDP ' + (runtime.direct.portStart || 0) + '-' + (runtime.direct.portEnd || 0) : '未启用']);
      rows.push(['启动时间', date(state.settings.info.startedAt)]);
      const cert = state.settings.info.certificate;
      $('certificate-summary').innerHTML = cert ? '<strong>' + esc(cert.dnsNames.length ? cert.dnsNames.join(' · ') : cert.subject) + '</strong><br>签发者：' + esc(cert.issuer) + '<br>有效期：' + esc(date(cert.notBefore)) + ' — ' + esc(date(cert.notAfter)) + '<div class="mono">SHA256 ' + esc(cert.sha256) + '</div>' : '当前进程没有加载 TLS 证书。';
    }
    if (state.nativeUdp) {
      const udp = state.nativeUdp;
      rows.push(['Native UDP', (udp.associations || 0) + ' 个关联 · 队列 ' + bytes(udp.queueBytes) + ' · 重组 ' + bytes(udp.reassemblyBytes)]);
      rows.push(['UDP 过载丢弃', '队列 ' + (udp.queueDrops || 0) + ' · 重组 ' + (udp.reassemblyDrops || 0) + ' · 关联拒绝 ' + (udp.associationRejects || 0)]);
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
        state.nativeUdp = data.nativeUdp || null;
        $('stat-devices').textContent = data.onlineDevices;
        $('stat-exits').textContent = data.onlineExits;
        $('stat-streams').textContent = data.activeConnections;
        $('stat-p2p').textContent = data.activeP2PSessions || 0;
        $('stat-traffic').textContent = bytes(data.todayUpload + data.todayDownload);
        $('stat-traffic-detail').textContent = '↑ ' + bytes(data.todayUpload) + '  ↓ ' + bytes(data.todayDownload);
      }],
      ['devices', '/devices', data => { state.devices = data; renderDevices(); }],
      ['exits', '/exits', data => { state.exits = data; renderExits(); }],
      ['sessions', '/sessions/active', data => { state.sessions = data; renderSessions(); }],
      ['p2pSessions', '/p2p/sessions', data => { state.p2pSessions = Array.isArray(data) ? data : []; renderP2PSessions(); }],
      ['messages', messageListPath(), data => { state.messages = Array.isArray(data) ? data : []; renderMessages(); }],
      ['channels', '/message-channels', data => { state.channels = Array.isArray(data) ? data : []; renderChannels(); }],
      ['identityOptions', user.role === 'admin' ? '/identities' : '/identity-options', data => { state.identities = Array.isArray(data) ? data : []; if (user.role === 'admin') renderIdentities(); syncIdentityGrantFormOptions(); }],
      ['enrollments', '/enrollments?state=pending', data => { state.enrollments = data; renderEnrollments(); }]
    ];
    if (user.role === 'admin') {
      jobs.push(['systemIdentityGrants', '/system-identity-grants?resourceId=server', data => { state.systemIdentityGrants = Array.isArray(data) ? data : []; renderSystemIdentityGrants(); }]);
      jobs.push(['rdpIngress', '/rdp/ingress', data => { state.ingress = data; renderIngress(); }]);
    } else {
      state.systemIdentityGrants = [];
      renderSystemIdentityGrants();
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
    renderIdentityGrants();
    syncIdentityGrantFormOptions();
    renderSystemIdentityGrants();
    renderSessions();
    renderP2PSessions();
    renderMessages();
    renderChannels();
    renderIngress();
    if (manual && !errors.length) { toast(state.dirty ? '数据已刷新，未保存的配置已保留' : '数据已刷新'); }
  }
  function readSettingsForm() {
    const cfg = { admin: {}, tunnel: {}, certificate: {}, relayACL: {}, serverExit: {}, rdpIngress: {}, p2p: {}, direct: {} };
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
    $('tunnel-tls-help').textContent = cfg.tunnel.tlsEnabled ? '开启后同时提供加密 TCP 和 QUIC 隧道。' : '关闭 TLS 后仍可使用 TCP，但隧道内容不会被 TLS 加密；QUIC 也会关闭。';
    const directEnabled = !!cfg.direct.enabled;
    $('direct-port-start').disabled = !directEnabled;
    $('direct-port-end').disabled = !directEnabled;
    $('direct-hint').textContent = directEnabled
      ? 'Server 会验证 Agent 公网 UDP 端点后才下发给客户端；0 / 0 表示由 Agent 使用系统随机端口。'
      : '关闭后 Server 不再广告 Public Direct 能力、验证端点或签发直连 Ticket。';
    $('acl-mode-hint').textContent = cfg.relayACL.accessMode === 'allow' ? '允许列表为空时，所有目标都会被拒绝。匹配目标仍需满足上方互联网 / 私网 / 回环权限。' : cfg.relayACL.accessMode === 'deny' ? '拒绝列表匹配项会被拦截；其余目标仍需满足上方权限。' : '域名和 IP 列表暂不参与筛选，保留内容便于下次启用。上方网络权限仍然有效。';
    const serverExitEnabled = !!cfg.serverExit.enabled;
    const serverExitProxy = cfg.serverExit.upstreamMode && cfg.serverExit.upstreamMode !== 'direct';
    ['server-exit-internet','server-exit-private','server-exit-loopback','server-exit-upstream-mode','server-exit-access-mode','server-exit-domains','server-exit-cidrs'].forEach(id => { $(id).disabled = !serverExitEnabled; });
    ['server-exit-upstream-address','server-exit-upstream-username','server-exit-upstream-password'].forEach(id => { $(id).disabled = !serverExitEnabled || !serverExitProxy; });
    $('server-exit-hint').textContent = !serverExitEnabled
      ? '当前运行时不提供 server 出口。启用并保存后必须重启 relay-server。'
      : serverExitProxy
        ? '保存后重启生效。客户端将出口 ID 设置为 server；流量由 Server 经 ' + String(cfg.serverExit.upstreamMode).toUpperCase() + ' 上游访问目标。'
        : '保存后重启生效。客户端将出口 ID 设置为 server；流量直接使用 relay-server 所在主机的网络出口。';
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
  function renderDeviceRDPTargets(device, grantedIDs) {
    const granted = new Set(grantedIDs || []);
    const candidates = state.devices.filter(item => item.id !== device.id && item.approvalState === 'approved' && item.ownerUserId === device.ownerUserId && (item.approvedCapabilities || []).includes('rdp.host'));
    $('device-rdp-target-count').textContent = granted.size + ' / ' + candidates.length + ' 台已授权';
    $('device-rdp-targets').innerHTML = candidates.length ? candidates.map(item => {
      const checked = granted.has(item.id) ? ' checked' : '';
      const availability = item.status === 'online' ? '在线' : '离线';
      return '<label class="capability-option"><input type="checkbox" data-rdp-target value="' + esc(item.id) + '"' + checked + '><span><strong>' + esc(item.name || item.id) + '</strong><small>' + esc(item.id) + ' · ' + availability + '</small></span></label>';
    }).join('') : '<p class="field-hint">当前没有可供迁移参考的历史 RDP 关系。</p>';
  }

  async function loadDeviceRDPTargets(device) {
    const editor = $('device-rdp-access-editor');
    const eligible = !!state.user && state.user.role === 'admin' && !device.identityId &&
      device.approvalState === 'approved' && (device.approvedCapabilities || []).includes('rdp.controller');
    editor.hidden = !eligible;
    if (!eligible) {
      $('device-rdp-targets').innerHTML = '';
      $('device-rdp-target-count').textContent = '0 台已授权';
      return;
    }
    $('device-rdp-targets').innerHTML = '<p class="field-hint">正在读取 Server RDP 授权…</p>';
    $('device-rdp-targets-save').disabled = true;
    errorAt('device-rdp-target-error', '');
    try {
      const data = await api('/rdp/targets?controllerId=' + encodeURIComponent(device.id));
      if (!state.selectedDevice || state.selectedDevice.id !== device.id || !$('device-dialog').open) return;
      renderDeviceRDPTargets(device, (data.targets || []).map(item => item.deviceId));
    } catch (err) {
      if (state.selectedDevice && state.selectedDevice.id === device.id) errorAt('device-rdp-target-error', err.message);
    } finally {
      if (state.selectedDevice && state.selectedDevice.id === device.id) $('device-rdp-targets-save').disabled = false;
    }
  }

  async function saveDeviceRDPTargets() {
    if (state.deviceBusy || state.rdpTargetBusy || !state.selectedDevice) return;
    const device = state.selectedDevice;
    const targetDeviceIds = all('#device-rdp-targets [data-rdp-target]:checked').map(input => input.value);
    state.rdpTargetBusy = true;
    all('#device-dialog button').forEach(button => { button.disabled = true; });
    errorAt('device-rdp-target-error', '');
    try {
      const data = await api('/devices/' + encodeURIComponent(device.id) + '/rdp-targets', { method: 'PUT', body: JSON.stringify({ targetDeviceIds }) });
      if (state.selectedDevice && state.selectedDevice.id === device.id) renderDeviceRDPTargets(device, (data.targets || []).map(item => item.deviceId));
      toast('RDP 设备授权已更新；Agent 将在下一次心跳刷新可连接列表');
    } catch (err) {
      errorAt('device-rdp-target-error', err.message);
    } finally {
      state.rdpTargetBusy = false;
      all('#device-dialog button').forEach(button => { button.disabled = false; });
    }
  }

  function openDeviceIdentity(id) {
    const device = state.devices.find(item => item.id === id);
    if (!device || !state.user || state.user.role !== 'admin') return;
    state.selectedDevice = device;
    $('device-identity-title').textContent = device.identityId ? '更改设备身份归属' : '迁移历史设备';
    $('device-identity-summary').textContent = (device.name || device.id) + ' · ' + device.id;
    $('device-identity-select').innerHTML = '<option value="">请选择身份</option>' + state.identities.map(identity => {
      const suffix = identity.status === 'active' ? '' : ' · 已禁用';
      return '<option value="' + esc(identity.id) + '">' + esc(identity.name + suffix) + '</option>';
    }).join('');
    $('device-identity-select').value = device.identityId || '';
    errorAt('device-identity-error', '');
    $('device-identity-dialog').showModal();
  }

  async function saveDeviceIdentity() {
    if (state.identityAssignmentBusy || !state.selectedDevice) return;
    const device = state.selectedDevice;
    const identityId = $('device-identity-select').value;
    const identity = state.identities.find(item => item.id === identityId);
    if (!identity) {
      errorAt('device-identity-error', '请选择设备所属身份。');
      return;
    }
    const unchanged = device.identityId === identityId;
    const impact = unchanged
      ? '再次保存会修复历史 owner 数据，并同步到该身份的独立登录账号。'
      : device.identityId
        ? '更改归属会立即断开设备，并删除以该设备为目标的跨身份授权。'
        : '分配后，该设备只能使用此身份的短 ID 重连。';
    if (!confirm((unchanged ? '重新保存' : '将') + '设备「' + (device.name || device.id) + '」归属到身份「' + identity.name + '」？\n\n' + impact)) return;
    state.identityAssignmentBusy = true;
    all('#device-identity-dialog button, #device-identity-dialog select').forEach(item => { item.disabled = true; });
    errorAt('device-identity-error', '');
    try {
      await api('/devices/' + encodeURIComponent(device.id) + '/identity', {
        method: 'PUT', body: JSON.stringify({ identityId })
      });
      state.selectedDevice = null;
      $('device-identity-dialog').close();
      toast('设备身份归属已保存；请在客户端配置该身份的短 ID');
      await refresh(true);
    } catch (err) {
      errorAt('device-identity-error', err.message);
    } finally {
      state.identityAssignmentBusy = false;
      all('#device-identity-dialog button, #device-identity-dialog select').forEach(item => { item.disabled = false; });
    }
  }

  function openDevice(id) {
    const device = state.devices.find(d => d.id === id);
    if (!device) { return; }
    state.selectedDevice = device;
    const requested = device.requestedCapabilities && device.requestedCapabilities.length ? device.requestedCapabilities : device.approvedCapabilities;
    const approved = device.approvedCapabilities || [];
    $('device-title').textContent = device.name || '未命名设备';
    $('device-details').innerHTML = details([['设备 ID', device.id], ['身份归属', device.identityName || device.identityId || '未分配（旧设备不可连接）'], ['角色', roleNames[device.deviceMode] || device.deviceMode], ['系统', [device.platform, device.arch].filter(Boolean).join(' / ') || '—'], ['客户端版本', device.clientVersion || '—'], ['授权状态', device.approvalState === 'approved' ? '已授权' : '已撤销'], ['当前能力', orderedCapabilities(approved).map(capability => capabilityNames[capability] || capability).join('、') || '无'], ['RDP UDP', device.rdpUdpReady ? 'QUIC Datagram 可用' : '不可用（需 QUIC 隧道）'], ['最近在线', date(device.lastSeenAt)]]);
    $('device-capabilities').innerHTML = capabilityOptionsHTML(requested, approved);
    $('device-capability-editor').hidden = !state.user || device.approvalState !== 'approved';
    $('device-revoke').hidden = device.approvalState !== 'approved' || !state.user;
    $('device-delete').hidden = !state.user;
    errorAt('device-action-error', '');
    errorAt('device-capability-error', '');
    errorAt('device-rdp-target-error', '');
    $('device-rdp-access-editor').hidden = true;
    const canGrant = !!device.identityId && device.approvalState === 'approved' && approved.some(cap => cap === 'proxy.exit' || cap === 'rdp.host');
    $('device-identity-grant-editor').hidden = !canGrant;
    $('device-dialog').showModal();
    loadDeviceRDPTargets(device);
    if (canGrant) {
      resetIdentityGrantForm();
      $('identity-grant-target').value = device.id;
      syncIdentityGrantCapabilities();
      refreshIdentityGrants(device.id).catch(err => errorAt('identity-grant-error', err.message));
    }
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
  async function deleteDevice() {
    if (state.deviceBusy || !state.selectedDevice) { return; }
    const device = state.selectedDevice;
    const label = device.name || device.id;
    if (!confirm('永久删除设备「' + label + '」？\n\n这会立即断开当前连接，并清除该设备的授权、RDP 关系和安装身份。客户端下次可使用身份 ID 重新申请，并等待身份管理员审批。')) { return; }
    state.deviceBusy = true;
    all('#device-dialog button').forEach(button => { button.disabled = true; });
    errorAt('device-action-error', '');
    try {
      await api('/devices/' + encodeURIComponent(device.id), { method: 'DELETE' });
      state.selectedDevice = null;
      $('device-dialog').close();
      toast('设备已删除；可使用身份 ID 重新申请审批');
      await refresh(true);
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
  $('capture-diagnostics').addEventListener('click', captureDiagnostics);
  window.addEventListener('hashchange', navigate);
  ['device-search', 'device-status', 'device-mode'].forEach(id => $(id).addEventListener('input', renderDevices));
  $('devices-body').addEventListener('click', event => {
    const assign = event.target.closest('[data-assign-identity]');
    const manage = event.target.closest('[data-manage]');
    if (assign) openDeviceIdentity(assign.dataset.assignIdentity);
    else if (manage) openDevice(manage.dataset.manage);
  });
  $('enrollments-body').addEventListener('click', event => {
    const manage = event.target.closest('[data-enrollment-manage]');
    const reject = event.target.closest('[data-enrollment-reject]');
    if (manage) openEnrollment(manage.dataset.enrollmentManage);
    if (reject) rejectEnrollment(reject.dataset.enrollmentReject);
  });
  $('exits-grid').addEventListener('click', event => { const button = event.target.closest('[data-copy-exit]'); if (button) copy(button.dataset.copyExit); });
  ['message-search', 'message-kind', 'message-status'].forEach(id => $(id).addEventListener('input', renderMessages));
  $('messages-refresh').addEventListener('click', () => refresh(true));
  $('messages-clear').addEventListener('click', () => clearServerMessages(''));
  $('messages-back-all').addEventListener('click', showAllMessages);
  $('messages-clear-channel').addEventListener('click', () => clearServerMessages(state.messageChannel));
  $('channel-create').addEventListener('click', () => openChannel(''));
  $('channel-form').addEventListener('submit', saveChannel);
  $('channel-delete').addEventListener('click', deleteChannel);
  $('channel-identity').addEventListener('change', () => {
    renderChannelDevices([]);
    renderChannelRules(null);
  });
  $('channel-all-devices').addEventListener('change', updateChannelDeviceState);
  $('channel-id').addEventListener('input', updateChannelDeviceState);
  $('channel-devices').addEventListener('change', updateChannelDeviceState);
  $('channel-verification-add').addEventListener('click', () => addVerificationRule());
  $('channel-route-add').addEventListener('click', () => addRouteRule());
  $('channel-verification-rules').addEventListener('click', event => {
    const button = event.target.closest('[data-verification-remove]');
    if (button) { button.closest('[data-verification-rule]').remove(); renumberChannelRules(); }
  });
  $('channel-route-rules').addEventListener('click', event => {
    const button = event.target.closest('[data-route-remove]');
    if (button) { button.closest('[data-route-rule]').remove(); renumberChannelRules(); }
  });
  $('channel-route-rules').addEventListener('change', event => {
    if (event.target.matches('[data-route-all]')) syncRouteRuleDeviceStates();
  });
  $('channel-copy-url').addEventListener('click', () => {
    const id = $('channel-id').value.trim();
    if (id && relayPushOrigin()) copy(channelPushURL(id));
  });
  $('channel-list').addEventListener('click', event => {
    const messagesButton = event.target.closest('[data-channel-messages]');
    const copyButton = event.target.closest('[data-channel-copy]');
    const editButton = event.target.closest('[data-channel-edit]');
    if (messagesButton) showChannelMessages(messagesButton.dataset.channelMessages);
    if (copyButton) copy(channelPushURL(copyButton.dataset.channelCopy));
    if (editButton) openChannel(editButton.dataset.channelEdit);
  });
  $('messages-body').addEventListener('click', event => {
    const copyButton = event.target.closest('[data-copy-message-code]');
    const deleteButton = event.target.closest('[data-delete-message]');
    if (copyButton) { copy(copyButton.dataset.copyMessageCode); toast('验证码已复制'); }
    if (deleteButton) deleteServerMessage(deleteButton.dataset.deleteMessage);
  });
  $('identity-refresh').addEventListener('click', () => refresh(true));
  $('identity-create-form').addEventListener('submit', createIdentity);
  $('identities-body').addEventListener('click', event => {
    const button = event.target.closest('[data-identity-manage]');
    if (button) openIdentity(button.dataset.identityManage);
  });
  $('identity-save').addEventListener('click', saveIdentity);
  $('identity-grant-form').addEventListener('submit', submitIdentityGrant);
  $('identity-grant-cancel').addEventListener('click', resetIdentityGrantForm);
  $('identity-grant-target').addEventListener('change', () => {
    delete $('identity-grant-error').dataset.serverError;
    errorAt('identity-grant-error', '');
    syncIdentityGrantCapabilities();
  });
  $('identity-grant-grantee').addEventListener('change', () => {
    delete $('identity-grant-error').dataset.serverError;
    errorAt('identity-grant-error', '');
    syncIdentityGrantCapabilities();
  });
  $('identity-grants-body').addEventListener('click', event => {
    const edit = event.target.closest('[data-identity-grant-edit]');
    const remove = event.target.closest('[data-identity-grant-delete]');
    if (edit) { editIdentityGrant(edit.dataset.identityGrantEdit); }
    if (remove) { deleteIdentityGrant(remove.dataset.identityGrantDelete); }
  });
  $('identity-password-reset').addEventListener('click', resetIdentityPassword);
  $('identity-dialog').addEventListener('close', () => {
    state.selectedIdentity = null;
    errorAt('identity-error', '');
    errorAt('identity-password-error', '');
  });
  $('refresh-enrollments').addEventListener('click', () => refresh(true));
  $('copy-admin-url').addEventListener('click', () => copy(managementURL($('admin-listen').value, $('admin-protocol').value === 'true')));
  $('device-revoke').addEventListener('click', revokeDevice);
  $('device-delete').addEventListener('click', deleteDevice);
  $('device-identity-save').addEventListener('click', saveDeviceIdentity);
  $('device-identity-dialog').addEventListener('cancel', event => { if (state.identityAssignmentBusy) event.preventDefault(); });
  $('device-identity-dialog').addEventListener('close', () => {
    if (!state.identityAssignmentBusy) {
      state.selectedDevice = null;
      errorAt('device-identity-error', '');
    }
  });
  $('device-capabilities-save').addEventListener('click', saveDeviceCapabilities);
  $('device-rdp-targets-save').addEventListener('click', saveDeviceRDPTargets);
  $('enrollment-approve').addEventListener('click', approveEnrollment);
  $('enrollment-reject').addEventListener('click', () => { if (state.selectedEnrollment) rejectEnrollment(state.selectedEnrollment.id); });
  resetIdentityGrantForm();
  resetServerExitGrantForm();
  bindCapabilityDependencies('device-capabilities');
  bindCapabilityDependencies('enrollment-capabilities');
  $('device-dialog').addEventListener('cancel', event => { if (state.deviceBusy || state.rdpTargetBusy) event.preventDefault(); });
  $('server-exit-grant-save').addEventListener('click', saveServerExitGrant);
  $('server-exit-grant-cancel').addEventListener('click', resetServerExitGrantForm);
  $('server-exit-grants-body').addEventListener('click', event => {
    const edit = event.target.closest('[data-server-exit-grant-edit]');
    const remove = event.target.closest('[data-server-exit-grant-delete]');
    if (edit) { editServerExitGrant(edit.dataset.serverExitGrantEdit); }
    if (remove) { deleteServerExitGrant(remove.dataset.serverExitGrantDelete); }
  });
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
