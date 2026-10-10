// Narrow Cookie-only compatibility adapter. Intentionally rejects domain,
// partitioned and non-root-path cookies rather than widening the rule scope.
// No raw Cookie values are persisted in extension storage or logged.
import { validatePolicy } from './envelope.js';

const digest = async value => {
  const bytes = new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(value)));
  return Array.from(bytes,b=>b.toString(16).padStart(2,'0')).join('');
};
const storeKey = 'browserSyncManagedCookieHashes';
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
  for(const name of selected.cookieNames){
    const matches=existing.filter(cookie=>cookie.name===name);
    if(matches.length!==1 || !isSafeCookie(matches[0],selected.siteOrigin))
      throw new Error('该站点的 Cookie 作用域不兼容当前安全白名单：'+name);
    const c=matches[0];
    result.push({
      name:c.name,value:c.value,secure:true,httpOnly:c.httpOnly===true,
      sameSite:c.sameSite||'unspecified',
      ...(c.session===false&&Number.isFinite(c.expirationDate)?{expirationDate:c.expirationDate}:{})
    });
  }
  return {siteOrigin:selected.siteOrigin,cookieNames:selected.cookieNames,cookies:result};
}
function verifySnapshot(snapshot,expected) {
  const policy=validatePolicy(expected);
  if(!snapshot||snapshot.siteOrigin!==policy.siteOrigin||
    !Array.isArray(snapshot.cookieNames) ||
    JSON.stringify(snapshot.cookieNames)!==JSON.stringify(policy.cookieNames)||
    !Array.isArray(snapshot.cookies) ||
    snapshot.cookies.length!==policy.cookieNames.length) throw new Error('站点白名单与快照不匹配');
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
      const hash=await digest(old.value);
      if(managed[c.name]!==hash) throw new Error('CONFLICT: 接收浏览器已有不同的登录状态');
    }
    inspected.push({incoming:c,current:old});
  }
  const nextManaged={...managed};
  for(const {incoming:c,current:old} of inspected){
    if(!old||old.value!==c.value){
      const details={
        url:cookieURL(policy.siteOrigin),name:c.name,value:c.value,
        path:'/',secure:true,httpOnly:c.httpOnly,
        ...(c.sameSite!=='unspecified'?{sameSite:c.sameSite}:{})
      };
      if(c.expirationDate!==undefined)details.expirationDate=c.expirationDate;
      const updated=await chrome.cookies.set(details);
      if(!updated||!isSafeCookie(updated,policy.siteOrigin)||
        updated.name!==c.name||updated.value!==c.value||
        cookieDomain(updated)!==host) throw new Error('Chrome 拒绝恢复 Cookie：'+c.name);
    }
    nextManaged[c.name]=await digest(c.value);
  }
  storage[ruleId]=nextManaged;
  await chrome.storage.local.set({[storeKey]:storage});
  return {count:inspected.length,status:'APPLIED'};
}
