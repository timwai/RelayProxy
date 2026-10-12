// Browser Sync v2: every cookie reachable by the explicitly authorized HTTP(S)
// origin, preserving domain, path, session and supported partition attributes.
// Never store cookie values in extension storage; only keyed HMAC ownership tags.
import {validatePolicy} from './envelope.js';
import {cookieValueTag} from './device-identity.js';

const ownerKey='browserSyncManagedAllCookieHmacV2';
const journalKey='browserSyncRestoreJournal';
const plain=o=>Object.assign(Object.create(null),o||{});
const hostOf=s=>new URL(s).hostname.toLowerCase();
const domainOf=c=>String(c.domain||'').replace(/^\./,'').toLowerCase();
const partitionOf=c=>c.partitionKey?.topLevelSite||'';
export const cookieIdentity=c=>JSON.stringify([domainOf(c),String(c.path||'/'),String(c.name),!!c.hostOnly,partitionOf(c)]);
const supportsOrigin=(c,origin)=>{
  const host=hostOf(origin),domain=domainOf(c),site=partitionOf(c);
  // Never copy HTTPS-only credentials into an unencrypted HTTP site.
  // CHIPS and SameSite=None require Secure and are not HTTP cookie scopes.
  if(new URL(origin).protocol==='http:' &&
    (c.secure || site || c.sameSite==='no_restriction'))return false;
  if(!domain || !(host===domain || (!c.hostOnly&&host.endsWith('.'+domain))))return false;
  // A cross-site ancestor partition is not available to this top-level site.
  if(c.partitionKey?.hasCrossSiteAncestor===true)return false;
  if(!String(c.path).startsWith('/') || typeof c.name!=='string' || typeof c.value!=='string')return false;
  if(site){
    try{
      const s=new URL(site);
      if(s.protocol!=='https:' || !(host===s.hostname||host.endsWith('.'+s.hostname)))return false;
    }catch{return false;}
  }
  return true;
};
async function allFor(origin){
  const host=hostOf(origin),labels=host.split('.');
  const suffixes=[host];
  for(let i=1;i<labels.length-1;i++)suffixes.push(labels.slice(i).join('.'));
  // Chrome returns only unpartitioned cookies when partitionKey is omitted.
  // An empty partitionKey requests all partitions; subsequent filtering
  // limits the snapshot to the approved site's own top-level context.
  const groups=await Promise.all(suffixes.map(domain=>
    chrome.cookies.getAll({domain,partitionKey:{}})));
  const entries=new Map();
  for(const c of groups.flat())if(supportsOrigin(c,origin))entries.set(cookieIdentity(c),c);
  return entries;
}
function fromChrome(c){
  return {name:c.name,value:c.value,domain:domainOf(c),hostOnly:!!c.hostOnly,
    path:c.path,secure:!!c.secure,httpOnly:!!c.httpOnly,
    sameSite:c.sameSite||'unspecified',session:!!c.session,
    ...(Number.isFinite(c.expirationDate)&&!c.session?{expirationDate:c.expirationDate}:{}),
    ...(c.partitionKey?{partitionKey:{topLevelSite:c.partitionKey.topLevelSite,
      ...(typeof c.partitionKey.hasCrossSiteAncestor==='boolean'
        ?{hasCrossSiteAncestor:c.partitionKey.hasCrossSiteAncestor}:{})}}:{})};
}
function checked(c,origin){
  if(!c||typeof c!=='object'||!supportsOrigin(c,origin)||
    c.name.length>256||c.value.length>8192||c.path.length>1024||c.domain.length>256||
    typeof c.hostOnly!=='boolean'||typeof c.secure!=='boolean'||
    typeof c.httpOnly!=='boolean'||typeof c.session!=='boolean'||
    !['unspecified','no_restriction','lax','strict'].includes(c.sameSite)||
    (c.expirationDate!==undefined&&(!Number.isFinite(c.expirationDate)||c.expirationDate<=Date.now()/1000))||
    (c.partitionKey&&(!c.partitionKey.topLevelSite ||
      Object.keys(c.partitionKey).some(k=>!['topLevelSite','hasCrossSiteAncestor'].includes(k)) ||
      (c.partitionKey.hasCrossSiteAncestor!==undefined &&
       typeof c.partitionKey.hasCrossSiteAncestor!=='boolean'))))
    throw new Error('站点 Cookie 属性无效或超出安全限制');
  return c;
}
function writeDetails(c,origin){
  const details={url:origin+'/',name:c.name,value:c.value,path:c.path,
    secure:c.secure,httpOnly:c.httpOnly,
    ...(c.hostOnly?{}:{domain:c.domain}),
    ...(c.sameSite!=='unspecified'?{sameSite:c.sameSite}:{}),
    ...(!c.session&&c.expirationDate!==undefined?{expirationDate:c.expirationDate}:{}),
    ...(c.partitionKey?{partitionKey:c.partitionKey}:{})};
  return details;
}
function removeDetails(c,origin){
  // URL path is required so Chrome chooses the cookie's path. Refuse ambiguous
  // identity collisions at preflight rather than deleting unrelated cookies.
  return {url:new URL(c.path,origin).toString(),name:c.name,
    ...(c.partitionKey?{partitionKey:c.partitionKey}:{})};
}
export async function captureAllCookies(policy){
  const p=validatePolicy(policy);
  if(![2,3,4].includes(p.schemaVersion) || p.allCookies!==true)throw new Error('需要全量站点授权');
  const jar=await allFor(p.siteOrigin);
  const cookies=[...jar.values()].map(fromChrome).sort((a,b)=>cookieIdentity(a).localeCompare(cookieIdentity(b)));
  if(cookies.length>1000)throw new Error('站点 Cookie 超过 1000 条安全上限');
  for(const c of cookies)checked(c,p.siteOrigin);
  return {schemaVersion:2,siteOrigin:p.siteOrigin,cookies};
}
export async function applyAllCookies(ruleId,snapshot,policy,{allowOverwrite=false}={}){
  const p=validatePolicy(policy);
  if(![2,3,4].includes(p.schemaVersion) || p.allCookies!==true || snapshot?.schemaVersion!==2 ||
    snapshot.siteOrigin!==p.siteOrigin || !Array.isArray(snapshot.cookies) ||
    snapshot.cookies.length>1000)throw new Error('站点 Cookie 策略不匹配');
  const next=new Map();
  for(const item of snapshot.cookies){
    const c=checked(item,p.siteOrigin),key=cookieIdentity(c);
    if(next.has(key))throw new Error('Cookie 身份重复');
    next.set(key,c);
  }
  const journal=(await chrome.storage.local.get(journalKey))[journalKey]||{};
  if(journal[ruleId])throw new Error('PARTIAL_ROLLBACK: 上次浏览器状态恢复意外中断');
  const state=(await chrome.storage.local.get(ownerKey))[ownerKey]||{};
  const managed=plain(state[ruleId]);
  const current=await allFor(p.siteOrigin);
  const operations=[],updated=plain(managed);
  for(const [id,c] of next){
    const old=current.get(id),tag=old&&await cookieValueTag(ruleId,id,old.value);
    if(old&&old.value!==c.value&&!allowOverwrite&&managed[id]!==tag)
      throw new Error('CONFLICT: 接收端已有不同 Cookie 登录状态');
    if(!old||old.value!==c.value){
      operations.push({kind:'set',id,c,old});
      updated[id]=await cookieValueTag(ruleId,id,c.value);
    }else if(managed[id]!==tag)delete updated[id];
  }
  for(const id of Object.keys(managed)){
    if(next.has(id))continue;
    const old=current.get(id);
    if(!old){delete updated[id];continue;}
    if(managed[id]!==await cookieValueTag(ruleId,id,old.value))
      throw new Error('CONFLICT: 不允许删除由接收站点修改的 Cookie');
    operations.push({kind:'delete',id,old});
    delete updated[id];
  }
  // Same name / path can coexist for distinct domain/partition scopes.
  // Chrome's remove API cannot address domain, so reject ambiguous deletes.
  for(const op of operations.filter(o=>o.kind==='delete')){
    const details=removeDetails(op.old,p.siteOrigin);
    if([...current.values()].filter(c=>c.name===op.old.name&&
        partitionOf(c)===partitionOf(op.old)&&details.url.startsWith(p.siteOrigin)).filter(c=>
        details.url.startsWith(p.siteOrigin+c.path)).length>1)
      throw new Error('CONFLICT: Cookie 删除作用域不明确');
  }
  const changes=[];
  journal[ruleId]={startedAt:Date.now(),type:'cookies-v2'};
  await chrome.storage.local.set({[journalKey]:journal});
  let restored=true;
  try{
    for(const op of operations){
      changes.push(op);
      if(op.kind==='set'){
        const actual=await chrome.cookies.set(writeDetails(op.c,p.siteOrigin));
        if(!actual||cookieIdentity(actual)!==op.id||actual.value!==op.c.value)
          throw new Error('Cookie 写入校验失败');
      }else{
        const removed=await chrome.cookies.remove(removeDetails(op.old,p.siteOrigin));
        if(!removed)throw new Error('Cookie 删除失败');
      }
    }
    state[ruleId]=updated;
    const live=(await chrome.storage.local.get(journalKey))[journalKey]||{};
    delete live[ruleId];
    await chrome.storage.local.set({[ownerKey]:state,[journalKey]:live});
  }catch{
    for(const op of changes.reverse()){
      try{
        const latest=await allFor(p.siteOrigin),present=latest.get(op.id);
        if(op.kind==='set'){
          if(present?.value===op.c.value){
            if(op.old){
              const replaced=await chrome.cookies.set(writeDetails(fromChrome(op.old),p.siteOrigin));
              if(!replaced||replaced.value!==op.old.value)restored=false;
            }else if(!await chrome.cookies.remove(removeDetails(op.c,p.siteOrigin)))restored=false;
          }else if((op.old&&present?.value===op.old.value)||(!op.old&&!present)){
            // An API failure made no change.
          }else restored=false;
        }else if(!present){
          const replaced=await chrome.cookies.set(writeDetails(fromChrome(op.old),p.siteOrigin));
          if(!replaced||replaced.value!==op.old.value)restored=false;
        }else if(present.value!==op.old.value)restored=false;
      }catch{restored=false;}
    }
    if(restored){
      try{
        const live=(await chrome.storage.local.get(journalKey))[journalKey]||{};
        delete live[ruleId];await chrome.storage.local.set({[journalKey]:live});
      }catch{restored=false;}
    }
    throw new Error(restored?'APPLY_FAILED: Cookie 应用失败并已回滚':
      'PARTIAL_ROLLBACK: Cookie 恢复失败，已暂停自动同步');
  }
  return {count:next.size,removed:operations.filter(o=>o.kind==='delete').length,status:'APPLIED'};
}
