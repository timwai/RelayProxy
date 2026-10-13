// All Local/Session Storage keys for a single explicitly approved HTTP(S) origin.
// sessionStorage belongs to ONE tab's top-level browsing context; Chrome does
// not provide an API to enumerate all other tabs' sessionStorage atomically.
// Content script executes in ISOLATED world; it uses the page origin's Storage.
import {validatePolicy} from './envelope.js';
import {cookieValueTag} from './device-identity.js';

const key='browserSyncManagedWebStorageV2';
const journalKey='browserSyncStorageRestoreJournal';
const maxKeys=4096;
const MAX_STORAGE_BYTES=4*1024*1024; // byte-accurate full-site ceiling, no dropped keys
function readBoth(expectedOrigin,includeLocal=true,includeSession=true){
  if(location.origin!==expectedOrigin)throw new Error('站点导航已变化');
  function read(store){
    const result=Object.create(null);
    for(let i=0;i<store.length;i++){
      const k=store.key(i);
      if(k!==null)result[k]=store.getItem(k);
    }
    return result;
  }
  return {local:includeLocal?read(localStorage):{},session:includeSession?read(sessionStorage):{}};
}
function mutate(expectedOrigin,changes){
  if(location.origin!==expectedOrigin)throw new Error('站点导航已变化');
  for(const {kind,name,value} of changes){
    const s=kind==='local'?localStorage:sessionStorage;
    if(value===null)s.removeItem(name);
    else s.setItem(name,value);
  }
  return true;
}
export async function tabFor(origin,{create=false}={}){
  const tabs=await chrome.tabs.query({});
  const active=await chrome.tabs.query({active:true,lastFocusedWindow:true});
  let tab=[...active,...tabs].find(t=>{
    try{return !t.incognito && new URL(t.url).origin===origin;}catch{return false;}
  });
  if(!tab&&create){
    tab=await chrome.tabs.create({url:origin+'/',active:false});
    if(tab.status!=='complete'){
      await new Promise((resolve,reject)=>{
        const timer=setTimeout(()=>{cleanup();reject(new Error('打开目标站点超时'));},15000);
        const changed=(id,info)=>{if(id===tab.id&&info.status==='complete'){cleanup();resolve();}};
        const removed=id=>{if(id===tab.id){cleanup();reject(new Error('目标标签页已关闭'));}};
        function cleanup(){clearTimeout(timer);chrome.tabs.onUpdated.removeListener(changed);chrome.tabs.onRemoved.removeListener(removed);}
        chrome.tabs.onUpdated.addListener(changed);chrome.tabs.onRemoved.addListener(removed);
      });
    }
  }
  if(!tab)throw new Error('请先在普通 Chrome 标签页打开所选 HTTP/HTTPS 网站后再同步 Storage');
  const latest=await chrome.tabs.get(tab.id);
  if(latest.incognito||new URL(latest.url).origin!==origin)throw new Error('站点已经跳转，拒绝跨域读取 Storage');
  return tab.id;
}
export async function execute(tabId,func,args){
  const result=await chrome.scripting.executeScript({target:{tabId},world:'ISOLATED',func,args});
  if(result?.length!==1||!result[0]||!('result' in result[0]))
    throw new Error('页面 Storage 读取/写入失败');
  return result[0].result;
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
export async function captureWebStorage(policy){
  const p=validatePolicy(policy);
  if(![2,3,4].includes(p.schemaVersion)||(!p.localStorage&&!p.sessionStorage))throw new Error('需要 Storage 同步授权');
  const id=await tabFor(p.siteOrigin);
  return verify(await execute(id,readBoth,[p.siteOrigin,p.localStorage,p.sessionStorage]));
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
  const present=verify(await execute(tabId,readBoth,[p.siteOrigin,p.localStorage,p.sessionStorage]));
  const state=(await chrome.storage.local.get(key))[key]||{};
  const oldOwnership=state[ruleId]||{local:{},session:{}};
  const owner={local:{...oldOwnership.local},session:{...oldOwnership.session}};
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
  let started=false,finished=false;
  return {
    tabId,
    async apply(){
      if(started)throw new Error('Storage 事务不能重复应用');
      started=true;
      journal[ruleId]={startedAt:Date.now()};
      await chrome.storage.local.set({[journalKey]:journal});
      await execute(tabId,mutate,[p.siteOrigin,changes]);
    },
    async commit(){
      if(!started||finished)throw new Error('Storage 事务状态无效');
      state[ruleId]=owner;
      const live=(await chrome.storage.local.get(journalKey))[journalKey]||{};
      delete live[ruleId];
      await chrome.storage.local.set({[key]:state,[journalKey]:live});
      finished=true;
    },
    async rollback(){
      if(!started||finished)return true;
      try{
        const live=verify(await execute(tabId,readBoth,[p.siteOrigin,p.localStorage,p.sessionStorage]));
        // Never overwrite a website mutation that appeared after our write.
        for(const change of changes){
          const value=live[change.kind][change.name];
          if(value!==change.value && !(value===undefined&&change.value===null) &&
            !(value===change.oldValue))return false;
        }
        await execute(tabId,mutate,[p.siteOrigin,
          changes.filter(c=>live[c.kind][c.name]!==c.oldValue).reverse()
            .map(c=>({kind:c.kind,name:c.name,value:c.oldValue}))]);
        const record=(await chrome.storage.local.get(journalKey))[journalKey]||{};
        delete record[ruleId];
        await chrome.storage.local.set({[journalKey]:record});
        finished=true;
        return true;
      }catch{return false;}
    }
  };
}
