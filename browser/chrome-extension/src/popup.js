import { parseSiteOrigin, getSettings, updateSettings } from './rules.js';

const $ = id => document.getElementById(id);
const status = text => { $('message').textContent = text; $('message').className = ''; };
const failure = error => { $('message').textContent = String(error.message || error); $('message').className = 'error'; };

function renderSites(sites) {
  const list = $('sites');
  list.replaceChildren();
  const siteSelect = $('pair-site');
  const prior = siteSelect.value;
  siteSelect.replaceChildren();
  for (const origin of sites) {
    const li = document.createElement('li');
    li.textContent = origin;
    list.append(li);
    const option = document.createElement('option');
    option.value = origin;
    option.textContent = origin;
    siteSelect.append(option);
  }
  if (sites.includes(prior)) siteSelect.value = prior;
}

async function load() {
  const result = await chrome.runtime.sendMessage({ type: 'GET_STATUS' });
  if (!result?.ok) throw new Error(result?.error || 'Extension background unavailable');
  $('device').textContent = result.identity.deviceId;
  $('extension-id').textContent = chrome.runtime.id;
  $('server').value = result.settings.serverOrigin;
  renderSites(result.settings.sites);
}

$('save').addEventListener('click', async () => {
  try {
    const raw = $('server').value.trim();
    const origin = new URL(raw);
    if (origin.protocol !== 'https:' || origin.pathname !== '/' ||
        origin.search || origin.hash || origin.username || origin.password) {
      throw new Error('仅支持 HTTPS Server Origin');
    }
    // Explicit click grants the extension access to the Admin HTTPS origin.
    const pattern = `${origin.protocol}//${origin.hostname}/*`; // Chrome host permission patterns omit ports
    const granted = await chrome.permissions.request({ origins: [pattern] });
    if (!granted) { status('未授予 Server 访问权限。'); return; }
    const response = await chrome.runtime.sendMessage({
      type: 'SAVE_SERVER', serverOrigin: raw
    });
    if (!response?.ok) throw new Error(response?.error || 'Unable to save address');
    status('Server 地址已保存，可以注册浏览器设备。');
  } catch (error) { failure(error); }
});

$('add-site').addEventListener('click', async () => {
  try {
    const origin = parseSiteOrigin($('site').value.trim());
    // Call request() directly from a user click; never grant silently.
    const websiteURL = new URL(origin);
    const pattern = `${websiteURL.protocol}//${websiteURL.hostname}/*`;
    const granted = await chrome.permissions.request({ origins: [pattern] });
    if (!granted) return status('站点权限未获授予。');
    const settings = await getSettings();
    if (!settings.sites.includes(origin)) {
      settings.sites.push(origin);
      await updateSettings({ sites: settings.sites });
    }
    renderSites(settings.sites);
    status('站点权限已授予。尚未启用会话传输。');
  } catch (error) { failure(error); }
});

$('register').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'REGISTER_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '设备注册失败');
    status(result.state === 'pending'
      ? '已注册，等待管理员在 Server 后台审批。'
      : '浏览器设备状态：' + result.state);
  } catch (error) { failure(error); }
});

$('connect').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'CONNECT_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '连接失败');
    status('已完成设备身份认证，刷新设备列表进行配对。尚未开启 Cookie 同步。');
    await refreshPairing();
  } catch (error) { failure(error); }
});


async function ask(message) {
  const result = await chrome.runtime.sendMessage(message);
  if (!result?.ok) throw new Error(result?.error || '浏览器配对请求失败');
  return result;
}

let cachedRules = [];

async function refreshPairing() {
  const peers = (await ask({ type: 'LIST_PEERS' })).peers || [];
  const select = $('target');
  const previous = select.value;
  select.replaceChildren();
  const placeholder = document.createElement('option');
  placeholder.value = '';
  placeholder.textContent = '请选择已审批的接收设备';
  select.append(placeholder);
  for (const peer of peers.filter(p => p.receive)) {
    const option = document.createElement('option');
    option.value = peer.id;
    option.textContent = (peer.name || 'Chrome') + ' · ' + peer.id.slice(0, 18);
    select.append(option);
  }
  if (peers.some(p => p.id === previous && p.receive)) select.value = previous;
  cachedRules = (await ask({ type: 'LIST_RULES' })).rules || [];
  renderRules(cachedRules);
}

function textNode(tag, content, className) {
  const node = document.createElement(tag);
  node.textContent = content;
  if (className) node.className = className;
  return node;
}

function renderRules(rules) {
  const parent = $('pairing-rules');
  parent.replaceChildren();
  if (!rules.length) {
    parent.append(textNode('p', '暂无配对邀请。'));
    return;
  }
  for (const rule of rules) {
    const card = document.createElement('div');
    card.className = 'rule';
    const desc = rule.role === 'source' ? '来源 A' : '接收 B';
    card.append(textNode('strong', desc + ' · ' + rule.status));
    card.append(textNode('p', '对端：' + rule.remoteID));
    if (rule.lastTransfer && ['SENDING','RELAYED','RECEIVED','APPLIED','FAILED','CONFLICT','UNKNOWN','PARTIAL'].includes(rule.lastTransfer.state)) {
      const translated = {SENDING:'正在发送密文',RELAYED:'密文已转发',RECEIVED:'接收端已收到',
        APPLIED:'Cookie 已应用（网站登录未验证）',FAILED:'同步失败',
        CONFLICT:'目标已有不同登录状态',UNKNOWN:'结果尚不确定，可重试或等待对账',
        PARTIAL:'Cookie 恢复不完整，已暂停接收，必须人工检查'};
      card.append(textNode('p', '最近同步：' + translated[rule.lastTransfer.state]));
    }
    if (!rule.valid) {
      card.append(textNode('p', rule.error || '尚未找到经核验的设备密钥，无法授权。', 'error'));
    } else {
      card.append(textNode('p', '站点：' + rule.policy.siteOrigin));
      card.append(textNode('p', 'Cookie 名称：' + rule.policy.cookieNames.join(', ')));
      card.append(textNode('div', '配对校验码：' + rule.code, 'code'));
      if (rule.status === 'offered' &&
          ((rule.role === 'target' && !rule.targetApproved) ||
           (rule.role === 'source' && rule.targetApproved))) {
        const label = document.createElement('label');
        const checked = document.createElement('input');
        checked.type = 'checkbox';
        label.append(checked, textNode('span', '我已通过独立渠道与另一台设备核对上述校验码。'));
        card.append(label);
        const button = document.createElement('button');
        button.textContent = rule.role === 'target' ? '接受邀请' : '最终确认并激活';
        button.disabled = true;
        checked.addEventListener('change', () => { button.disabled = !checked.checked; });
        button.addEventListener('click', async () => {
          try {
            if (rule.role === 'target') {
              // Chrome requires permissions.request to be called directly
              // within a user gesture before any asynchronous operation.
              const website = new URL(rule.policy.siteOrigin);
              const pattern = website.protocol + '//' + website.hostname + '/*';
              const granted = await chrome.permissions.request({ origins: [pattern] });
              if (!granted) { status('未授权目标站点。'); return; }
              await ask({ type: 'ACCEPT_RULE', ruleId: rule.id, confirmed: true, pairingCode: rule.code });
            } else {
              await ask({ type: 'CONFIRM_RULE', ruleId: rule.id, confirmed: true, pairingCode: rule.code });
            }
            status('配对授权已更新。只有状态为 active 的规则才可同步指定 Cookie。');
            await refreshPairing();
          } catch (error) { failure(error); }
        });
        card.append(button);
      }
    }
    if (rule.status === 'active' && rule.valid) {
      const sync = document.createElement('button');
      sync.textContent = rule.role === 'source' ? '立即发送加密登录状态' : '从来源设备请求同步';
      sync.addEventListener('click', async () => {
        try {
          const action = rule.role === 'source' ? 'SEND_SESSION' : 'REQUEST_SESSION';
          const result = await ask({ type: action, ruleId: rule.id });
          status(result.state === 'RELAYED' ? '密文已投递，等待接收端验证和应用。' :
            '已请求来源设备发送最新密文快照，请刷新检查结果。');
        } catch (error) { failure(error); }
      });
      card.append(sync);
      if (rule.role === 'target') {
        if (rule.lastTransfer?.state === 'PARTIAL') {
          card.append(textNode('p','注意：上次部分 Cookie 无法完整恢复。请检查当前目标网站的登录状态，确认没有混合账户后再恢复。','error'));
          const resume = document.createElement('button');
          resume.textContent = '我已检查登录状态，恢复此规则';
          resume.addEventListener('click',async()=>{
            if(!window.confirm('你是否已经检查目标网站的登录状态，并确认可以继续接收会话？'))return;
            try {
              await ask({type:'RESUME_PARTIAL_RESTORE',ruleId:rule.id,confirmed:true});
              status('已解除恢复暂停。请检查目标登录账号后手动请求同步。');
              await refreshPairing();
            }catch(error){failure(error);}
          });
          card.append(resume);
        }
        const open = document.createElement('button');
        open.textContent = '同步成功后打开网站';
        open.addEventListener('click',async()=>{
          try{
            await ask({type:'SYNC_THEN_OPEN',ruleId:rule.id});
            status('已请求同步，只有 Cookie 成功应用后才会打开网站。');
          }catch(error){failure(error);}
        });
        card.append(open);
        const label = document.createElement('label');
        const checked = document.createElement('input');
        checked.type = 'checkbox';
        label.append(checked, textNode('span', '明确允许此规则覆盖接收端同名 Cookie（可能切换账号）'));
        checked.addEventListener('change', async () => {
          if (checked.checked && !window.confirm('确定允许替换目标网站的现有登录会话吗？')) {
            checked.checked = false; return;
          }
          try {
            await ask({ type: 'ALLOW_OVERWRITE', ruleId: rule.id, allowed: checked.checked });
            status(checked.checked ? '此规则允许覆盖。可立即从来源设备请求同步。' : '已恢复冲突保护。');
          } catch (error) { checked.checked = !checked.checked; failure(error); }
        });
        card.append(label);
      }
    }
    const revoke = document.createElement('button');
    revoke.textContent = '撤销配对';
    revoke.addEventListener('click', async () => {
      try {
        if (!window.confirm('确定撤销此配对规则？此操作不会退出网站已登录的设备。')) return;
        await ask({ type: 'REVOKE_RULE', ruleId: rule.id });
        status('规则已撤销，不再允许后续同步。');
        await refreshPairing();
      } catch (error) { failure(error); }
    });
    card.append(revoke);
    parent.append(card);
  }
}

$('refresh-pairing').addEventListener('click', async () => {
  try { await refreshPairing(); status('已更新配对设备和邀请。'); }
  catch (error) { failure(error); }
});

$('offer-rule').addEventListener('click', async () => {
  try {
    const siteOrigin = $('pair-site').value;
    const targetID = $('target').value;
    const cookieNames = $('cookie-names').value.split(',').map(v => v.trim()).filter(Boolean);
    if (!targetID || !siteOrigin) throw new Error('请先选择网站与接收设备');
    const result = await ask({ type: 'CREATE_RULE', siteOrigin, targetID, cookieNames });
    status('已创建加密邀请 ' + result.ruleId.slice(0, 8) + '，请双方核对配对校验码。');
    await refreshPairing();
  } catch (error) { failure(error); }
});

load().catch(failure);

