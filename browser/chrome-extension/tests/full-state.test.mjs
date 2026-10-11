import test from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';
import {captureAllCookies,applyAllCookies} from '../src/cookies-all.js';
import {captureWebStorage,prepareWebStorageRestore} from '../src/web-storage.js';

globalThis.crypto ||= webcrypto;
const policy={siteOrigin:'https://example.com',schemaVersion:2,allCookies:true,
  localStorage:true,sessionStorage:true};
function profile(){
  const state=new Map(),jar=new Map(),secrets=new Map();
  const page={local:new Map(),session:new Map()};
  const idb={createObjectStore(){},transaction(){
    const tx={objectStore(){return {
      get(k){const q={};queueMicrotask(()=>{q.result=secrets.get(k);q.onsuccess?.();});return q;},
      put(v,k){secrets.set(k,v);queueMicrotask(()=>tx.oncomplete?.());}
    }}};
    return tx;
  },close(){}};
  const cookieKey=c=>JSON.stringify([c.domain,c.path,c.name,!!c.hostOnly,c.partitionKey?.topLevelSite||'']);
  const api={
    storage:{local:{
      async get(k){if(Array.isArray(k))return Object.fromEntries(k.map(x=>[x,state.get(x)]));return {[k]:state.get(k)};},
      async set(o){for(const [k,v] of Object.entries(o))state.set(k,v);}
    }},
    cookies:{
      async getAll({url,domain}){
        return [...jar.values()].filter(c=>domain?
          c.domain.replace(/^\./,'')===domain :
          new URL(url).hostname===c.domain.replace(/^\./,'')&&new URL(url).pathname.startsWith(c.path));
      },
      async set(d){
        const c={name:d.name,value:d.value,domain:d.domain||new URL(d.url).hostname,
          hostOnly:!d.domain,path:d.path,secure:d.secure,httpOnly:d.httpOnly,
          sameSite:d.sameSite||'unspecified',session:!('expirationDate' in d),
          ...(d.partitionKey?{partitionKey:d.partitionKey}:{}),
          ...(d.expirationDate?{expirationDate:d.expirationDate}:{})};
        jar.set(cookieKey(c),c);return c;
      },
      async remove(d){
        const old=[...jar].find(([,c])=>c.name===d.name&&
          new URL(d.url).pathname.startsWith(c.path) &&
          (!d.partitionKey||c.partitionKey?.topLevelSite===d.partitionKey.topLevelSite));
        if(!old)return null;jar.delete(old[0]);return {name:d.name,url:d.url};
      }
    },
    tabs:{
      async query(){return [{id:10,url:'https://example.com/',status:'complete',incognito:false}];},
      async get(){return {id:10,url:'https://example.com/',status:'complete',incognito:false};}
    },
    scripting:{async executeScript({func,args}){
      const old={location:globalThis.location,localStorage:globalThis.localStorage,
        sessionStorage:globalThis.sessionStorage};
      function store(map){return {
        get length(){return map.size;},key(i){return [...map.keys()][i]??null;},
        getItem(k){return map.get(k)??null;},setItem(k,v){map.set(k,v);},removeItem(k){map.delete(k);}
      };}
      globalThis.location={origin:'https://example.com'};
      globalThis.localStorage=store(page.local);
      globalThis.sessionStorage=store(page.session);
      try{return [{result:func(...args)}];}
      finally{Object.assign(globalThis,old);}
    }}
  };
  return {chrome:api,jar,page,indexedDB:{open(){
    const q={result:idb};queueMicrotask(()=>{q.onupgradeneeded?.();q.onsuccess?.();});return q;
  }}};
}
function use(p){globalThis.chrome=p.chrome;globalThis.indexedDB=p.indexedDB;}
test('v2 captures every path and domain Cookie without listing names',async()=>{
  const a=profile(),b=profile();use(a);
  await a.chrome.cookies.set({url:'https://example.com',name:'root',value:'1',
    path:'/',secure:true,httpOnly:true});
  await a.chrome.cookies.set({url:'https://example.com',name:'auth',value:'2',
    path:'/login',secure:true,httpOnly:true,domain:'example.com'});
  const snapshot=await captureAllCookies(policy);
  assert.equal(snapshot.cookies.length,2);
  assert.equal(snapshot.cookies.find(c=>c.name==='auth').path,'/login');
  use(b);
  assert.equal((await applyAllCookies('r',snapshot,policy)).status,'APPLIED');
  assert.equal((await captureAllCookies(policy)).cookies.length,2);
  const conflicting=profile();use(conflicting);
  await conflicting.chrome.cookies.set({url:'https://example.com',name:'root',
    value:'separate-account',path:'/',secure:true,httpOnly:true});
  await assert.rejects(applyAllCookies('r',snapshot,policy),/CONFLICT/);
});
test('v2 Storage captures every key and blocks unapproved account overwrite',async()=>{
  const a=profile();use(a);
  a.page.local.set('token','secret');
  a.page.local.set('theme','dark');
  a.page.session.set('tab-session','value');
  const snapshot=await captureWebStorage(policy);
  assert.deepEqual({...snapshot.local},{token:'secret',theme:'dark'});
  assert.deepEqual({...snapshot.session},{'tab-session':'value'});
  const b=profile();use(b);
  b.page.local.set('token','other-user');
  await assert.rejects(prepareWebStorageRestore('r',snapshot,policy),/CONFLICT/);
  const txn=await prepareWebStorageRestore('r',snapshot,policy,{allowOverwrite:true});
  await txn.apply();
  await txn.commit();
  assert.equal(b.page.local.get('token'),'secret');
  assert.equal(b.page.session.get('tab-session'),'value');
  assert.equal(Object.keys((await b.chrome.storage.local.get('browserSyncManagedWebStorageV2'))
    .browserSyncManagedWebStorageV2.r.local).length,2);
});
