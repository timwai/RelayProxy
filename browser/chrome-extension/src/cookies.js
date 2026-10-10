// Narrow Cookie-only compatibility adapter. Intentionally rejects domain,
// partitioned and non-root-path cookies rather than widening the rule scope.
// No raw Cookie values are persisted in extension storage or logged.
import { validatePolicy } from './envelope.js';
import { cookieValueTag } from './device-identity.js';

// Backwards incompatible on purpose: old SHA-256 fingerprints cannot be
// trusted for a deletion or silent overwrite. The receiver must explicitly
// reapprove an existing session once when upgrading the development build.
const storeKey = 'browserSyncManagedCookieHmacV1';
const cookieDomain = c => (c.domain||'').replace(/^\./,'').toLowerCase();
const isSafeCookie = (cookie,origin) => {
  const host=new URL(origin).hostname.toLowerCase();
  return cookie && cookie.hostOnly===true && cookieDomain(cookie)===host &&
    cookie.path==='/' && cookie.secure===true &&
    !cookie.partitionKey && cookie.value!==undefined &&
    (!cookie.expirationDate || cookie.expirationDate > Date.now()/1000);
};
const cookieURL = origin => new URL('/',origin).toString();

export async function captureCookies(policy) {
  const selected=validatePolicy(policy);
  const existing=await chrome.cookies.getAll({url:cookieURL(selected.siteOrigin)});
  const result=[];
  const removedNames=[];
  for(const name of selected.cookieNames){
    const matches=existing.filter(cookie=>cookie.name===name);
    if(matches.length===0) { removedNames.push(name); continue; }
    if(matches.length!==1 || !isSafeCookie(matches[0],selected.siteOrigin))
      throw new Error('该站点的 Cookie 作用域不兼容当前安全白名单：'+name);
    const c=matches[0];
    result.push({
      name:c.name,value:c.value,secure:true,httpOnly:c.httpOnly===true,
      sameSite:c.sameSite||'unspecified',
      ...(c.session===false&&Number.isFinite(c.expirationDate)?{expirationDate:c.expirationDate}:{})
    });
  }
  return {siteOrigin:selected.siteOrigin,cookieNames:selected.cookieNames,cookies:result,removedNames};
}
function verifySnapshot(snapshot,expected) {
  const policy=validatePolicy(expected);
  if(!snapshot||snapshot.siteOrigin!==policy.siteOrigin||
    !Array.isArray(snapshot.cookieNames) ||
    JSON.stringify(snapshot.cookieNames)!==JSON.stringify(policy.cookieNames)||
    !Array.isArray(snapshot.cookies) ||
    !Array.isArray(snapshot.removedNames) ||
    snapshot.cookies.length+snapshot.removedNames.length!==policy.cookieNames.length) throw new Error('站点白名单与快照不匹配');
  const names=new Set();
  for(const c of snapshot.cookies){
    if(!c||typeof c!=='object'||!policy.cookieNames.includes(c.name)||names.has(c.name)||
      typeof c.value!=='string'||c.value.length>4096||
      c.secure!==true||typeof c.httpOnly!=='boolean'||
      !['unspecified','no_restriction','lax','strict'].includes(c.sameSite)||
      (c.expirationDate!==undefined &&
       (!Number.isFinite(c.expirationDate)||c.expirationDate<=Date.now()/1000))) {
      throw new Error('Cookie 数据不符合目标规则');
    }
    names.add(c.name);
  }
  for(const name of snapshot.removedNames){
    if(typeof name!=='string'||!policy.cookieNames.includes(name)||names.has(name))
      throw new Error('Cookie 删除名单与目标规则不匹配');
    names.add(name);
  }
  if(names.size!==policy.cookieNames.length) throw new Error('Cookie 快照不完整');
  return policy;
}
export async function applyCookies(ruleId,snapshot,expected,{allowOverwrite=false}={}) {
  const policy=verifySnapshot(snapshot,expected);
  const host=new URL(policy.siteOrigin).hostname;
  const current=await chrome.cookies.getAll({url:cookieURL(policy.siteOrigin)});
  const storage=(await chrome.storage.local.get(storeKey))[storeKey]||{};
  const managed=storage[ruleId]||{};
  // Run all checks before writing *any* Cookie, to avoid silent account swaps.
  const inspected=[];
  for(const c of snapshot.cookies){
    const matches=current.filter(x=>x.name===c.name);
    if(matches.length>1 || matches.some(x=>!isSafeCookie(x,policy.siteOrigin)))
      throw new Error('CONFLICT: 网站已有同名但作用域不同的 Cookie');
    const old=matches[0]||null;
    if(old&&old.value!==c.value&&!allowOverwrite){
      const hash=await cookieValueTag(ruleId,c.name,old.value);
      if(managed[c.name]!==hash) throw new Error('CONFLICT: 接收浏览器已有不同的登录状态');
    }
    inspected.push({incoming:c,current:old});
  }
  const removals=[];
  for(const name of snapshot.removedNames){
    const matches=current.filter(c=>c.name===name);
    if(matches.length>1||matches.some(c=>!isSafeCookie(c,policy.siteOrigin)))
      throw new Error('CONFLICT: 目标同名 Cookie 作用域不同，拒绝删除');
    const old=matches[0]||null;
    // Never delete a Cookie that was not installed by this exact rule,
    // or that the target site/user has changed since the last sync.
    if(old){
      if(!managed[name]||managed[name]!==await cookieValueTag(ruleId,name,old.value))
        throw new Error('CONFLICT: 不允许删除接收端独立登录状态');
    }
    removals.push({name,old});
  }
  const url=cookieURL(policy.siteOrigin);
  const nextManaged={...managed};
  // Chrome has no multi-Cookie transaction. Stage only reversible changes,
  // perform preflight checks above, and compensate in reverse on failure.
  const changed=[];
  const cookieDetails=(c)=>({
    url,name:c.name,value:c.value,path:'/',secure:true,httpOnly:c.httpOnly,
    ...(c.sameSite!=='unspecified'?{sameSite:c.sameSite}:{}),
    ...(c.expirationDate!==undefined?{expirationDate:c.expirationDate}:{})
  });
  async function rollback() {
    let incomplete=false;
    for(const item of [...changed].reverse()){
      try {
        const matches=(await chrome.cookies.getAll({url})).filter(c=>c.name===item.name);
        if(item.expected===null && matches.length===0) {
          // Chrome deletion succeeded: we can safely restore a previously
          // rule-owned Cookie, provided no replacement appeared.
          if(item.old){
            const restored=await chrome.cookies.set(cookieDetails(item.old));
            if(!restored||restored.value!==item.old.value||!isSafeCookie(restored,policy.siteOrigin))
              incomplete=true;
          }
          continue;
        }
        if(matches.length!==1||!isSafeCookie(matches[0],policy.siteOrigin)||
            matches[0].value!==item.expected){
          // If the website changed this Cookie while we were writing, do not
          // overwrite its new value to "restore" a potentially newer login.
          if(matches.length===0 && !item.old) continue;
          incomplete=true;
          continue;
        }
        if(item.old) {
          const restored=await chrome.cookies.set(cookieDetails(item.old));
          if(!restored||restored.value!==item.old.value||!isSafeCookie(restored,policy.siteOrigin))
            incomplete=true;
        }else if(!await chrome.cookies.remove({url,name:item.name})){
          incomplete=true;
        }
      }catch{
        incomplete=true;
      }
    }
    return !incomplete;
  }
  try {
    for(const {incoming:c,current:old} of inspected){
      if(!old||old.value!==c.value){
        // Record before calling Chrome: a rejected/partial response may still
        // have changed the Cookie jar.
        changed.push({name:c.name,old,expected:c.value});
        const updated=await chrome.cookies.set(cookieDetails(c));
        if(!updated||!isSafeCookie(updated,policy.siteOrigin)||
          updated.name!==c.name||updated.value!==c.value||
          cookieDomain(updated)!==host) throw new Error('Cookie 写入失败');
      }
      // Identical Cookies may belong to a separate login on the receiver;
      // do not silently claim them for a future remote logout.
      if(!old||old.value!==c.value)
        nextManaged[c.name]=await cookieValueTag(ruleId,c.name,c.value);
      else if(managed[c.name]!==await cookieValueTag(ruleId,c.name,old.value))
        delete nextManaged[c.name];
    }
    for(const {name,old} of removals){
      if(old){
        changed.push({name,old,expected:null});
        const removed=await chrome.cookies.remove({url,name});
        if(!removed)throw new Error('Cookie 删除失败');
      }
      delete nextManaged[name];
    }
    storage[ruleId]=nextManaged;
    await chrome.storage.local.set({[storeKey]:storage});
  }catch{
    const restored=await rollback();
    // Never include Cookie names, values or site origins in exceptions/logs.
    throw new Error(restored?'APPLY_FAILED: 已恢复写入前状态':
      'PARTIAL_ROLLBACK: 无法保证所有 Cookie 已恢复，已暂停本次同步');
  }
  return {count:inspected.length,removed:removals.length,status:'APPLIED'};
}
