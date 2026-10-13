import test from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';
import {getOrCreateIdentity} from '../src/device-identity.js';
import {encryptSnapshot,decryptSnapshot} from '../src/session-envelope.js';
import {captureAllCookies,applyAllCookies} from '../src/cookies-all.js';
import {captureWebStorage,prepareWebStorageRestore} from '../src/web-storage.js';

globalThis.crypto ||= webcrypto;
const policy={siteOrigin:'https://example.com',schemaVersion:2,allCookies:true,
  localStorage:true,sessionStorage:true};
function profile(origin='https://example.com'){
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
      async getAll({url,domain,partitionKey}){
        return [...jar.values()].filter(c=>(partitionKey!==undefined || !c.partitionKey) && (domain?
          c.domain.replace(/^\./,'')===domain :
          new URL(url).hostname===c.domain.replace(/^\./,'')&&new URL(url).pathname.startsWith(c.path)));
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
      async query(){return [{id:10,url:origin+'/',status:'complete',incognito:false}];},
      async get(){return {id:10,url:origin+'/',status:'complete',incognito:false};}
    },
    scripting:{async executeScript({func,args}){
      const old={location:globalThis.location,localStorage:globalThis.localStorage,
        sessionStorage:globalThis.sessionStorage};
      function store(map){return {
        get length(){return map.size;},key(i){return [...map.keys()][i]??null;},
        getItem(k){return map.get(k)??null;},setItem(k,v){map.set(k,v);},removeItem(k){map.delete(k);}
      };}
      globalThis.location={origin};
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

test('partitioned cookies are included for the authorized top-level site',async()=>{
  const source=profile();use(source);
  await source.chrome.cookies.set({url:'https://example.com/',name:'CHIPS',value:'ok',
    path:'/',secure:true,httpOnly:true,partitionKey:{topLevelSite:'https://example.com',hasCrossSiteAncestor:false}});
  await source.chrome.cookies.set({url:'https://example.com/',name:'CHIPS',value:'different-site',
    path:'/',secure:true,httpOnly:true,partitionKey:{topLevelSite:'https://outside.test',hasCrossSiteAncestor:true}});
  const snap=await captureAllCookies(policy);
  assert.equal(snap.cookies.length,1);
  assert.equal(snap.cookies[0].value,'ok');
  const target=profile();use(target);
  await applyAllCookies('partition',snap,policy);
  assert.equal((await captureAllCookies(policy)).cookies[0].value,'ok');
});

test('v2 A to B end-to-end encrypted snapshot restores Cookie and both Storage kinds',async()=>{
  const a=profile(),b=profile();
  use(a);
  const source=await getOrCreateIdentity();
  a.page.local.set('auth-key','local-secret-abc');
  a.page.session.set('tab-auth','session-secret-xyz');
  await a.chrome.cookies.set({url:'https://example.com/',name:'login',value:'cookie-secret',
    path:'/account',secure:true,httpOnly:true});
  const payload={...await captureAllCookies(policy),storage:await captureWebStorage(policy)};
  use(b);
  const target=await getOrCreateIdentity();
  use(a);
  const rule={ruleId:crypto.randomUUID(),status:'active',
    sourceBrowserDeviceId:source.deviceId,targetBrowserDeviceId:target.deviceId};
  const env=await encryptSnapshot(rule,{...target,id:target.deviceId},1,payload);
  for(const secret of ['cookie-secret','local-secret-abc','session-secret-xyz']){
    assert.equal(JSON.stringify(env).includes(secret),false);
  }
  use(b);
  const clear=await decryptSnapshot(env,{...source,id:source.deviceId});
  assert.deepEqual(clear,JSON.parse(JSON.stringify(payload)));
  const txn=await prepareWebStorageRestore(rule.ruleId,clear.storage,policy);
  await txn.apply();
  await applyAllCookies(rule.ruleId,clear,policy);
  await txn.commit();
  assert.equal(b.page.local.get('auth-key'),'local-secret-abc');
  assert.equal(b.page.session.get('tab-auth'),'session-secret-xyz');
  assert.equal((await captureAllCookies(policy)).cookies.find(c=>c.name==='login').path,'/account');
  const corrupted={...env,ciphertext:(env.ciphertext[0]==='A'?'B':'A')+env.ciphertext.slice(1)};
  await assert.rejects(decryptSnapshot(corrupted,{...source,id:source.deviceId}),/签名/);
});

test('v2 remote logout cannot erase Storage values changed by destination',async()=>{
  const p=profile();use(p);
  const first={local:{token:'remote-account'},session:{csrf:'remote-csrf'}};
  const initial=await prepareWebStorageRestore('logout',first,policy);
  await initial.apply();await initial.commit();
  p.page.local.set('token','new-local-account');
  await assert.rejects(prepareWebStorageRestore('logout',
    {local:{},session:{}},policy),/CONFLICT/);
  assert.equal(p.page.local.get('token'),'new-local-account');
  assert.equal(p.page.session.get('csrf'),'remote-csrf');
});

test('HTTP site v2 synchronizes non-Secure Cookies and all Local/Session Storage keys',async()=>{
  const httpPolicy={...policy,siteOrigin:'http://example.com'};
  const a=profile(httpPolicy.siteOrigin),b=profile(httpPolicy.siteOrigin);use(a);
  a.page.local.set('app-token','HTTP-local-token');
  a.page.session.set('csrf','HTTP-tab-token');
  await a.chrome.cookies.set({url:'http://example.com/',name:'plain',value:'http-session',
    path:'/user',secure:false,httpOnly:true,sameSite:'lax'});
  // Domain enumerations can also return HTTPS-only credentials for this host.
  // They must not be exported into a downgraded HTTP-origin invitation.
  await a.chrome.cookies.set({url:'https://example.com/',name:'https-only',
    value:'secret-https',path:'/',secure:true,httpOnly:true});
  const captured=await captureAllCookies(httpPolicy);
  assert.deepEqual(captured.cookies.map(x=>x.name),['plain']);
  assert.equal(captured.cookies[0].secure,false);
  const storage=await captureWebStorage(httpPolicy);
  assert.equal(storage.local['app-token'],'HTTP-local-token');
  assert.equal(storage.session.csrf,'HTTP-tab-token');
  use(b);
  const txn=await prepareWebStorageRestore('http-rule',storage,httpPolicy);
  await txn.apply();
  await applyAllCookies('http-rule',captured,httpPolicy);
  await txn.commit();
  assert.equal((await captureAllCookies(httpPolicy)).cookies[0].value,'http-session');
  assert.equal(b.page.local.get('app-token'),'HTTP-local-token');
  assert.equal(b.page.session.get('csrf'),'HTTP-tab-token');
});

test('HTTP snapshots reject injected Secure, partitioned and SameSite=None cookies',async()=>{
  const httpPolicy={...policy,siteOrigin:'http://example.com'};
  const p=profile(httpPolicy.siteOrigin);use(p);
  const baseline={schemaVersion:2,siteOrigin:httpPolicy.siteOrigin,
    cookies:[{name:'sid',value:'v',domain:'example.com',hostOnly:true,
      path:'/',secure:false,httpOnly:true,sameSite:'lax',session:true}]};
  for(const override of [
    {secure:true},{sameSite:'no_restriction'},
    {partitionKey:{topLevelSite:'https://example.com'}}
  ]){
    const malicious={...baseline,cookies:[{...baseline.cookies[0],...override}]};
    await assert.rejects(applyAllCookies('http-invalid',malicious,httpPolicy),/无效/);
  }
  assert.equal((await captureAllCookies(httpPolicy)).cookies.length,0);
});

test('large 700 KiB Storage snapshot is encrypted, relayed and restored without dropping any Key',async()=>{
  const a=profile(),b=profile();
  use(a);
  const source=await getOrCreateIdentity();
  a.page.local.set('large-auth', 'A'.repeat(420*1024));
  a.page.session.set('large-tab','B'.repeat(280*1024));
  a.page.local.set('last-key','last-key-must-survive');
  const payload={...await captureAllCookies(policy),storage:await captureWebStorage(policy)};
  use(b);
  const target=await getOrCreateIdentity();
  use(a);
  const rule={ruleId:crypto.randomUUID(),status:'active',
    sourceBrowserDeviceId:source.deviceId,targetBrowserDeviceId:target.deviceId};
  const envelope=await encryptSnapshot(rule,{...target,id:target.deviceId},1,payload);
  assert.ok(envelope.ciphertext.length<1024*1024*4/3);
  assert.equal(JSON.stringify(envelope).includes('last-key-must-survive'),false);
  use(b);
  const decrypted=await decryptSnapshot(envelope,{...source,id:source.deviceId});
  assert.equal(decrypted.storage.local['large-auth'].length,420*1024);
  assert.equal(decrypted.storage.session['large-tab'].length,280*1024);
  const transaction=await prepareWebStorageRestore(rule.ruleId,decrypted.storage,policy);
  await transaction.apply();
  await transaction.commit();
  assert.equal(b.page.local.get('large-auth').length,420*1024);
  assert.equal(b.page.session.get('large-tab').length,280*1024);
  assert.equal(b.page.local.get('last-key'),'last-key-must-survive');
});

test('v4 only Local Storage leaves Cookie and Session Storage unchanged on receiver',async()=>{
  const selected={siteOrigin:'https://example.com',schemaVersion:4,
    allCookies:false,localStorage:true,sessionStorage:false,indexedDB:false};
  const a=profile();use(a);
  a.page.local.set('token','source-local');
  a.page.session.set('token','source-private-session');
  await a.chrome.cookies.set({url:selected.siteOrigin+'/',name:'sid',value:'source-cookie',
    path:'/',secure:true,httpOnly:true});
  const snapshot=await captureWebStorage(selected);
  assert.deepEqual({...snapshot.local},{token:'source-local'});
  assert.deepEqual({...snapshot.session},{});
  const b=profile();use(b);
  b.page.session.set('token','receiver-private-session');
  await b.chrome.cookies.set({url:selected.siteOrigin+'/',name:'sid',value:'receiver-cookie',
    path:'/',secure:true,httpOnly:true});
  const txn=await prepareWebStorageRestore('select-local',snapshot,selected);
  await txn.apply();await txn.commit();
  assert.equal(b.page.local.get('token'),'source-local');
  assert.equal(b.page.session.get('token'),'receiver-private-session');
  const live=await b.chrome.cookies.getAll({url:selected.siteOrigin+'/'});
  assert.equal(live.find(c=>c.name==='sid').value,'receiver-cookie');
  await assert.rejects(prepareWebStorageRestore('select-local',
    {local:{token:'source-local'},session:{token:'injected'}},selected),/未授权/);
});

test('v4 only Session Storage does not even read Local Storage from page',async()=>{
  const selected={siteOrigin:'https://example.com',schemaVersion:4,
    allCookies:false,localStorage:false,sessionStorage:true,indexedDB:false};
  const a=profile();use(a);
  a.page.local.set('secret-key','not-allowed');
  a.page.session.set('tab','selected');
  const snapshot=await captureWebStorage(selected);
  assert.deepEqual({...snapshot.local},{});
  assert.deepEqual({...snapshot.session},{tab:'selected'});
  const b=profile();use(b);
  b.page.local.set('secret-key','receiver-only');
  const txn=await prepareWebStorageRestore('select-session',snapshot,selected);
  await txn.apply();await txn.commit();
  assert.equal(b.page.local.get('secret-key'),'receiver-only');
  assert.equal(b.page.session.get('tab'),'selected');
});
