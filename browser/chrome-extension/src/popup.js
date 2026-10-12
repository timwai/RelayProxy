// Popup is an operational dashboard only. Permission prompts, site grants,
// pairing confirmation and account-overwrite policy live in options.html.
const $ = id => document.getElementById(id);
const feedback = (message, error = false) => {
  const node = $('message');
  node.hidden = false;
  node.textContent = message;
  node.className = 'feedback' + (error ? ' feedback-error' : '');
};
const messageError = error => feedback(String(error?.message || error), true);

const STATES = {
  APPLIED: '已应用 · 登录待验证', RELAYED: '密文已转发', RECEIVED: '等待应用',
  SENDING: '正在加密发送', FAILED: '同步失败', CONFLICT: '目标状态冲突',
  UNKNOWN: '结果待确认', PARTIAL: '恢复已暂停'
};

async function ask(type, fields = {}) {
  const result = await chrome.runtime.sendMessage({type, ...fields});
  if (!result?.ok) throw new Error(result?.error || '无法连接浏览器后台');
  return result;
}
const optionPage = () => chrome.runtime.openOptionsPage().catch(messageError);
$('open-options').addEventListener('click', optionPage);
$('footer-options').addEventListener('click', optionPage);
$('empty-options').addEventListener('click', optionPage);

function el(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}
function activeSite(tab) {
  try {
    const uri = new URL(tab?.url);
    return ['http:', 'https:'].includes(uri.protocol) ? uri.origin : '';
  } catch { return ''; }
}
function setConnection(connected, configured, stage) {
  $('connection-dot').className = 'status-dot ' + (connected ? 'online' : 'offline');
  $('connection-text').textContent = connected ? '已连接' : configured ? '尚未连接' : '尚未配置';
  $('connect').textContent = connected ? '已连接' : configured ? '立即连接' : '去配置';
  $('connect').disabled = connected;
  $('connect').dataset.configured = String(configured);
  $('connect').title = connected ? '已通过设备身份认证' : (stage || '等待连接');
}

function renderRules(rules, siteOrigin) {
  const list = $('quick-rules');
  list.replaceChildren();
  const active = rules.filter(r => r.status === 'active' && r.valid && r.policy?.siteOrigin);
  const siteRules = active.filter(r => r.policy.siteOrigin === siteOrigin);
  const ordered = [...siteRules, ...active.filter(r => r.policy.siteOrigin !== siteOrigin)];
  $('rule-count').textContent = active.length + ' 个已激活配对';
  $('current-site').hidden = !siteOrigin;
  if (siteOrigin) $('current-site').textContent = '当前标签页 · ' + new URL(siteOrigin).hostname + (siteOrigin.startsWith('http://') ? ' · HTTP 不安全' : '');
  $('empty-state').hidden = ordered.length > 0;
  if (!ordered.length) {
    $('empty-title').textContent = rules.some(r => r.status === 'offered') ? '有等待确认的配对邀请' : '尚无活跃会话';
    $('empty-description').textContent = '前往完整设置管理网站授权、配对邀请和账号冲突。';
  }
  for (const rule of ordered) {
    const item = el('article','quick-rule');
    if (siteOrigin && rule.policy.siteOrigin === siteOrigin) item.classList.add('matching-rule');
    const info = el('div','rule-info');
    const main = el('div','rule-name');
    main.append(el('strong','',new URL(rule.policy.siteOrigin).hostname + (rule.policy.siteOrigin.startsWith('http://') ? ' · HTTP' : '')),
      el('span','role-label',rule.role === 'source' ? '发送端' : '接收端'));
    const detail = el('div','rule-detail',STATES[rule.lastTransfer?.state] || '等待同步');
    if (['PARTIAL','CONFLICT','FAILED'].includes(rule.lastTransfer?.state)) detail.classList.add('attention');
    info.append(main,detail);
    const buttons = el('div','quick-actions');
    const sync = el('button','small-button primary-button',rule.role === 'source' ? '立即发送' : '请求同步');
    sync.type = 'button';
    const paused = rule.role === 'target' && rule.lastTransfer?.state === 'PARTIAL';
    sync.disabled = paused;
    if (paused) sync.title = '请先在完整设置中检查并恢复暂停的会话';
    sync.addEventListener('click',async()=>{
      sync.disabled = true;
      try {
        const result = await ask(rule.role === 'source' ? 'SEND_SESSION' : 'REQUEST_SESSION',{ruleId:rule.id});
        feedback(result.state === 'RELAYED' ? '密文已发送；等待接收端确认。' : '已请求来源设备发送最新会话。');
        await refresh({quiet:true});
      }catch(error){messageError(error);}
      finally{sync.disabled = false;}
    });
    buttons.append(sync);
    if (rule.role === 'target') {
      const open = el('button','small-button outline-button','同步后打开');
      open.type = 'button';
      open.disabled = paused;
      if (paused) open.title = '请先在完整设置中检查并恢复暂停的会话';
      open.addEventListener('click',async()=>{
        open.disabled = true;
        try {await ask('SYNC_THEN_OPEN',{ruleId:rule.id});feedback('已请求同步，成功应用后将打开对应的恢复标签页。');}
        catch(error){messageError(error);}
        finally{open.disabled = false;}
      });
      buttons.append(open);
    }
    if (rule.lastTransfer?.state === 'PARTIAL' || rule.lastTransfer?.state === 'CONFLICT') {
      const hint = el('button','text-button rule-manage','在设置中处理 →');
      hint.type = 'button';hint.addEventListener('click',optionPage);
      item.append(info,buttons,hint);
    } else item.append(info,buttons);
    list.append(item);
  }
}
let localPort = 21291;
let refreshing = false;
async function refresh({quiet = false} = {}) {
  if (refreshing) return;
  refreshing = true;
  $('refresh').disabled = true;
  try {
    const result = await ask('GET_STATUS');
    localPort = result.localAgentProxy?.port || 21291;
    $('quick-proxy').checked = !!result.localAgentProxy?.enabled;
    $('proxy-port').textContent = '本机端口 · ' + localPort;
    const configured = !!result.settings.serverOrigin;
    $('server-summary').textContent = result.settings.serverOrigin || '请先在设置中添加 HTTPS Server';
    setConnection(!!result.connected,configured,result.stage);
    if(configured&&!result.connected&&result.nextRetryAt>Date.now()){
      const seconds=Math.max(1,Math.ceil((result.nextRetryAt-Date.now())/1000));
      $('connection-text').textContent='等待自动重连';
      $('connect').title='自动重试约 '+seconds+' 秒后；也可以手动连接';
    }
    if (result.connected) {
      const [ruleReply, tabs] = await Promise.all([
        ask('LIST_RULES'),chrome.tabs.query({active:true,currentWindow:true})
      ]);
      renderRules(ruleReply.rules || [],activeSite(tabs[0]));
    } else {
      $('current-site').hidden = true;
      $('quick-rules').replaceChildren();
      $('rule-count').textContent = configured ? '连接后查看活跃配对' : '等待初始配置';
      $('empty-state').hidden = false;
      $('empty-title').textContent = configured ? '连接以查看配对' : '尚未配置 Server';
      $('empty-description').textContent = configured ? '已保存 Server 地址，连接后即可快捷同步。' : '先在设置页面配置 Server 和站点权限。';
    }
    if (!quiet) $('message').hidden = true;
  } catch(error) {messageError(error);}
  finally{refreshing = false;$('refresh').disabled = false;}
}
$('refresh').addEventListener('click',()=>refresh());
$('connect').addEventListener('click',async()=>{
  if ($('connect').dataset.configured !== 'true') return optionPage();
  $('connect').disabled = true;
  try {await ask('CONNECT_BROWSER');feedback('浏览器设备已通过 Server 身份认证。');await refresh({quiet:true});}
  catch(error){messageError(error);}
  finally{$('connect').disabled = $('connection-text').textContent === '已连接';}
});
$('quick-proxy').addEventListener('change',async()=>{
  const next = $('quick-proxy').checked;
  $('quick-proxy').disabled = true;
  try {
    await ask('SET_LOCAL_AGENT_GLOBAL',{enabled:next,port:localPort});
    feedback(next ? '已为当前 Chrome Profile 启用 Agent 分流。' : '已关闭全局 Agent 代理，网站独立绑定不受影响。');
  }catch(error){messageError(error);await refresh({quiet:true});}
  finally{$('quick-proxy').disabled = false;}
});
refresh();
