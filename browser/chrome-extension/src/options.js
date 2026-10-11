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
    li.append(textNode('span',origin));
    const remove=document.createElement('button');
    remove.textContent='移除授权';remove.title='停止该站点后续同步';
    remove.addEventListener('click',async()=>{
      try { await ask({type:'REMOVE_SITE',siteOrigin:origin});
        const latest=await getSettings();renderSites(latest.sites);
        status('已撤销站点同步授权；原网站登录状态未清除。');
      }catch(error){failure(error);}
    });
    li.append(remove);list.append(li);
    const option = document.createElement('option');
    option.value = origin;
    option.textContent = origin;
    siteSelect.append(option);
  }
  if (sites.includes(prior)) siteSelect.value = prior;
}

async function refreshSiteChoices(){
  const select=$('site'),selected=select.value;
  const tabs=await chrome.tabs.query({});
  const sites=[...new Set(tabs.map(tab=>{
    try {
      const u=new URL(tab.url);
      return ['http:','https:'].includes(u.protocol) ? u.origin : null;
    }catch{return null;}
  }).filter(Boolean))].sort();
  select.replaceChildren();
  const prompt=document.createElement('option');
  prompt.value='';
  prompt.textContent=sites.length?'请选择已打开的网站':'请先在 Chrome 打开 HTTP/HTTPS 网站';
  select.append(prompt);
  for(const site of sites){
    const item=document.createElement('option');
    item.value=site;item.textContent=site.startsWith('http://')?'HTTP（不安全） · '+site:site;
    select.append(item);
  }
  if(sites.includes(selected))select.value=selected;
}
$('refresh-sites').addEventListener('click',()=>refreshSiteChoices().catch(failure));

async function load() {
  const result = await chrome.runtime.sendMessage({ type: 'GET_STATUS' });
  if (!result?.ok) throw new Error(result?.error || 'Extension background unavailable');
  $('device').textContent = result.identity.deviceId;
  $('server-stage').textContent = result.connected ? '设备认证已连接' : result.settings.serverOrigin ? '尚未连接 Server' : '尚未配置 Server';
  $('server').value = result.settings.serverOrigin;
  renderSites(result.settings.sites);
  await refreshSiteChoices();
  $('local-agent-all').checked=!!result.localAgentProxy?.enabled;
  $('local-agent-port').value=result.localAgentProxy?.port||21291;
}

$('local-agent-apply').addEventListener('click',async()=>{
  try{
    const enabled=$('local-agent-all').checked;
    const port=Number($('local-agent-port').value);
    await ask({type:'SET_LOCAL_AGENT_GLOBAL',enabled,port});
    status(enabled?'Chrome 浏览器代理已交给本机 RelayProxy Agent 的原生分流':'已停用本机 Agent 全浏览器代理，保留独立会话网络规则');
  }catch(error){failure(error);}
});

$('save').addEventListener('click', async () => {
  try {
    const raw = $('server').value.trim();
    const origin = new URL(raw);
    if (origin.protocol !== 'https:' || origin.pathname !== '/' ||
        origin.search || origin.hash || origin.username || origin.password) {
      throw new Error('仅支持 HTTPS Server Origin');
    }
    // Explicit click grants the extension access to the selected Relay HTTPS origin.
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
    // An explicit secondary warning prevents accidental HTTP credential
    // disclosure; the site's own traffic cannot be secured by E2EE sync.
    if(origin.startsWith('http://') && !window.confirm(
      '此网站使用 HTTP：网站登录 Cookie 和 Storage 可能在网络中被窃取或篡改。' +
      'RelayProxy 只加密设备间的同步数据，无法加密您与网站之间的 HTTP 连接。仍要授权此站点吗？'))return;
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
    status(origin.startsWith('http://')?'HTTP 站点已授权（网站访问仍为明文）；尚未建立配对。':'站点权限已授予。尚未启用会话传输。');
  } catch (error) { failure(error); }
});

$('register').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'REGISTER_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '设备注册失败');
    status(result.state === 'extension_pending'
      ? '当前 Server 为旧版本，需先批准扩展，再次点击注册设备。升级 Server 后可一次审批。'
      : result.state === 'pending'
      ? '申请已一次提交。请管理员在 Server Web → 浏览器同步 中选择身份及权限，并点击“批准扩展与设备”。'
      : result.state === 'approved'
      ? '浏览器设备及扩展已获批，现在可以连接 Server。'
      : result.state === 'revoked'
      ? '该 Chrome 浏览器设备的旧授权已撤销。请管理员在 Server Web → 浏览器同步删除旧设备（若扩展也已撤销，先清理关联设备，再删除旧扩展），然后重新提交申请。'
      : '浏览器设备状态：' + result.state);
  } catch (error) { failure(error); }
});

$('connect').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'CONNECT_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '连接失败');
    status('已完成设备身份认证，刷新设备列表进行配对。请在两端审批配对后同步。');
    $('server-stage').textContent = '设备认证已连接';
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
    const desc = rule.role === 'source' ? '发送端' : '接收端';
    card.append(textNode('strong', desc + ' · ' + ({active:'已启用',offered:'待确认',revoked:'已撤销'}[rule.status]||rule.status)));
    card.append(textNode('p', '配对设备：' + rule.remoteID));
    if (rule.lastTransfer && ['SENDING','RELAYED','RECEIVED','APPLIED','FAILED','CONFLICT','UNKNOWN','PARTIAL'].includes(rule.lastTransfer.state)) {
      const translated = {SENDING:'正在发送密文',RELAYED:'密文已转发',RECEIVED:'接收端已收到',
        APPLIED:'站点状态已应用（网站登录未验证）',FAILED:'同步失败',
        CONFLICT:'目标已有不同登录状态',UNKNOWN:'结果尚不确定，可重试或等待对账',
        PARTIAL:'浏览器状态恢复不完整，已暂停接收，必须人工检查'};
      card.append(textNode('p', '最近同步：' + translated[rule.lastTransfer.state]));
    }
    if (!rule.valid) {
      card.append(textNode('p', rule.error || '尚未找到经核验的设备密钥，无法授权。', 'error'));
    } else {
      card.append(textNode('p', '站点：' + rule.policy.siteOrigin));
      if(rule.policy.siteOrigin.startsWith('http://'))
        card.append(textNode('p', 'HTTP 风险：网站访问与登录凭据可能明文暴露，设备间同步本身仍为加密传输。', 'error'));
      card.append(textNode('p',Array.isArray(rule.policy.cookieNames) ?
        '旧版指定 Cookie：'+rule.policy.cookieNames.join(', ') :
        (rule.policy.indexedDB===true
          ? '同步范围：全部 Cookie / Local Storage / Session Storage / 同源 IndexedDB（v3；最多 4 MiB IndexedDB）'
          : '同步范围：全部 Cookie / Local Storage / Session Storage（旧 v2 配对不包括 IndexedDB）')));
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
              if(website.protocol==='http:' && !window.confirm(
                '确认接收来自 HTTP 网站的会话？此站点本身并未加密，Cookie 和登录数据可能在访问网站时被窃取。'))return;
              const pattern = website.protocol + '//' + website.hostname + '/*';
              const granted = await chrome.permissions.request({ origins: [pattern] });
              if (!granted) { status('未授权目标站点。'); return; }
              await ask({ type: 'ACCEPT_RULE', ruleId: rule.id, confirmed: true, pairingCode: rule.code });
            } else {
              await ask({ type: 'CONFIRM_RULE', ruleId: rule.id, confirmed: true, pairingCode: rule.code });
            }
            status('配对授权已更新。只允许已激活的配对同步。');
            await refreshPairing();
          } catch (error) { failure(error); }
        });
        card.append(button);
      }
    }
    if(rule.status==='active' && rule.valid && rule.policy.siteOrigin.startsWith('https://')){
      if(rule.network?.status==='offered' && rule.role==='target'){
        card.append(textNode('p','来源设备申请：访问 '+rule.network.host+
          ' 时经由 Agent '+rule.network.exitDeviceId+' 出口。仅 HTTPS 443。'));
        const approve=document.createElement('button');
        approve.textContent='启用来源设备出口';
        approve.disabled=!rule.network.matchingHost;
        approve.addEventListener('click',async()=>{
          if(!window.confirm('此网站将通过来源设备出口访问；出口离线时会阻断连接。确定启用？'))return;
          try{
            await ask({type:'NETWORK_ACCEPT',ruleId:rule.id,confirmed:true});
            status('网络绑定已授权；该网站通过来源设备 Agent 出口访问。');
            await refreshPairing();
          }catch(error){failure(error);}
        });
        card.append(approve);
      }
      if(rule.role==='source' && (!rule.network || rule.network.status==='none')){
        const action=document.createElement('button');
        action.textContent='为此 Cookie 规则绑定 Agent 网络';
        action.title='需要已启用 Browser Sync 的 TLS/TCP 中继、浏览器 send 权限，以及同身份的共享 Agent Exit 在线';
        action.addEventListener('click',async()=>{
          try{
            const exits=(await ask({type:'NETWORK_EXITS'})).exits||[];
            if(!exits.length)throw new Error(
              '当前没有可用 Agent 出口。请检查：① Server 已启用 Browser Sync 和 TLS/TCP 中继；' +
              '② 来源设备 Agent 已开启浏览器网络共享并具有出口权限；' +
              '③ Agent 与来源浏览器属于同一身份。');
            const select=document.createElement('select');
            for(const exit of exits){
              const opt=document.createElement('option');
              opt.value=exit.id;
              opt.textContent=(exit.name||'Agent')+' · '+exit.id;
              select.append(opt);
            }
            const approve=document.createElement('button');
            approve.textContent='发送网络绑定邀请';
            approve.addEventListener('click',async()=>{
              try{
                await ask({type:'NETWORK_OFFER',ruleId:rule.id,exitDeviceId:select.value});
                status('网络绑定已提出，等待 B 端独立确认。');
                await refreshPairing();
              }catch(error){failure(error);}
            });
            card.append(select,approve);
            action.disabled=true;
          }catch(error){failure(error);}
        });
        card.append(action);
      }
      if(rule.network?.status==='active'){
        card.append(textNode('p','网络：'+rule.network.host+' → '+rule.network.exitDeviceId));
        const revoke=document.createElement('button');
        revoke.textContent='撤销网络绑定';
        revoke.addEventListener('click',async()=>{
          if(!window.confirm('撤销后，此网站可能恢复使用本机网络，已同步的网站登录状态不会自动清除。继续？'))return;
          try{
            await ask({type:'NETWORK_REVOKE',ruleId:rule.id});
            status('网络绑定已撤销。');
            await refreshPairing();
          }catch(error){failure(error);}
        });
        card.append(revoke);
      }
    }
    if (rule.status==='active' && rule.valid && rule.role==='target' && rule.policy.siteOrigin.startsWith('https://')){
      const current=rule.localNetworkMode==='local_agent'?'本机 Agent':rule.localNetworkMode==='source_agent'?'来源设备 Agent':'未启用';
      card.append(textNode('p','当前站点网络出口：'+current));
      const local=document.createElement('button');
      local.textContent='改用本机 Agent 代理';
      local.disabled=rule.localNetworkMode==='local_agent';
      local.addEventListener('click',()=>{
        local.disabled=true;
        const help=textNode('p','此网站使用当前设备的 Agent 分流及出口。');
        const port=document.createElement('input');
        port.type='number';port.min='1024';port.max='65535';port.value='21291';
        port.placeholder='本机监听端口';
        port.setAttribute('aria-label','本机 Agent 监听端口');
        const confirm=document.createElement('button');confirm.textContent='确认绑定本机 Agent';
        confirm.addEventListener('click',async()=>{
          if(!window.confirm('此网站将改用当前设备 Agent 出口；匹配 DIRECT 的规则会被阻断以防止泄漏。确定切换？'))return;
          try{
            await ask({type:'NETWORK_LOCAL_ENABLE',ruleId:rule.id,
              port:Number(port.value),confirmed:true});
            status('已切换至本机 Agent。');
            await refreshPairing();
          }catch(error){failure(error);}
        });
        card.append(help,port,confirm);
      });
      card.append(local);
      if(rule.network?.status==='active'&&rule.network.matchingHost&&rule.localNetworkMode!=='source_agent'){
        const useSource=document.createElement('button');
        useSource.textContent='切换到来源设备出口';
        useSource.addEventListener('click',async()=>{
          try{
            await ask({type:'NETWORK_USE_SOURCE',ruleId:rule.id});
            status('已切换到来源设备出口');
            await refreshPairing();
          }catch(error){failure(error);}
        });
        card.append(useSource);
      }
      if(rule.localNetworkMode==='local_agent'){
        const stop=document.createElement('button');
        stop.textContent='停用本机站点代理';
        stop.addEventListener('click',async()=>{
          if(!window.confirm('停用后此网站可能使用本机网络直连；已同步的登录状态不会自动清除。确定？'))return;
          try{
            await ask({type:'NETWORK_LOCAL_DISABLE',ruleId:rule.id});
            await refreshPairing();
          }catch(error){failure(error);}
        });
        card.append(stop);
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
            status('已请求同步，只有本次授权范围内的会话数据成功应用后才会打开网站。');
          }catch(error){failure(error);}
        });
        card.append(open);
        const label = document.createElement('label');
        const checked = document.createElement('input');
        checked.type = 'checkbox';
        label.append(checked, textNode('span', '允许覆盖目标端 Cookie、Local/Session Storage 与 IndexedDB（可能切换账号）'));
        checked.addEventListener('change', async () => {
          if (checked.checked && !window.confirm('确定允许覆盖目标网站的 Cookie、Storage 与 IndexedDB 登录数据吗？覆盖可能切换账号；数据库版本或索引冲突仍会拒绝。')) {
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
    if (!targetID || !siteOrigin) throw new Error('请先选择网站与接收设备');
    if(siteOrigin.startsWith('http://') && !window.confirm(
      '即将为 HTTP 网站创建会话配对。虽然同步数据端到端加密，但站点访问本身仍可能泄露登录凭据。继续？'))return;
    const result = await ask({ type: 'CREATE_RULE', siteOrigin, targetID });
    status('已创建加密邀请 ' + result.ruleId.slice(0, 8) + '，请双方核对配对校验码。');
    await refreshPairing();
  } catch (error) { failure(error); }
});

// Task-first navigation: the current page style stays intact, while only the
// selected panel is displayed. Deep links and the extension popup still work.
export function activateSettingsSection(sectionId){
  const panels=[...document.querySelectorAll('.settings-main > .settings-section')];
  const selected=panels.some(panel=>panel.id===sectionId)?sectionId:'server-section';
  for(const panel of panels)panel.hidden=panel.id!==selected;
  for(const link of document.querySelectorAll('.settings-nav a[href^="#"]')){
    const active=link.getAttribute('href')==='#'+selected;
    link.classList.toggle('active',active);
    if(active)link.setAttribute('aria-current','page');
    else link.removeAttribute('aria-current');
  }
  document.querySelector('.settings-main')?.scrollIntoView?.({block:'start'});
  return selected;
}
document.querySelectorAll('.settings-nav a[href^="#"]').forEach(link=>{
  link.addEventListener('click',event=>{
    event.preventDefault();
    const id=link.getAttribute('href').slice(1);
    if(window.location.hash!=='#'+id)window.location.hash=id;
    else activateSettingsSection(id);
  });
});
window.addEventListener('hashchange',()=>activateSettingsSection(window.location.hash.slice(1)));
activateSettingsSection(window.location.hash.slice(1));

load().then(async () => {
  const response=await chrome.runtime.sendMessage({type:'GET_STATUS'});
  if(response?.connected)await refreshPairing();
}).catch(failure);

