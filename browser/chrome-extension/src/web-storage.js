// All Local/Session Storage keys for a single explicitly approved HTTP(S) origin.
// sessionStorage belongs to ONE tab's top-level browsing context; Chrome does
// not provide an API to enumerate all other tabs' sessionStorage atomically.
// Content script executes in ISOLATED world; it uses the page origin's Storage.
import {validatePolicy} from './envelope.js';
import {cookieValueTag} from './device-identity.js';
import {syncFailure} from './sync-diagnostics.js';

const key='browserSyncManagedWebStorageV2';
const journalKey='browserSyncStorageRestoreJournal';
const maxKeys=4096;
const MAX_STORAGE_BYTES=4*1024*1024; // byte-accurate full-site ceiling, no dropped keys
function readBoth(expectedOrigin,includeLocal=true,includeSession=true){
  try{
  if(location.origin!==expectedOrigin)throw new Error('站点导航已变化');
  function read(store){
    const result=Object.create(null);
    // Use ISOLATED-world native accessors, including for keys named getItem,
    // length or __proto__. Never coerce a missing value to "undefined".
    const proto=Object.getPrototypeOf(store);
    const length=Object.getOwnPropertyDescriptor(proto,'length').get;
    for(let i=0;i<length.call(store);i++){
      const k=proto.key.call(store,i);
      if(k===null)throw new Error('Storage 采集期间键已变化，请重试');
      const value=proto.getItem.call(store,k);
      if(typeof value!=='string')throw new Error('Storage 采集期间值已变化，请重试');
      result[k]=value;
    }
    return result;
  }
  return {local:includeLocal?read(localStorage):{},session:includeSession?read(sessionStorage):{}};
  }catch(error){
    // Thrown executeScript functions can produce a null InjectionResult in
    // Chrome. Return a fixed reason so the worker retains the real failure.
    return {__relayStorageError:error?.name==='SecurityError'?'STORAGE_ACCESS_DENIED':
      String(error?.message)==='站点导航已变化'?'DOCUMENT_CHANGED':
      /采集期间/.test(String(error?.message))?'STORAGE_UNSTABLE':'STORAGE_READ_FAILED'};
  }
}
function mutate(expectedOrigin,changes,expected=null){
  let applied=0;
  try{
  if(location.origin!==expectedOrigin)throw new Error('站点导航已变化');
  // Compare in the same synchronous page task that performs the mutation.
  // Do not overwrite a concurrent website login since extension preflight.
  if(expected)for(const kind of ['local','session']){
    if(!expected[kind])continue;
    const store=kind==='local'?localStorage:sessionStorage;
    const proto=Object.getPrototypeOf(store);
    const names=Object.keys(expected[kind]);
    if(Object.getOwnPropertyDescriptor(proto,'length').get.call(store)!==names.length||
       names.some(name=>proto.getItem.call(store,name)!==expected[kind][name]))
      throw new Error('CONFLICT: Storage 在恢复前已被网站修改，请重试');
  }
  for(const {kind,name,value} of changes)
    if(!['local','session'].includes(kind)||typeof name!=='string'||
       value!==null&&typeof value!=='string')throw new Error('Storage 写入数据无效');
  for(const {kind,name,value} of changes){
    const s=kind==='local'?localStorage:sessionStorage;
    const proto=Object.getPrototypeOf(s);
    if(value===null)proto.removeItem.call(s,name);
    else proto.setItem.call(s,name,value);
    applied++;
  }
  return true;
  }catch(error){
    return {__relayStorageError:error?.name==='QuotaExceededError'?'STORAGE_QUOTA':
      error?.name==='SecurityError'?'STORAGE_ACCESS_DENIED':
      String(error?.message)==='站点导航已变化'?'DOCUMENT_CHANGED':
      String(error?.message).startsWith('CONFLICT:')?'STORAGE_CHANGED':'STORAGE_WRITE_FAILED',
      applied};
  }
}
export async function tabFor(origin,{create=false}={}){
  const tabs=await chrome.tabs.query({});
  const active=await chrome.tabs.query({active:true,lastFocusedWindow:true});
  const matches=t=>{
    try{return !t.incognito && new URL(t.url).origin===origin;}catch{return false;}
  };
  const candidates=tabs.filter(matches);
  const recent=[...candidates].sort((a,b)=>(b.lastAccessed||0)-(a.lastAccessed||0));
  let tab=active.find(matches)||recent[0];
  if(!active.some(matches)&&recent.length>1&&
     (!recent[0].lastAccessed||recent[0].lastAccessed===recent[1].lastAccessed))
    throw new Error('同站点有多个 Storage 标签页，请先激活要同步的页面，或关闭其他同站点标签页');
  if(!tab&&create){
    tab=await chrome.tabs.create({url:origin+'/',active:false});
    if(tab.status!=='complete'){
      await new Promise((resolve,reject)=>{
        const timer=setTimeout(()=>{cleanup();reject(new Error('打开目标站点超时'));},15000);
        const changed=(id,info)=>{if(id===tab.id&&info.status==='complete'){cleanup();resolve();}};
        const removed=id=>{if(id===tab.id){cleanup();reject(new Error('目标标签页已关闭'));}};
        function cleanup(){clearTimeout(timer);chrome.tabs.onUpdated.removeListener(changed);chrome.tabs.onRemoved.removeListener(removed);}
        chrome.tabs.onUpdated.addListener(changed);chrome.tabs.onRemoved.addListener(removed);
        // A cached/local page can finish between tabs.create resolving and
        // listener registration. Inspect it after subscribing to close that
        // gap instead of waiting for a completion event that already happened.
        chrome.tabs.get(tab.id).then(current=>{
          if(current.status==='complete'){cleanup();resolve();}
        },()=>{cleanup();reject(new Error('目标标签页已关闭'));});
      });
    }
  }
  if(!tab)throw new Error('请先在普通 Chrome 标签页打开所选 HTTP/HTTPS 网站后再同步 Storage');
  const latest=await chrome.tabs.get(tab.id);
  if(latest.incognito||new URL(latest.pendingUrl||latest.url).origin!==origin)throw new Error('站点已经跳转，拒绝跨域读取 Storage');
  if(latest.status==='loading'||latest.discarded||latest.frozen)
    throw new Error('Storage 标签页尚未就绪，请激活页面并等待加载完成');
  return tab.id;
}
function checkedFrame(result,documentId=null){
  if(result?.length!==1||!result[0]||result[0].frameId!==undefined&&result[0].frameId!==0||
     !Object.hasOwn(result[0],'result')||result[0].result===null||result[0].result===undefined)
    throw new Error('页面 Storage 读取/写入失败');
  if(documentId&&result[0].documentId!==documentId)
    throw new Error('Storage 目标页面已经刷新，请重新同步');
  const reply=result[0].result;
  if(Object.hasOwn(reply,'__relayStorageError')){
    const error=syncFailure(reply.__relayStorageError==='STORAGE_CHANGED'
      ?'CONFLICT: Storage 在恢复前已被网站修改，请重试':'页面 Storage 读取/写入失败',reply.__relayStorageError);
    // Zero completed native mutations means there is nothing to undo. Do not
    // turn an unrelated website change before our write into a partial rollback.
    error.storageUnchanged=reply.applied===0;
    throw error;
  }
  return result[0];
}
export async function execute(tabId,func,args,documentId=null){
  return checkedFrame(await chrome.scripting.executeScript({
    target:documentId?{tabId,documentIds:[documentId]}:{tabId,frameIds:[0]},
    world:'ISOLATED',func,args}),documentId).result;
}
function verify(data){
  if(!data||typeof data!=='object')throw new Error('Storage 快照格式无效');
  const clean={};
  for(const kind of ['local','session']){
    const input=data[kind];
    if(!input||typeof input!=='object'||Array.isArray(input)||Object.keys(input).length>maxKeys)
      throw new Error('Storage 键值数量超限');
    const out=Object.create(null);
    for(const [k,v] of Object.entries(input)){
      if(typeof v!=='string'||k.length>4096||new TextEncoder().encode(v).length>MAX_STORAGE_BYTES)
        throw new Error('Storage Key/Value 不符合限制');
      out[k]=v;
    }
    clean[kind]=out;
  }
  if(new TextEncoder().encode(JSON.stringify(clean)).length>MAX_STORAGE_BYTES)
    throw new Error('完整 Storage 数据超出 4 MiB 安全上限；没有截断 Key，可清理无关站点缓存后重试');
  return clean;
}
const sameStorage=(a,b)=>['local','session'].every(kind=>{
  const names=Object.keys(a[kind]);
  return names.length===Object.keys(b[kind]).length&&
    names.every(name=>Object.hasOwn(b[kind],name)&&a[kind][name]===b[kind][name]);
});
async function readStorage(tabId,p,documentId=null){
  return verify(await execute(tabId,readBoth,[p.siteOrigin,p.localStorage,p.sessionStorage],documentId));
}
export async function captureWebStorage(policy,{tabId=null}={}){
  const p=validatePolicy(policy);
  if(![2,3,4].includes(p.schemaVersion)||(!p.localStorage&&!p.sessionStorage))throw new Error('需要 Storage 同步授权');
  const id=tabId??await tabFor(p.siteOrigin);
  let before=await readStorage(id,p);
  for(let attempt=0;attempt<2;attempt++){
    const after=await readStorage(id,p);
    if(sameStorage(before,after))return after;
    before=after;
  }
  throw new Error('Storage 在采集期间持续变化，请等待页面稳定后重试');
}
export async function prepareWebStorageRestore(ruleId,data,policy,{allowOverwrite=false}={}){
  const p=validatePolicy(policy);
  if(![2,3,4].includes(p.schemaVersion)||(!p.localStorage&&!p.sessionStorage))throw new Error('需要 Storage 同步授权');
  const incoming=verify(data);
  if(!p.localStorage&&Object.keys(incoming.local).length ||
     !p.sessionStorage&&Object.keys(incoming.session).length)
    throw new Error('快照包含未授权的 Storage 数据类型');
  const journal=(await chrome.storage.local.get(journalKey))[journalKey]||{};
  if(journal[ruleId])throw new Error('PARTIAL_ROLLBACK: Storage 上次应用中断');
  const tabId=await tabFor(p.siteOrigin,{create:true});
  // Bind the complete transaction to this document, not just its tab ID.
  const documents=await chrome.scripting.executeScript({target:{tabId,frameIds:[0]},
    world:'ISOLATED',func:readBoth,args:[p.siteOrigin,p.localStorage,p.sessionStorage]});
  const frame=checkedFrame(documents);
  const documentId=frame.documentId||null;
  const present=verify(frame.result);
  const state=(await chrome.storage.local.get(key))[key]||{};
  const oldOwnership=state[ruleId]||{local:{},session:{}};
  const owner={local:Object.assign(Object.create(null),oldOwnership.local),
    session:Object.assign(Object.create(null),oldOwnership.session)};
  const changes=[];
  for(const kind of ['local','session']){
    if(kind==='local'&&!p.localStorage || kind==='session'&&!p.sessionStorage)continue;
    for(const [name,value] of Object.entries(incoming[kind])){
      const previous=present[kind][name];
      const tag=previous!==undefined?await cookieValueTag(ruleId,kind+':'+name,previous):null;
      if(previous!==undefined&&previous!==value&&!allowOverwrite&&oldOwnership[kind]?.[name]!==tag)
        throw new Error('CONFLICT: 接收端 Storage 已有不同登录状态');
      if(previous!==value){
        changes.push({kind,name,value,oldValue:previous===undefined?null:previous});
        owner[kind][name]=await cookieValueTag(ruleId,kind+':'+name,value);
      }else if(oldOwnership[kind]?.[name]!==tag)delete owner[kind][name];
    }
    for(const [name,tag] of Object.entries(oldOwnership[kind]||{})){
      if(Object.hasOwn(incoming[kind],name))continue;
      const previous=present[kind][name];
      if(previous===undefined){delete owner[kind][name];continue;}
      if(tag!==await cookieValueTag(ruleId,kind+':'+name,previous))
        throw new Error('CONFLICT: 目标 Storage 已被本地网站修改，不能远程删除');
      changes.push({kind,name,value:null,oldValue:previous});
      delete owner[kind][name];
    }
  }
  const expected={local:Object.assign(Object.create(null),present.local),
    session:Object.assign(Object.create(null),present.session)};
  for(const {kind,name,value} of changes){
    if(value===null)delete expected[kind][name];
    else expected[kind][name]=value;
  }
  const selectedBefore={...(p.localStorage?{local:present.local}:{}),
    ...(p.sessionStorage?{session:present.session}:{})};
  async function verifyApplied(){
    if(!sameStorage(await readStorage(tabId,p,documentId),expected))
      throw new Error('Storage 写入后键值不一致，拒绝报告同步成功');
  }
  let started=false,finished=false,unchanged=false;
  return {
    tabId,
    // Website scripts may initialize Storage again after the extension write.
    // Never ACK an already-observed value change or missing synchronized key.
    async verifySiteState(){
      const live=await readStorage(tabId,p);
      for(const kind of ['local','session']){
        for(const [name,value] of Object.entries(incoming[kind]))
          if(live[kind][name]!==value)
            throw new Error('PARTIAL_ROLLBACK: Storage 被目标网站脚本修改，请检查目标页面');
      }
      for(const {kind,name,value} of changes)
        if(value===null&&Object.hasOwn(live[kind],name))
          throw new Error('PARTIAL_ROLLBACK: Storage 被目标网站脚本修改，请检查目标页面');
    },
    async apply(){
      if(started)throw new Error('Storage 事务不能重复应用');
      started=true;
      journal[ruleId]={startedAt:Date.now()};
      await chrome.storage.local.set({[journalKey]:journal});
      try{await execute(tabId,mutate,[p.siteOrigin,changes,selectedBefore],documentId);}
      catch(error){unchanged=error.storageUnchanged===true;throw error;}
      await verifyApplied();
    },
    async commit(){
      if(!started||finished)throw new Error('Storage 事务状态无效');
      await verifyApplied();
      state[ruleId]=owner;
      const live=(await chrome.storage.local.get(journalKey))[journalKey]||{};
      delete live[ruleId];
      await chrome.storage.local.set({[key]:state,[journalKey]:live});
      finished=true;
    },
    async rollback(){
      if(!started||finished)return true;
      try{
        if(unchanged){
          const record=(await chrome.storage.local.get(journalKey))[journalKey]||{};
          delete record[ruleId];
          await chrome.storage.local.set({[journalKey]:record});
          finished=true;
          return true;
        }
        const live=await readStorage(tabId,p,documentId);
        // Never overwrite a website mutation that appeared after our write.
        for(const change of changes){
          const value=live[change.kind][change.name];
          if(value!==change.value && !(value===undefined&&change.value===null) &&
            !(value===change.oldValue))return false;
        }
        await execute(tabId,mutate,[p.siteOrigin,
          changes.filter(c=>live[c.kind][c.name]!==c.oldValue).reverse()
            .map(c=>({kind:c.kind,name:c.name,value:c.oldValue}))],documentId);
        if(!sameStorage(await readStorage(tabId,p,documentId),present))return false;
        const record=(await chrome.storage.local.get(journalKey))[journalKey]||{};
        delete record[ruleId];
        await chrome.storage.local.set({[journalKey]:record});
        finished=true;
        return true;
      }catch{return false;}
    }
  };
}
