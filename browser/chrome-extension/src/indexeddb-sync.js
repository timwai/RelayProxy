// IndexedDB per-origin capture/restore. Runs only in a permissioned, normal
// top-level tab (ISOLATED world). The injected function is self-contained,
// because chrome.scripting serializes the function without lexical bindings.
// Session snapshots remain in memory and inside the existing signed E2EE
// envelope; persisted extension metadata contains keyed ownership tags only.
import {validatePolicy} from './envelope.js';
import {cookieValueTag} from './device-identity.js';
import {tabFor} from './web-storage.js';

export const MAX_INDEXEDDB_BYTES=4*1024*1024;
// Chrome scripting IPC may reorder object properties even when the actual
// IndexedDB snapshot is identical. Always canonicalize before comparing or
// computing ownership HMACs; JSON.stringify(object) is not stable across worlds.
export function canonicalIndexedDBJSON(value){
  const normalize=v=>{
    if(Array.isArray(v))return v.map(normalize);
    if(v&&typeof v==='object'){
      const sorted=Object.create(null);
      for(const k of Object.keys(v).sort())sorted[k]=normalize(v[k]);
      return sorted;
    }
    return v;
  };
  return JSON.stringify(normalize(value));
}
const OWNER_KEY='browserSyncManagedIndexedDBV1';
export const JOURNAL_KEY='browserSyncIndexedDBRestoreJournal';

export function validateIndexedDBSnapshot(value){
  if(!value||typeof value!=='object'||Array.isArray(value)||
     value.schemaVersion!==1||!Array.isArray(value.databases)||
     value.databases.length>16)throw new Error('IndexedDB 快照格式无效');
  let count=0;
  const names=new Set();
  for(const db of value.databases){
    if(!db||typeof db.name!=='string'||db.name.length>256||names.has(db.name)||
       !Number.isSafeInteger(db.version)||db.version<1||db.version>2147483647||
       !Array.isArray(db.stores)||db.stores.length>64)
      throw new Error('IndexedDB 数据库元数据无效');
    names.add(db.name);
    const stores=new Set();
    for(const s of db.stores){
      if(!s||typeof s.name!=='string'||s.name.length>256||stores.has(s.name)||
         (s.keyPath!==null&&typeof s.keyPath!=='string'&&
          !(Array.isArray(s.keyPath)&&s.keyPath.every(k=>typeof k==='string')))||
         typeof s.autoIncrement!=='boolean'||!Array.isArray(s.indexes)||
         s.indexes.length>64||!Array.isArray(s.rows))
        throw new Error('IndexedDB Object Store 格式无效');
      stores.add(s.name);
      const indexes=new Set();
      for(const ix of s.indexes){
        if(!ix||typeof ix.name!=='string'||indexes.has(ix.name)||
           (typeof ix.keyPath!=='string'&&
             !(Array.isArray(ix.keyPath)&&ix.keyPath.every(k=>typeof k==='string')))||
           typeof ix.unique!=='boolean'||typeof ix.multiEntry!=='boolean')
          throw new Error('IndexedDB 索引格式无效');
        indexes.add(ix.name);
      }
      for(const row of s.rows){
        if(!row||!Array.isArray(row.key)||!Array.isArray(row.value))
          throw new Error('IndexedDB 记录编码无效');
        if(++count>4096)throw new Error('IndexedDB 超过 4096 条记录上限，未截断任何数据');
      }
    }
  }
  if(new TextEncoder().encode(JSON.stringify(value)).byteLength>MAX_INDEXEDDB_BYTES)
    throw new Error('完整 IndexedDB 超过 4 MiB 安全上限，未截断任何记录');
  return value;
}

// Executed by chrome.scripting, not by the extension service worker.
// IndexedDB is scoped to the current page origin. No MAIN-world scripts,
// postMessage bridges, or cross-origin iframe enumeration are required.
export async function siteIndexedDB(expectedOrigin,mode,target=null,expected=null,reportErrors=false){
  try {
  if(location.origin!==expectedOrigin)throw new Error('IndexedDB 站点导航已变化');
  if(typeof indexedDB.databases!=='function')
    throw new Error('当前 Chrome 不支持安全枚举 IndexedDB 数据库');
  const byteLimit=4*1024*1024;
  // An injected function is serialized independently; keep this helper
  // self-contained rather than capturing a module-scoped variable.
  const canonical=value=>{
    const normalize=v=>{
      if(Array.isArray(v))return v.map(normalize);
      if(v&&typeof v==='object'){
        const sorted=Object.create(null);
        for(const k of Object.keys(v).sort())sorted[k]=normalize(v[k]);
        return sorted;
      }
      return v;
    };
    return JSON.stringify(normalize(value));
  };
  const b64=a=>{
    const x=new Uint8Array(a),chunks=[];
    for(let i=0;i<x.length;i+=8192)chunks.push(String.fromCharCode(...x.subarray(i,i+8192)));
    return btoa(chunks.join('')).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/g,'');
  };
  const binary=s=>{
    if(typeof s!=='string'||s.length>byteLimit*2||!/^[A-Za-z0-9_-]*$/.test(s))
      throw new Error('IndexedDB 二进制编码无效');
    const raw=atob(s.replace(/-/g,'+').replace(/_/g,'/')+'='.repeat((4-s.length%4)%4));
    return Uint8Array.from(raw,c=>c.charCodeAt(0)).buffer;
  };
  const fail=()=>{throw new Error('IndexedDB 含无法完整复制的结构化数据类型，未写入任何数据');};
  const seen=new WeakSet();
  async function encode(v,depth=0){
    if(depth>24)fail();
    if(v===null)return ['0'];
    if(v===undefined)return ['u'];
    if(typeof v==='string')return ['s',v];
    if(typeof v==='boolean')return ['b',v];
    if(typeof v==='number')return ['n',String(v)];
    if(typeof v==='bigint')return ['i',v.toString()];
    if(typeof v!=='object'||seen.has(v))return fail();
    seen.add(v);
    try{
      if(v instanceof Date){
        const timestamp=v.getTime();
        if(!Number.isFinite(timestamp))return fail();
        return ['d',timestamp];
      }
      if(v instanceof RegExp)return ['r',v.source,v.flags];
      if(v instanceof ArrayBuffer)return ['B',b64(v)];
      if(ArrayBuffer.isView(v))
        return ['v',v.constructor.name,b64(v.buffer.slice(v.byteOffset,v.byteOffset+v.byteLength))];
      if(typeof Blob!=='undefined'&&v instanceof Blob){
        if(v.size>byteLimit)throw new Error('IndexedDB Blob 超过 4 MiB 安全上限');
        if(typeof File!=='undefined'&&v instanceof File)
          return ['f',v.name,v.type,v.lastModified,b64(await v.arrayBuffer())];
        return ['F',v.type,b64(await v.arrayBuffer())];
      }
      if(Array.isArray(v)){
        const rows=[];
        for(let i=0;i<v.length;i++)if(Object.hasOwn(v,i))
          rows.push([i,await encode(v[i],depth+1)]);
        return ['a',v.length,rows];
      }
      if(v instanceof Map){
        const entries=[];
        for(const [k,item] of v)entries.push([await encode(k,depth+1),await encode(item,depth+1)]);
        return ['m',entries];
      }
      if(v instanceof Set){
        const entries=[];
        for(const item of v)entries.push(await encode(item,depth+1));
        return ['t',entries];
      }
      const proto=Object.getPrototypeOf(v);
      if(proto!==Object.prototype&&proto!==null)return fail();
      const entries=[];
      for(const [k,item] of Object.entries(v))entries.push([k,await encode(item,depth+1)]);
      return ['o',entries];
    }finally{
      // Deliberately retain references within the snapshot. JSON cannot
      // represent a structured-clone object graph with shared identities or
      // cycles; refuse such records rather than silently changing semantics.
    }
  }
  function decode(e,depth=0){
    if(!Array.isArray(e)||depth>24)fail();
    switch(e[0]){
      case '0':return null;
      case 'u':return undefined;
      case 's':if(typeof e[1]!=='string')fail();return e[1];
      case 'b':if(typeof e[1]!=='boolean')fail();return e[1];
      case 'n':
        if(typeof e[1]!=='string'||!/^(-?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?|NaN|-?Infinity|-0)$/.test(e[1]))fail();
        return Number(e[1]);
      case 'i':if(typeof e[1]!=='string'||!(/^-?\d+$/).test(e[1]))fail();return BigInt(e[1]);
      case 'd':if(typeof e[1]!=='number')fail();return new Date(e[1]);
      case 'r':return new RegExp(e[1],e[2]);
      case 'B':return binary(e[1]);
      case 'v':{
        const types={Int8Array,Uint8Array,Uint8ClampedArray,Int16Array,Uint16Array,
          Int32Array,Uint32Array,Float32Array,Float64Array,DataView,
          ...(typeof BigInt64Array==='function'?{BigInt64Array}:{}),
          ...(typeof BigUint64Array==='function'?{BigUint64Array}:{})};
        if(!Object.hasOwn(types,e[1]))fail();
        return new types[e[1]](binary(e[2]));
      }
      case 'F':return new Blob([binary(e[2])],{type:e[1]});
      case 'f':return new File([binary(e[4])],e[1],{type:e[2],lastModified:e[3]});
      case 'a':{
        if(!Number.isSafeInteger(e[1])||e[1]<0||e[1]>4096||!Array.isArray(e[2]))fail();
        const arr=new Array(e[1]);
        for(const [i,item] of e[2]){
          if(!Number.isSafeInteger(i)||i<0||i>=arr.length||Object.hasOwn(arr,i))fail();
          arr[i]=decode(item,depth+1);
        }
        return arr;
      }
      case 'm':{
        if(!Array.isArray(e[1]))fail();
        return new Map(e[1].map(pair=>[decode(pair[0],depth+1),decode(pair[1],depth+1)]));
      }
      case 't':{
        if(!Array.isArray(e[1]))fail();
        return new Set(e[1].map(x=>decode(x,depth+1)));
      }
      case 'o':{
        if(!Array.isArray(e[1]))fail();
        const obj=Object.create(null);
        for(const [k,v] of e[1]){
          if(typeof k!=='string'||Object.hasOwn(obj,k))fail();
          obj[k]=decode(v,depth+1);
        }
        return obj;
      }
      default: return fail();
    }
  }
  async function open(name,version=null,create=null){
    return new Promise((resolve,reject)=>{
      const req=version===null?indexedDB.open(name):indexedDB.open(name,version);
      const timer=setTimeout(()=>reject(new Error('IndexedDB 数据库被其他标签页占用或打开超时')),7000);
      req.onblocked=()=>{ /* time out rather than downgrading version */ };
      req.onerror=()=>{clearTimeout(timer);reject(req.error||new Error('IndexedDB 打开失败'));};
      req.onupgradeneeded=()=>{
        if(!create){req.transaction.abort();return;}
        const db=req.result;
        for(const s of create.stores){
          const store=db.createObjectStore(s.name,{keyPath:s.keyPath,autoIncrement:s.autoIncrement});
          for(const ix of s.indexes)store.createIndex(ix.name,ix.keyPath,
            {unique:ix.unique,multiEntry:ix.multiEntry});
        }
      };
      req.onsuccess=()=>{clearTimeout(timer);resolve(req.result);};
    });
  }
  async function rows(db,storeName){
    return new Promise((resolve,reject)=>{
      const tx=db.transaction(storeName,'readonly');
      const all=[];
      const req=tx.objectStore(storeName).openCursor();
      req.onsuccess=()=>{
        const cur=req.result;
        if(!cur)return;
        if(all.length>=4096){tx.abort();return;}
        all.push({key:cur.key,value:cur.value});
        cur.continue();
      };
      tx.oncomplete=()=>resolve(all);
      tx.onerror=()=>reject(tx.error||new Error('IndexedDB 读取失败'));
      tx.onabort=()=>reject(tx.error||new Error('IndexedDB 读取中断'));
    });
  }
  async function read(){
    if(location.origin!==expectedOrigin)throw new Error('IndexedDB 站点已导航');
    const list=await indexedDB.databases();
    if(!Array.isArray(list)||list.length>16)throw new Error('IndexedDB 数据库数量超出安全上限');
    const databases=[];
    for(const info of list){
      if(typeof info.name!=='string'||!Number.isSafeInteger(info.version)||info.version<1)
        throw new Error('IndexedDB 元数据无效');
      const db=await open(info.name);
      try{
        const stores=[];
        for(const name of db.objectStoreNames){
          if(stores.length>=64)throw new Error('IndexedDB Object Store 超限');
          const tx=db.transaction(name,'readonly'),store=tx.objectStore(name);
          const indexes=Array.from(store.indexNames,x=>{
            const ix=store.index(x);
            return {name:ix.name,keyPath:ix.keyPath,unique:ix.unique,multiEntry:ix.multiEntry};
          });
          const raw=await rows(db,name),encoded=[];
          for(const row of raw)encoded.push({
            key:await encode(row.key),value:await encode(row.value)
          });
          stores.push({name,keyPath:store.keyPath,autoIncrement:store.autoIncrement,
            indexes,rows:encoded});
        }
        databases.push({name:db.name,version:db.version,stores});
      }finally{db.close();}
    }
    const out={schemaVersion:1,databases:databases.sort((a,b)=>a.name.localeCompare(b.name))};
    if(new TextEncoder().encode(JSON.stringify(out)).length>byteLimit)
      throw new Error('完整 IndexedDB 超过 4 MiB 上限，不会遗漏记录');
    return out;
  }
  function schema(db){return canonical(db.stores.map(s=>({
    name:s.name,keyPath:s.keyPath,autoIncrement:s.autoIncrement,indexes:s.indexes
  })));}
  async function write(current,next){
    const liveBeforeWrite=await read();
    if(canonical(liveBeforeWrite)!==canonical(current))
      throw new Error('CONFLICT: IndexedDB 在准备恢复后被网站修改' +
        ' (expected DBs='+current.databases.length+
        ', actual DBs='+liveBeforeWrite.databases.length+')');
    for(const oldDB of current.databases){
      const newDB=next.databases.find(d=>d.name===oldDB.name);
      if(newDB&&(oldDB.version!==newDB.version||schema(oldDB)!==schema(newDB)))
        throw new Error('CONFLICT: IndexedDB 数据库版本或索引结构不同');
    }
    // Validate every value before the first mutation.
    for(const db of next.databases){
      for(const store of db.stores)for(const row of store.rows){
        decode(row.key);decode(row.value);
      }
    }
    // Removing a newly created database is only used by an explicit
    // rollback, never to propagate arbitrary remote database deletion.
    for(const oldDB of current.databases){
      if(next.databases.some(d=>d.name===oldDB.name))continue;
      await new Promise((resolve,reject)=>{
        const req=indexedDB.deleteDatabase(oldDB.name);
        const timer=setTimeout(()=>reject(new Error('PARTIAL_ROLLBACK: IndexedDB 删除被阻止')),7000);
        req.onsuccess=()=>{clearTimeout(timer);resolve();};
        req.onerror=()=>{clearTimeout(timer);reject(req.error);};
      });
    }
    for(const targetDB of next.databases){
      const oldDB=current.databases.find(d=>d.name===targetDB.name);
      if(oldDB&&canonical(oldDB)===canonical(targetDB))continue;
      const db=await open(targetDB.name,targetDB.version,oldDB?null:targetDB);
      try{
        const names=targetDB.stores.map(s=>s.name);
        if(names.length===0)continue;
        await new Promise((resolve,reject)=>{
          const tx=db.transaction(names,'readwrite');
          tx.oncomplete=resolve;
          tx.onabort=()=>reject(tx.error||new Error('IndexedDB 写入失败'));
          tx.onerror=()=>reject(tx.error||new Error('IndexedDB 写入失败'));
          try{
            for(const s of targetDB.stores){
              const store=tx.objectStore(s.name);
              store.clear();
              for(const row of s.rows){
                const value=decode(row.value);
                if(s.keyPath===null)store.put(value,decode(row.key));
                else store.put(value);
              }
            }
          }catch(e){tx.abort();reject(e);}
        });
      }finally{db.close();}
    }
    return true;
  }
  if(mode==='read')return await read();
  if(mode==='validate'){
    if(!target||target.schemaVersion!==1||!Array.isArray(target.databases))fail();
    for(const db of target.databases)for(const s of db.stores)for(const row of s.rows){
      decode(row.key);decode(row.value);
    }
    return true;
  }
  if(mode==='write')return await write(expected,target);
  throw new Error('IndexedDB 操作模式无效');
  }catch(error){
    // Chromium may return a null InjectionResult for an injected script that
    // throws instead of rejecting scripting.executeScript. Report a bounded
    // error string explicitly; never return "success" on an unknown result.
    if(reportErrors)return {__relayIndexedDBError:String(error?.message||error).slice(0,256)};
    throw error;
  }
}

// ISOLATED-world content scripts share the page origin's actual IndexedDB
// storage, while their JS globals/prototypes are separate from page scripts.
// Do not execute sensitive restore logic in MAIN, where a malicious website
// can replace indexedDB.open/databases, Blob, Promise or other primitives.
// The website's Origin is checked again by siteIndexedDB before each action.
// The page can still mutate its own DB; preflight/write/readback checks remain.
async function executeSiteIndexedDB(tabId,args){
  const result=await chrome.scripting.executeScript({
    target:{tabId},world:'ISOLATED',func:siteIndexedDB,args:[...args,true]
  });
  if(result?.length!==1||!result[0]||!('result' in result[0])||result[0].result===null)
    throw new Error('页面 IndexedDB 脚本异常，未确认读写完成');
  if(result[0].result?.__relayIndexedDBError)
    throw new Error(result[0].result.__relayIndexedDBError);
  return result[0].result;
}

export async function captureIndexedDB(policy){
  const p=validatePolicy(policy);
  if(p.schemaVersion!==3)throw new Error('仅双方确认的 v3 站点配对支持 IndexedDB');
  const tabId=await tabFor(p.siteOrigin);
  return validateIndexedDBSnapshot(await executeSiteIndexedDB(tabId,[p.siteOrigin,'read']));
}

export async function prepareIndexedDBRestore(ruleId,data,policy,{allowOverwrite=false,tabId=null}={}){
  const p=validatePolicy(policy);
  if(p.schemaVersion!==3)throw new Error('仅双方确认的 v3 站点配对支持 IndexedDB');
  // Old Chrome senders included no IndexedDB; never treat a missing field
  // as an instruction to clear the receiver's databases.
  if(data===undefined)return {tabId,async apply(){},async commit(){},async rollback(){return true;}};
  const incoming=validateIndexedDBSnapshot(data);
  const id=tabId??await tabFor(p.siteOrigin,{create:true});
  await executeSiteIndexedDB(id,[p.siteOrigin,'validate',incoming]);
  const journal=(await chrome.storage.local.get(JOURNAL_KEY))[JOURNAL_KEY]||{};
  if(journal[ruleId])throw new Error('PARTIAL_ROLLBACK: IndexedDB 上次写入中断');
  const before=validateIndexedDBSnapshot(await executeSiteIndexedDB(id,[p.siteOrigin,'read']));
  const meta=(await chrome.storage.local.get(OWNER_KEY))[OWNER_KEY]||{};
  const owned=meta[ruleId]||{};
  const merged=new Map(before.databases.map(db=>[db.name,db]));
  for(const db of incoming.databases){
    const existing=merged.get(db.name);
    if(existing){
      const shape=x=>canonicalIndexedDBJSON(x.stores.map(s=>({
        name:s.name,keyPath:s.keyPath,autoIncrement:s.autoIncrement,indexes:s.indexes
      })));
      if(existing.version!==db.version||shape(existing)!==shape(db))
        throw new Error('CONFLICT: 接收端 IndexedDB 版本或索引结构不同');
      const same=canonicalIndexedDBJSON(existing)===canonicalIndexedDBJSON(db);
      const empty=existing.stores.every(s=>s.rows.length===0);
      const ownTag=await cookieValueTag(ruleId,'indexeddb:'+db.name,canonicalIndexedDBJSON(existing));
      if(!same&&!empty&&!allowOverwrite&&owned[db.name]!==ownTag)
        throw new Error('CONFLICT: 接收端 IndexedDB 有不同的本地账号数据');
    }
    merged.set(db.name,db);
  }
  // Never remove a database omitted by the sender: a website may use it
  // independently, and data deletion cannot be reliably inferred from a
  // snapshot that predates IndexedDB support.
  const after={schemaVersion:1,databases:[...merged.values()].sort((a,b)=>a.name.localeCompare(b.name))};
  validateIndexedDBSnapshot(after);
  const nextOwnership={...owned};
  for(const db of incoming.databases){
    nextOwnership[db.name]=await cookieValueTag(ruleId,'indexeddb:'+db.name,canonicalIndexedDBJSON(db));
  }
  let started=false,finished=false;
  return {
    tabId:id,
    async apply(){
      if(started)throw new Error('IndexedDB 事务不能重复应用');
      started=true;
      journal[ruleId]={startedAt:Date.now()};
      await chrome.storage.local.set({[JOURNAL_KEY]:journal});
      await executeSiteIndexedDB(id,[p.siteOrigin,'write',after,before]);
      // A successful chrome.scripting response alone cannot prove the
      // website can read the committed database. Verify the complete schema
      // and encoded records before allowing a terminal APPLIED acknowledgement.
      const verified=validateIndexedDBSnapshot(
        await executeSiteIndexedDB(id,[p.siteOrigin,'read']));
      if(canonicalIndexedDBJSON(verified)!==canonicalIndexedDBJSON(after))
        throw new Error('INDEXEDDB_FAILURE: IndexedDB 写入后校验失败，拒绝报告同步成功');
    },
    async commit(){
      if(!started||finished)throw new Error('IndexedDB 事务状态无效');
      const fresh=(await chrome.storage.local.get(JOURNAL_KEY))[JOURNAL_KEY]||{};
      delete fresh[ruleId];
      await chrome.storage.local.set({[OWNER_KEY]:{...meta,[ruleId]:nextOwnership},[JOURNAL_KEY]:fresh});
      finished=true;
    },
    async rollback(){
      if(!started||finished)return true;
      try{
        const live=validateIndexedDBSnapshot(await executeSiteIndexedDB(id,[p.siteOrigin,'read']));
        const beforeBy=new Map(before.databases.map(db=>[db.name,db]));
        const afterBy=new Map(after.databases.map(db=>[db.name,db]));
        for(const db of live.databases){
          const prev=beforeBy.get(db.name),next=afterBy.get(db.name);
          if(canonicalIndexedDBJSON(db)!==canonicalIndexedDBJSON(prev)&&canonicalIndexedDBJSON(db)!==canonicalIndexedDBJSON(next))
            return false;
        }
        await executeSiteIndexedDB(id,[p.siteOrigin,'write',before,live]);
        const fresh=(await chrome.storage.local.get(JOURNAL_KEY))[JOURNAL_KEY]||{};
        delete fresh[ruleId];
        await chrome.storage.local.set({[JOURNAL_KEY]:fresh});
        finished=true;
        return true;
      }catch{return false;}
    }
  };
}
