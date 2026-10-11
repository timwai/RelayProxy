// Opt-in Browser Sync network binding. The receiver only needs Chrome.
// The proxy is configured for exact first-party HTTPS hosts; no DIRECT
// fallback is ever returned for a bound hostname.
import {sendControl} from './server-api.js';
import {getSettings} from './rules.js';

const DESIRED='browserSyncNetworkDesired';
const CREDS='browserSyncNetworkProxyCredentials';
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
export function buildPAC(desired,endpoint=null){
  const map={};
  for(const entry of Object.values(desired||{}))map[parseBoundHost(entry.siteOrigin)]=true;
  const target=endpoint||BLACKHOLE;
  if(!/^(HTTPS [a-z0-9.-]+:[0-9]{1,5}|PROXY 127\.0\.0\.1:9)$/.test(target))
    throw new Error('非法代理规则');
  return 'function FindProxyForURL(url, host) {\n'+
    '  var sites='+JSON.stringify(map)+';\n'+
    '  return Object.prototype.hasOwnProperty.call(sites,String(host).toLowerCase())'+
    ' ? '+JSON.stringify(target)+' : "DIRECT";\n}';
}
export async function desiredNetwork(){
  return (await chrome.storage.local.get(DESIRED))[DESIRED]||{};
}
async function currentLease(){
  return (await chrome.storage.session.get(CREDS))[CREDS]||null;
}
async function applyPAC(desired,lease=null){
  if(!Object.keys(desired).length){
    await chrome.storage.session.remove(CREDS);
    await chrome.proxy.settings.clear({scope:'regular'});
    return;
  }
  const target=lease?proxyEndpoint(lease.host,lease.port):null;
  await chrome.proxy.settings.set({scope:'regular',value:{
    mode:'pac_script',pacScript:{data:buildPAC(desired,target),mandatory:true}
  }});
  const status=await chrome.proxy.settings.get({incognito:false});
  if(status.levelOfControl!=='controlled_by_this_extension')
    throw new Error('另一个扩展或浏览器策略控制了 Chrome 代理');
}
export async function installNetworkGuard(){
  const desired=await desiredNetwork();
  if(Object.keys(desired).length)await applyPAC(desired);
}
export async function stopNetworkGuard(ruleId){
  const desired=await desiredNetwork();
  delete desired[ruleId];
  await chrome.storage.local.set({[DESIRED]:desired});
  await applyPAC(desired);
  if(Object.keys(desired).length)await refreshNetworkBinding({force:true}).catch(()=>{});
}
export async function startNetworkGuard(ruleId,siteOrigin){
  parseBoundHost(siteOrigin);
  const desired=await desiredNetwork();
  desired[ruleId]={siteOrigin};
  await chrome.storage.local.set({[DESIRED]:desired});
  await applyPAC(desired); // blackhole first: fail closed
  return refreshNetworkBinding({force:true});
}
// Called BEFORE applying Cookie snapshots or issuing a sync request.
export async function ensureNetworkForRule(ruleId,siteOrigin){
  const result=await sendControl('NETWORK_BINDING',{ruleId});
  const binding=result?.binding;
  const desired=await desiredNetwork();
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
    const lease=await currentLease();
    if(!force && lease?.expiresAt>Date.now()+5*60*1000)return {state:'ACTIVE'};
    const settings=await getSettings();
    for(const [ruleId,entry] of Object.entries(desired)){
      if(!settings.sites.includes(entry.siteOrigin))throw new Error('网站授权已失效');
      const answer=await sendControl('NETWORK_BINDING',{ruleId});
      if(answer?.binding?.status!=='active' ||
        answer.binding.host!==parseBoundHost(entry.siteOrigin))
        throw new Error('网络绑定已撤销或站点变更');
    }
    const result=await sendControl('NETWORK_TOKEN');
    if(result.type!=='NETWORK_TOKEN'||typeof result.token!=='string'||
      result.token.length<32||!Number.isSafeInteger(result.expiresAt)||
      result.expiresAt<Date.now()+60000)throw new Error('无效的网络租约');
    proxyEndpoint(result.host,result.port);
    const next={username:'browser',password:result.token,
      host:result.host,port:result.port,expiresAt:result.expiresAt};
    await chrome.storage.session.set({[CREDS]:next});
    await applyPAC(desired,next);
    return {state:'ACTIVE',expiresAt:next.expiresAt};
  };
  refreshing=job().catch(async error=>{
    const desired=await desiredNetwork();
    if(Object.keys(desired).length)await applyPAC(desired).catch(()=>{});
    await chrome.storage.session.remove(CREDS);
    throw error;
  }).finally(()=>{refreshing=null});
  return refreshing;
}
export function registerNetworkAuth(){
  chrome.webRequest?.onAuthRequired?.addListener((details,respond)=>{
    (async()=>{
      if(!details.isProxy || details.scheme?.toLowerCase()!=='basic')return {cancel:true};
      const lease=await currentLease();
      if(!lease||Date.now()>=lease.expiresAt ||
        String(details.challenger?.host).toLowerCase()!==lease.host ||
        Number(details.challenger?.port)!==lease.port)return {cancel:true};
      // One device-level credential: Chrome caches proxy Basic auth by realm.
      // The server checks every CONNECT's hostname and accepted rule separately.
      return {authCredentials:{username:lease.username,password:lease.password}};
    })().then(respond,()=>respond({cancel:true}));
  },{urls:['<all_urls>']},['asyncBlocking']);
}
