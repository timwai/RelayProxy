// Opt-in Browser Sync network binding. The receiver only needs Chrome.
// The proxy is configured for exact first-party HTTPS hosts; no DIRECT
// fallback is ever returned for a bound hostname.
import {sendControl} from './server-api.js';
import {getSettings} from './rules.js';

const DESIRED='browserSyncNetworkDesired';
const CREDS='browserSyncNetworkProxyCredentials';
// Session-only server pairing validation, NOT local proxy authentication.
const LOCAL_READY='browserSyncLocalAgentValidated';
const LEGACY_LOCAL_CREDS='browserSyncLocalAgentCredentials';
const BLACKHOLE='PROXY 127.0.0.1:9';
let refreshing=null;

export function safeHostname(host){
  return typeof host==='string' && host.length>=4 && host.length<=253 &&
    host===host.toLowerCase() &&
    /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$/.test(host) &&
    !['local','internal','localhost','arpa'].some(t=>host.endsWith('.'+t)) &&
    !host.split('.').every(part=>/^[0-9]+$/.test(part)) &&
    !host.split('.').some(t=>t.length>63);
}
export function parseBoundHost(origin){
  const url=new URL(origin);
  if(url.protocol!=='https:'||url.port!==''||url.pathname!=='/'||
     url.search||url.hash||url.username||url.password||!safeHostname(url.hostname))
    throw new Error('网络绑定仅支持 HTTPS 443 精确站点域名');
  return url.hostname;
}
export function proxyEndpoint(host,port){
  if(!safeHostname(host)||!Number.isInteger(port)||port<1||port>65535)
    throw new Error('服务器代理入口无效');
  return 'HTTPS '+host+':'+port;
}
// Each paired site selects its own egress. A local Agent entry is still
// Chromium CONNECT at the Chrome boundary but uses Agent-native routing,
// not Agent's general-purpose SOCKS/HTTP services.
export function buildPAC(desired,endpoint=null,local=null){
  const map={};
  const target=endpoint||BLACKHOLE;
  if(!/^(HTTPS [a-z0-9.-]+:[0-9]{1,5}|PROXY 127\.0\.0\.1:9)$/.test(target))
    throw new Error('非法代理规则');
  for(const entry of Object.values(desired||{})){
    const host=parseBoundHost(entry.siteOrigin);
    if(entry.mode && !['source_agent','local_agent'].includes(entry.mode))
      throw new Error('不支持的网络出口模式');
    const route=entry.mode==='local_agent'?
      (local && local.port===entry.port?'PROXY 127.0.0.1:'+entry.port:BLACKHOLE):target;
    if(map[host] && map[host]!==route)
      throw new Error('同一域名不能绑定到不同 Agent 出口');
    map[host]=route;
  }
  return 'function FindProxyForURL(url, host) {\n'+
    '  var sites='+JSON.stringify(map)+';\n'+
    '  var key=String(host).toLowerCase();\n'+
    '  return Object.prototype.hasOwnProperty.call(sites,key) ? sites[key] : "DIRECT";\n}';
}
export async function desiredNetwork(){
  return (await chrome.storage.local.get(DESIRED))[DESIRED]||{};
}
async function currentLease(){
  return (await chrome.storage.session.get(CREDS))[CREDS]||null;
}
async function localValidated(){
  return (await chrome.storage.session.get(LOCAL_READY))[LOCAL_READY]||null;
}
function checkLocalPort(port){
  if(!Number.isInteger(port)||port<1024||port>65535)
    throw new Error('本机 Agent 端口无效');
  return port;
}
export async function selectedNetworkRules(){return desiredNetwork();}
async function applyPAC(desired,lease=null){
  if(!Object.keys(desired).length){
    await chrome.storage.session.remove([CREDS,LOCAL_READY,LEGACY_LOCAL_CREDS]);
    await chrome.proxy.settings.clear({scope:'regular'});
    return;
  }
  const target=lease?proxyEndpoint(lease.host,lease.port):null;
  const local=await localValidated();
  await chrome.proxy.settings.set({scope:'regular',value:{
    mode:'pac_script',pacScript:{data:buildPAC(desired,target,local),mandatory:true}
  }});
  const status=await chrome.proxy.settings.get({incognito:false});
  if(status.levelOfControl!=='controlled_by_this_extension')
    throw new Error('另一个扩展或浏览器策略控制了 Chrome 代理');
}
export async function installNetworkGuard(){
  const desired=await desiredNetwork();
  await chrome.storage.session.remove(LEGACY_LOCAL_CREDS);
  if(Object.keys(desired).length)await applyPAC(desired);
}
export async function stopNetworkGuard(ruleId){
  const desired=await desiredNetwork();
  delete desired[ruleId];
  await chrome.storage.local.set({[DESIRED]:desired});
  if(!Object.values(desired).some(x=>x.mode==='local_agent'))
    await chrome.storage.session.remove(LOCAL_READY);
  await applyPAC(desired);
  if(Object.keys(desired).length)await refreshNetworkBinding({force:true}).catch(()=>{});
}
export async function startNetworkGuard(ruleId,siteOrigin){
  parseBoundHost(siteOrigin);
  const desired=await desiredNetwork();
  desired[ruleId]={mode:'source_agent',siteOrigin};
  await chrome.storage.local.set({[DESIRED]:desired});
  await applyPAC(desired); // blackhole first: fail closed
  return refreshNetworkBinding({force:true});
}
// The caller must have checked receiver pairing and decrypted Cookie policy.
// Loopback CONNECT needs no credentials. PAC remains blocked until validated.
export async function startLocalNetworkGuard(ruleId,siteOrigin,port=21291){
  parseBoundHost(siteOrigin);
  checkLocalPort(port);
  const desired=await desiredNetwork();
  desired[ruleId]={mode:'local_agent',siteOrigin,port};
  await chrome.storage.local.set({[DESIRED]:desired});
  // Guard first. If connection or permission checks fail, bound host must
  // not fall through to DIRECT even after Chrome service worker restart.
  await chrome.storage.session.remove([LOCAL_READY,LEGACY_LOCAL_CREDS]);
  await applyPAC(desired);
  return refreshNetworkBinding({force:true});
}
// Called BEFORE applying Cookie snapshots or issuing a sync request.
export async function ensureNetworkForRule(ruleId,siteOrigin){
  const desired=await desiredNetwork();
  if(desired[ruleId]?.mode==='local_agent'){
    if(desired[ruleId].siteOrigin!==siteOrigin)throw new Error('本机代理域名与加密 Cookie 规则不一致');
    await refreshNetworkBinding({force:true});
    const current=await localValidated();
    if(!current || current.port!==desired[ruleId].port)
      throw new Error('本机 Agent 配对状态未验证，网站访问保持阻断');
    await verifyProxyOwnership();
    return;
  }
  const result=await sendControl('NETWORK_BINDING',{ruleId});
  const binding=result?.binding;
  if(!binding || binding.status!=='active'){
    if(desired[ruleId])throw new Error('来源网络绑定已撤销，网站仍保持阻断');
    return;
  }
  if(binding.host!==parseBoundHost(siteOrigin))
    throw new Error('Agent 网络绑定与加密 Cookie 策略的域名不一致');
  if(desired[ruleId]?.siteOrigin!==siteOrigin)
    throw new Error('未在本机明确授权代理网络，已拒绝此 Cookie 同步');
  await refreshNetworkBinding();
  const lease=await currentLease();
  if(!lease || lease.expiresAt<=Date.now()+30000)
    throw new Error('来源 Agent 代理不可用，已阻断绑定网站访问');
  await verifyProxyOwnership();
}
async function verifyProxyOwnership(){
  const effective=await chrome.proxy.settings.get({incognito:false});
  if(effective.levelOfControl!=='controlled_by_this_extension' ||
     effective.value?.mode!=='pac_script')
    throw new Error('Chrome 代理设置已被其他扩展或系统策略覆盖，拒绝 Cookie 同步');
}
export function refreshNetworkBinding({force=false}={}){
  if(refreshing)return refreshing;
  const job=async()=>{
    const desired=await desiredNetwork();
    if(!Object.keys(desired).length)return {state:'OFF'};
    const settings=await getSettings();
    const localRules=Object.entries(desired).filter(([,v])=>v.mode==='local_agent');
    const sourceRules=Object.entries(desired).filter(([,v])=>v.mode!=='local_agent');
    if(localRules.length){
      const identity=(await import('./device-identity.js')).getOrCreateIdentity;
      const me=await identity();
      const serverRules=(await sendControl('LIST_RULES')).rules||[];
      for(const [ruleId,item] of localRules){
        if(!settings.sites.includes(item.siteOrigin))throw new Error('本机代理站点权限已撤销');
        parseBoundHost(item.siteOrigin);checkLocalPort(item.port);
        if(!serverRules.some(r=>r.ruleId===ruleId && r.status==='active' &&
          r.targetBrowserDeviceId===me.deviceId && r.keysConfirmed===true))
          throw new Error('本机 Agent 绑定的 Cookie 规则不再有效');
      }
      const port=localRules[0][1].port;
      if(!localRules.every(([,r])=>r.port===port))
        throw new Error('当前 Chrome Profile 的本机 Agent 端口必须一致');
      await chrome.storage.session.set({[LOCAL_READY]:{port}});
    }
    if(!localRules.length)await chrome.storage.session.remove(LOCAL_READY);
    for(const [ruleId,item] of sourceRules){
      if(!settings.sites.includes(item.siteOrigin))throw new Error('站点授权已失效');
      const answer=await sendControl('NETWORK_BINDING',{ruleId});
      if(answer?.binding?.status!=='active' ||
        answer.binding.host!==parseBoundHost(item.siteOrigin))
        throw new Error('来源网络绑定已撤销或站点变更');
    }
    let lease=await currentLease();
    if(sourceRules.length && (force || !lease || lease.expiresAt<=Date.now()+5*60*1000)){
      const result=await sendControl('NETWORK_TOKEN');
      if(result.type!=='NETWORK_TOKEN'||typeof result.token!=='string'||
        result.token.length<32||!Number.isSafeInteger(result.expiresAt)||
        result.expiresAt<Date.now()+60000)throw new Error('无效的来源 Agent 网络租约');
      proxyEndpoint(result.host,result.port);
      lease={username:'browser',password:result.token,
        host:result.host,port:result.port,expiresAt:result.expiresAt};
      await chrome.storage.session.set({[CREDS]:lease});
    }
    await applyPAC(desired,sourceRules.length?lease:null);
    await verifyProxyOwnership();
    return {state:'ACTIVE',expiresAt:lease?.expiresAt||null};
  };
  refreshing=job().catch(async error=>{
    const desired=await desiredNetwork();
    if(Object.keys(desired).length){
      await chrome.storage.session.remove([CREDS,LOCAL_READY,LEGACY_LOCAL_CREDS]);
      await applyPAC(desired).catch(()=>{});
    }
    throw error;
  }).finally(()=>{refreshing=null});
  return refreshing;
}
// Do not interfere with a website's normal HTTP 401 / WWW-Authenticate
// flow or another extension's proxy. Cancel only a challenge from our known
// RelayProxy endpoint when its receiver-scoped lease has expired.
export function proxyAuthDecision(details,lease,now=Date.now()){
  if(!details?.isProxy || details.scheme?.toLowerCase()!=='basic')return {};
  // Only the remote Relay Server authenticates. The B-local Agent loopback
  // CONNECT endpoint intentionally sends no 407 and receives no credentials.
  if(String(details.challenger?.host).toLowerCase()==='127.0.0.1')return {};
  if(!lease || String(details.challenger?.host).toLowerCase()!==lease.host ||
    Number(details.challenger?.port)!==lease.port)return {};
  if(!Number.isSafeInteger(lease.expiresAt) || now>=lease.expiresAt)
    return {cancel:true};
  return {authCredentials:{username:lease.username,password:lease.password}};
}
export function registerNetworkAuth(){
  chrome.webRequest?.onAuthRequired?.addListener((details,respond)=>{
    (async()=>{
      if(!details.isProxy || details.scheme?.toLowerCase()!=='basic')return {};
      const lease=await currentLease();
      return proxyAuthDecision(details,lease);
    })().then(respond,()=>respond({}));
  },{urls:['<all_urls>']},['asyncBlocking']);
}
