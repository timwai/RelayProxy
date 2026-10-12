import test from 'node:test';
import assert from 'node:assert/strict';
import {siteIndexedDB,validateIndexedDBSnapshot,MAX_INDEXEDDB_BYTES,captureIndexedDB,prepareIndexedDBRestore,canonicalIndexedDBJSON,executeSiteIndexedDB} from '../src/indexeddb-sync.js';

// Small IndexedDB simulator for the injected ISOLATED-world routine. It
// reproduces upgrade callbacks, cursor order, transaction completion and
// structured-clone boundaries; no page or MV3 service-worker globals leak.
function fakeIndexedDB(){
  const databases=new Map();
  function api(entry){
    return {
      name:entry.name,version:entry.version,
      get objectStoreNames(){return [...entry.stores.keys()].sort();},
      createObjectStore(name,{keyPath,autoIncrement}){
        if(entry.stores.has(name))throw Error('duplicate store');
        const store={name,keyPath,autoIncrement,indexes:new Map(),rows:new Map()};
        entry.stores.set(name,store);
        return {
          createIndex(name,keyPath,{unique,multiEntry}){
            store.indexes.set(name,{name,keyPath,unique,multiEntry});
          }
        };
      },
      transaction(name,mode){
        const names=Array.isArray(name)?name:[name];
        const tx={error:null,objectStore(key){
          if(!names.includes(key))throw Error('unauthorized store');
          const s=entry.stores.get(key);
          if(!s)throw Error('missing store '+key);
          return {
            name:s.name,keyPath:s.keyPath,autoIncrement:s.autoIncrement,
            get indexNames(){return [...s.indexes.keys()].sort();},
            index(id){return s.indexes.get(id);},
            openCursor(){
              const req={result:null},rows=[...s.rows.values()];
              let i=0;
              const next=()=>{
                const row=rows[i++];
                req.result=row?{key:structuredClone(row.key),value:structuredClone(row.value),
                  continue:()=>queueMicrotask(next)}:null;
                req.onsuccess?.();
                if(!row)setTimeout(()=>tx.oncomplete?.(),0);
              };
              queueMicrotask(next);
              return req;
            },
            clear(){s.rows.clear();},
            put(value,key){
              const clone=structuredClone(value);
              const primary=s.keyPath===null?key:
                typeof s.keyPath==='string'?clone[s.keyPath]:
                s.keyPath.map(k=>clone[k]);
              if(primary===undefined)throw Error('missing primary key');
              s.rows.set(JSON.stringify(primary),{key:structuredClone(primary),value:clone});
              setTimeout(()=>tx.oncomplete?.(),0);
            }
          };
        },abort(){queueMicrotask(()=>tx.onabort?.());}};
        return tx;
      },
      close(){}
    };
  }
  return {
    databases:async()=>[...databases.values()].map(e=>({name:e.name,version:e.version})),
    open(name,version){
      const req={result:null,transaction:{abort(){req.aborted=true;}}};
      queueMicrotask(()=>{
        let entry=databases.get(name);
        if(!entry){
          entry={name,version:version??1,stores:new Map()};
          databases.set(name,entry);
          req.result=api(entry);
          req.onupgradeneeded?.();
          if(req.aborted){databases.delete(name);req.onerror?.();return;}
        }else req.result=api(entry);
        req.onsuccess?.();
      });
      return req;
    },
    deleteDatabase(name){
      const req={};queueMicrotask(()=>{databases.delete(name);req.onsuccess?.();});
      return req;
    }
  };
}
const origin='https://example.com';
const blank={schemaVersion:1,databases:[]};
function use(factory){globalThis.indexedDB=factory;globalThis.location={origin};}

test('capture empty origin and restore all stores, keys, indexes and typed values',async()=>{
  const source=fakeIndexedDB();use(source);
  const db=await new Promise((resolve,reject)=>{
    const q=source.open('app-auth',2);
    q.onupgradeneeded=()=>{
      const s=q.result.createObjectStore('tokens',{keyPath:null,autoIncrement:false});
      s.createIndex('by_account','account',{unique:false,multiEntry:false});
      q.result.createObjectStore('secondary',{keyPath:'id',autoIncrement:false});
    };
    q.onsuccess=()=>resolve(q.result);
    q.onerror=()=>reject(q.error);
  });
  db.transaction('tokens','readwrite').objectStore('tokens').put({
    account:'Alice',token:'indexeddb-token',when:new Date('2026-10-11T08:00:00Z'),
    counter:123n,binary:new Uint8Array([0,255,7]),
    extras:new Map([['auth',new Set([1,2,3])]]),
    arr:[undefined,null,'ok']
  },'user:alice');
  db.transaction('secondary','readwrite').objectStore('secondary')
    .put({id:3,enabled:true});
  db.close();
  const captured=validateIndexedDBSnapshot(await siteIndexedDB(origin,'read'));
  assert.equal(captured.databases[0].name,'app-auth');
  assert.equal(captured.databases[0].version,2);
  const auth=captured.databases[0].stores.find(s=>s.name==='tokens');
  const secondary=captured.databases[0].stores.find(s=>s.name==='secondary');
  assert.equal(auth.indexes[0].name,'by_account');
  assert.equal(auth.rows.length,1);
  assert.equal(secondary.rows.length,1);
  assert.match(JSON.stringify(captured),/indexeddb-token/);
  const target=fakeIndexedDB();use(target);
  assert.deepEqual(await siteIndexedDB(origin,'read'),blank);
  assert.equal(await siteIndexedDB(origin,'write',captured,blank),true);
  assert.deepEqual(await siteIndexedDB(origin,'read'),captured);
  // A rolled-back newly created database must be removed, not abandoned.
  assert.equal(await siteIndexedDB(origin,'write',blank,captured),true);
  assert.deepEqual(await siteIndexedDB(origin,'read'),blank);
});

test('site-level optimistic check refuses concurrent IndexedDB writes',async()=>{
  const source=fakeIndexedDB();use(source);
  const snapshot={schemaVersion:1,databases:[{
    name:'login',version:1,stores:[{name:'auth',keyPath:null,
      autoIncrement:false,indexes:[],rows:[{key:['s','k'],value:['s','secret']}]}]
  }]};
  await siteIndexedDB(origin,'write',snapshot,blank);
  await assert.rejects(siteIndexedDB(origin,'write',blank,blank),/CONFLICT/);
  assert.deepEqual(await siteIndexedDB(origin,'read'),snapshot);
});

test('IndexedDB rejects oversized payloads, invalid structure and unsafe types',async()=>{
  assert.throws(()=>validateIndexedDBSnapshot({schemaVersion:1,databases:[{
    name:'db',version:1,stores:[{name:'records',keyPath:null,autoIncrement:false,
      indexes:[],rows:[{key:['s','a'],value:['s','x'.repeat(MAX_INDEXEDDB_BYTES)]}]}]
  }]}),/4 MiB/);
  assert.throws(()=>validateIndexedDBSnapshot({schemaVersion:1,databases:[
    {name:'dup',version:1,stores:[]},{name:'dup',version:1,stores:[]}
  ]}),/无效/);
  use(fakeIndexedDB());
  await assert.rejects(siteIndexedDB(origin,'validate',{
    schemaVersion:1,databases:[{name:'danger',version:1,stores:[{
      name:'bad',keyPath:null,autoIncrement:false,indexes:[],
      rows:[{key:['s','x'],value:['o',[['__proto__',['q','unsafe']]]]}]
    }]}]
  }),/无法完整复制/);
  assert.deepEqual(await siteIndexedDB(origin,'read'),blank);
});

test('navigation to an unpaired origin prevents both reads and writes',async()=>{
  use(fakeIndexedDB());
  globalThis.location={origin:'https://unexpected.test'};
  await assert.rejects(siteIndexedDB(origin,'read'),/导航/);
  await assert.rejects(siteIndexedDB(origin,'write',blank,blank),/导航/);
});

test('unsupported invalid Date and shared-object graphs are rejected before transmission',async()=>{
  const factory=fakeIndexedDB();use(factory);
  const db=await new Promise((resolve,reject)=>{
    const q=factory.open('bad-graph',1);
    q.onupgradeneeded=()=>q.result.createObjectStore('items',{keyPath:null,autoIncrement:false});
    q.onsuccess=()=>resolve(q.result);
    q.onerror=()=>reject(q.error);
  });
  const store=db.transaction('items','readwrite').objectStore('items');
  store.put({when:new Date(NaN)},'invalid-date');
  db.close();
  await assert.rejects(siteIndexedDB(origin,'read'),/无法完整复制/);
});

test('real capture/restore entry points accept explicit v3 pairing, never old v2',async()=>{
  use(fakeIndexedDB());
  const oldChrome=globalThis.chrome;
  const v3={siteOrigin:origin,schemaVersion:3,allCookies:true,
    localStorage:true,sessionStorage:true,indexedDB:true};
  const v2={siteOrigin:origin,schemaVersion:2,allCookies:true,
    localStorage:true,sessionStorage:true};
  globalThis.chrome={
    tabs:{
      async query(){return [{id:17,url:origin+'/'}];},
      async get(id){return {id,url:origin+'/',incognito:false};}
    },
    scripting:{async executeScript({world,func,args}){
      assert.equal(world,'ISOLATED',
        'IndexedDB injection may not execute in hostile page MAIN world');
      return [{result:await func(...args)}];
    }}
  };
  try{
    assert.deepEqual(await captureIndexedDB(v3),{schemaVersion:1,databases:[]});
    const noop=await prepareIndexedDBRestore('test-rule',undefined,v3);
    await noop.apply();
    await noop.commit();
    assert.equal(await noop.rollback(),true);
    await assert.rejects(captureIndexedDB(v2),/v3/);
    await assert.rejects(prepareIndexedDBRestore('test-rule',undefined,v2),/v3/);
  }finally{globalThis.chrome=oldChrome;}
});

test('IndexedDB object-key ordering across Chrome scripting boundaries never causes false conflict',()=>{
  const snapshot={schemaVersion:1,databases:[{
    name:'auth',version:1,stores:[{
      name:'tokens',keyPath:null,autoIncrement:false,indexes:[],
      rows:[{key:['s','id'],value:['o',[['account',['s','alice']]]]}]
    }]
  }]};
  const ipcReordered={
    databases:[{stores:[{rows:[{value:['o',[['account',['s','alice']]]],key:['s','id']}],
      indexes:[],autoIncrement:false,keyPath:null,name:'tokens'}],
      version:1,name:'auth'}],schemaVersion:1
  };
  assert.equal(canonicalIndexedDBJSON(snapshot),canonicalIndexedDBJSON(ipcReordered));
  const changed=structuredClone(ipcReordered);
  changed.databases[0].stores[0].rows[0].value[1][0][1][1]='bob';
  assert.notEqual(canonicalIndexedDBJSON(snapshot),canonicalIndexedDBJSON(changed));
});

test('Chrome injected IndexedDB errors are returned explicitly instead of silent null success',async()=>{
  use(fakeIndexedDB());
  const blank={schemaVersion:1,databases:[]};
  const snapshot={schemaVersion:1,databases:[{
    name:'fixture',version:1,stores:[{name:'records',keyPath:null,
      autoIncrement:false,indexes:[],rows:[{key:['s','id'],value:['s','value']}]}]
  }]};
  await siteIndexedDB(origin,'write',snapshot,blank);
  await assert.rejects(siteIndexedDB(origin,'write',snapshot,blank),/CONFLICT/);
  const reported=await siteIndexedDB(origin,'write',snapshot,blank,true);
  assert.match(reported.__relayIndexedDBError,/CONFLICT/);
  assert.deepEqual(await siteIndexedDB(origin,'read'),snapshot);
});

test('missing injected READ result retries once in ISOLATED top frame without mutating data',async()=>{
  const old=globalThis.chrome,seen=[];
  globalThis.chrome={
    tabs:{async get(){return {url:origin+'/',status:'complete'};}},
    scripting:{async executeScript({world,target,args}){
      assert.equal(world,'ISOLATED');
      assert.deepEqual(target,{tabId:17,frameIds:[0]});
      seen.push(args[1]);
      return [{frameId:0,result:seen.length===1?null:blank}];
    }}
  };
  try{
    assert.deepEqual(await executeSiteIndexedDB(17,[origin,'read']),blank);
    assert.deepEqual(seen,['read','read']);
  }finally{globalThis.chrome=old;}
});

test('unknown WRITE result is accepted only after independent full readback, never replayed',async()=>{
  const old=globalThis.chrome,seen=[];
  const written={schemaVersion:1,databases:[{
    name:'accounts',version:1,stores:[{
      name:'sessions',keyPath:null,autoIncrement:false,indexes:[],
      rows:[{key:['s','primary'],value:['s','opaque']}]
    }]
  }]};
  globalThis.chrome={
    tabs:{async get(){return {url:origin+'/account',status:'complete'};}},
    scripting:{async executeScript({world,args}){
      assert.equal(world,'ISOLATED');
      seen.push(args[1]);
      if(args[1]==='write')return [{frameId:0,result:null}];
      return [{frameId:0,result:written}];
    }}
  };
  try{
    assert.equal(await executeSiteIndexedDB(17,[origin,'write',written,blank]),true);
    assert.deepEqual(seen,['write','read']);
  }finally{globalThis.chrome=old;}
});

test('unknown WRITE result never replays or claims success when readback differs',async()=>{
  const old=globalThis.chrome,seen=[];
  globalThis.chrome={
    tabs:{async get(){return {url:origin+'/',status:'complete'};}},
    scripting:{async executeScript({args}){
      seen.push(args[1]);
      return [{frameId:0,result:args[1]==='write'?null:blank}];
    }}
  };
  try{
    const expected={schemaVersion:1,databases:[{name:'new-db',version:1,stores:[]}]};
    await assert.rejects(executeSiteIndexedDB(17,[origin,'write',expected,blank]),
      /PARTIAL_ROLLBACK/);
    assert.deepEqual(seen,['write','read']);
  }finally{globalThis.chrome=old;}
});

test('missing injection result refuses navigation and foreign frames without retry',async()=>{
  const old=globalThis.chrome;
  let called=0;
  globalThis.chrome={
    tabs:{async get(){return {url:'https://changed.example/'};}},
    scripting:{async executeScript(){called++;return [{frameId:0,result:null}];}}
  };
  try{
    await assert.rejects(executeSiteIndexedDB(17,[origin,'read']),/页面已跳转/);
    assert.equal(called,1);
    globalThis.chrome.scripting.executeScript=async()=>[{frameId:3,result:blank}];
    await assert.rejects(executeSiteIndexedDB(17,[origin,'read']),/不是顶层页面/);
  }finally{globalThis.chrome=old;}
});

test('persistent null read result stays failed closed after bounded retry',async()=>{
  const old=globalThis.chrome;
  let called=0;
  globalThis.chrome={
    tabs:{async get(){return {url:origin+'/'};}},
    scripting:{async executeScript(){called++;return [{frameId:0,result:null}];}}
  };
  try{
    await assert.rejects(executeSiteIndexedDB(17,[origin,'read']),/未返回结果/);
    assert.equal(called,2);
  }finally{globalThis.chrome=old;}
});
