import test from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';
import {getOrCreateIdentity} from '../src/device-identity.js';
import {encryptSnapshot,decryptSnapshot} from '../src/session-envelope.js';
import {captureAllCookies,applyAllCookies} from '../src/cookies-all.js';
import {cookieAccessOrigins} from '../src/cookie-access.js';
import {captureWebStorage,prepareWebStorageRestore} from '../src/web-storage.js';
import {failureDiagnostic} from '../src/sync-diagnostics.js';

globalThis.crypto ||= webcrypto;
const policy={siteOrigin:'https://example.com',schemaVersion:2,allCookies:true,
  localStorage:true,sessionStorage:true};
function profile(origin='https://example.com',granted=null){
  const state=new Map(),jar=new Map(),secrets=new Map();
  const page={local:new Map(),session:new Map()};
  const idb={createObjectStore(){},transaction(){
    let outstanding=0;
    const tx={objectStore(){return {
      get(k){return operation(()=>secrets.get(k));},
      put(v,k){return operation(()=>secrets.set(k,v));},
      delete(k){return operation(()=>secrets.delete(k));}
    }}};
    function operation(run){
      const q={};
      outstanding++;
      queueMicrotask(()=>{
        try{q.result=run();q.onsuccess?.();}
        catch(error){q.error=error;q.onerror?.();}
        finally{
          outstanding--;
          queueMicrotask(()=>{if(outstanding===0)tx.oncomplete?.();});
        }
      });
      return q;
    }
    return tx;
  },close(){}};
  const cookieKey=c=>JSON.stringify([c.domain,c.path,c.name,!!c.hostOnly,c.partitionKey?.topLevelSite||'']);
  const api={
    permissions:{async contains({origins}){return !granted||origins.every(o=>granted.has(o));}},
    storage:{local:{
      async get(k){if(Array.isArray(k))return Object.fromEntries(k.map(x=>[x,state.get(x)]));return {[k]:state.get(k)};},
      async set(o){for(const [k,v] of Object.entries(o))state.set(k,v);}
    }},
    cookies:{
      async getAll({url,domain,partitionKey}){
        return [...jar.values()].filter(c=>{
          const host=c.domain.replace(/^\./,'');
          if(granted&&!granted.has((c.secure?'https:':'http:')+'//'+host+'/*'))return false;
          return (partitionKey!==undefined || !c.partitionKey) && (domain?
          (host===domain||host.endsWith('.'+domain)) :
          new URL(url).hostname===c.domain.replace(/^\./,'')&&new URL(url).pathname.startsWith(c.path));
        });
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
      function store(map){return Object.create({
        get length(){return map.size;},key(i){return [...map.keys()][i]??null;},
        getItem(k){return map.get(k)??null;},setItem(k,v){map.set(k,v);},removeItem(k){map.delete(k);}
      });}
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
test('HTTPS IP SESSION without Secure needs HTTP read permission and supports explicit overwrite',async()=>{
  const origin='https://10.99.32.130:30009',selected={...policy,siteOrigin:origin};
  const granted=new Set(['https://10.99.32.130/*']);
  const a=profile(origin,granted);use(a);
  await a.chrome.cookies.set({url:origin+'/',name:'SESSION',value:'source-session',
    path:'/',secure:false,httpOnly:true,sameSite:'lax'});
  assert.deepEqual(await a.chrome.cookies.getAll({domain:'10.99.32.130',partitionKey:{}}),[],
    'Chrome filters non-Secure cookies against HTTP permissions even on HTTPS sites');
  await assert.rejects(captureAllCookies(selected),/Cookie 访问权限不完整/);
  for(const pattern of cookieAccessOrigins(origin))granted.add(pattern);
  const snapshot=await captureAllCookies(selected);
  assert.equal(snapshot.cookies.length,1);
  assert.equal(snapshot.cookies[0].secure,false);
  const targetGrants=new Set(['https://10.99.32.130/*']);
  const b=profile(origin,targetGrants);use(b);
  await b.chrome.cookies.set({url:origin+'/',name:'SESSION',value:'target-session',
    path:'/',secure:false,httpOnly:true,sameSite:'lax'});
  await assert.rejects(applyAllCookies('ip-session',snapshot,selected,{allowOverwrite:true}),/Cookie 访问权限不完整/);
  assert.equal([...b.jar.values()][0].value,'target-session');
  for(const pattern of cookieAccessOrigins(origin))targetGrants.add(pattern);
  await assert.rejects(applyAllCookies('ip-session',snapshot,selected),/CONFLICT/);
  const result=await applyAllCookies('ip-session',snapshot,selected,{allowOverwrite:true});
  assert.equal(result.status,'APPLIED');
  assert.equal((await captureAllCookies(selected)).cookies[0].value,'source-session');
});

test('parent-domain permissions include shared SESSION but exclude unrelated host-only and sibling cookies',async()=>{
  const origin='https://app.example.com',selected={...policy,siteOrigin:origin};
  const granted=new Set(['https://app.example.com/*','http://app.example.com/*']);
  const a=profile(origin,granted);use(a);
  for(const [name,domain] of [['SESSION','example.com'],['sibling','other.example.com']])
    await a.chrome.cookies.set({url:origin+'/',name,domain,value:'test',
      path:'/',secure:false,httpOnly:true});
  await a.chrome.cookies.set({url:'http://example.com/',name:'parent-only',value:'private',
    path:'/',secure:false,httpOnly:true});
  await assert.rejects(captureAllCookies(selected),/Cookie 访问权限不完整/);
  for(const pattern of cookieAccessOrigins(origin))granted.add(pattern);
  const snapshot=await captureAllCookies(selected);
  assert.deepEqual(snapshot.cookies.map(c=>c.name),['SESSION']);
});

test('same-value cookies still restore Secure HttpOnly SameSite and expiry attributes',async()=>{
  const source=profile();use(source);
  await source.chrome.cookies.set({url:policy.siteOrigin+'/',name:'SESSION',value:'same-value',
    path:'/',secure:true,httpOnly:true,sameSite:'strict',expirationDate:Date.now()/1000+3600});
  const snapshot=await captureAllCookies(policy);
  const target=profile();use(target);
  await target.chrome.cookies.set({url:policy.siteOrigin+'/',name:'SESSION',value:'same-value',
    path:'/',secure:false,httpOnly:false,sameSite:'lax'});
  await assert.rejects(applyAllCookies('attrs',snapshot,policy),/CONFLICT/);
  await applyAllCookies('attrs',snapshot,policy,{allowOverwrite:true});
  assert.deepEqual(await captureAllCookies(policy),snapshot);
});
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

test('Storage missing write reply cannot commit silently or repeat a write',async()=>{
 const b=profile();use(b);
 const original=b.chrome.scripting.executeScript;
 let writes=0;
 b.chrome.scripting.executeScript=async request=>{
  if(request.func.name==='mutate'){
   writes++;
   await original(request);
   return [{result:undefined}];
  }
  return original(request);
 };
 const txn=await prepareWebStorageRestore('lost-reply',{local:{token:'source'},session:{}},policy);
 await assert.rejects(txn.apply(),/Storage 读取\/写入失败/);
 assert.equal(writes,1,'ambiguous write must not be retried');
 assert.ok((await b.chrome.storage.local.get('browserSyncStorageRestoreJournal')).browserSyncStorageRestoreJournal['lost-reply']);
});

test('Storage detects partial successful-looking writes and rolls back every changed key',async()=>{
 const b=profile();use(b);b.page.local.set('one','before-one');b.page.local.set('two','before-two');
 const original=b.chrome.scripting.executeScript;
 let first=true;
 b.chrome.scripting.executeScript=async request=>{
  if(first&&request.func.name==='mutate'){
   first=false;
   const args=[...request.args];args[1]=args[1].slice(1);
   return original({...request,args});
  }
  return original(request);
 };
 const txn=await prepareWebStorageRestore('partial',
  {local:{one:'after-one',two:'after-two'},session:{tab:'session'}},policy,{allowOverwrite:true});
 await assert.rejects(txn.apply(),/Storage 写入后键值不一致/);
 assert.equal(await txn.rollback(),true);
 assert.deepEqual(Object.fromEntries(b.page.local),{one:'before-one',two:'before-two'});
 assert.equal(b.page.session.size,0);
 assert.equal((await b.chrome.storage.local.get('browserSyncStorageRestoreJournal')).browserSyncStorageRestoreJournal.partial,undefined);
});

test('Storage cannot commit values changed to undefined or removed by website after apply',async()=>{
 for(const action of ['undefined','remove']){
  const b=profile();use(b);
  const txn=await prepareWebStorageRestore('changed',
   {local:{token:'source-token'},session:{tab:'source-tab'}},policy);
  await txn.apply();
  if(action==='undefined')b.page.local.set('token','undefined');
  else b.page.session.delete('tab');
  await assert.rejects(txn.commit(),/Storage 写入后键值不一致/);
  assert.equal(await txn.rollback(),false,'concurrent website data must not be overwritten during rollback');
 }
});

test('Storage input rejects actual undefined without converting it to the literal string',async()=>{
 const b=profile();use(b);
 await assert.rejects(prepareWebStorageRestore('bad-value',
  {local:{token:undefined},session:{}},policy),/Key\/Value/);
 assert.equal(b.page.local.size,0);
});

test('Storage preflight refuses website mutation before any write even with overwrite consent',async()=>{
 const b=profile();use(b);
 const txn=await prepareWebStorageRestore('preflight',
  {local:{token:'source-token'},session:{}},policy,{allowOverwrite:true});
 b.page.local.set('token','new-website-login');
 await assert.rejects(txn.apply(),/CONFLICT/);
 assert.equal(b.page.local.get('token'),'new-website-login');
 assert.equal(await txn.rollback(),true,'a rejected preflight made no changes to undo');
 assert.equal(b.page.local.get('token'),'new-website-login');
 assert.equal((await b.chrome.storage.local.get('browserSyncStorageRestoreJournal')).browserSyncStorageRestoreJournal.preflight,undefined);
});

test('Storage preserves quota failure reason and rolls back without pausing an unchanged page',async()=>{
 const b=profile();use(b);
 const original=b.chrome.scripting.executeScript;
 b.chrome.scripting.executeScript=async request=>{
  if(request.func.name==='mutate')return [{result:{__relayStorageError:'STORAGE_QUOTA',applied:0}}];
  return original(request);
 };
 const txn=await prepareWebStorageRestore('quota',{local:{token:'source-token'},session:{}},policy);
 await assert.rejects(txn.apply(),error=>{
  assert.deepEqual(failureDiagnostic(error,'apply_storage'),
   {errorCode:'STORAGE_FAILURE',stage:'apply_storage',reason:'STORAGE_QUOTA'});
  return true;
 });
 b.page.local.set('unrelated','website-value');
 assert.equal(await txn.rollback(),true);
 assert.deepEqual([...b.page.local],[['unrelated','website-value']]);
});

test('Storage capture retries bounded unstable reads without dropping keys',async()=>{
 const b=profile();use(b);
 const original=b.chrome.scripting.executeScript;
 let reads=0;
 b.chrome.scripting.executeScript=async request=>{
  if(request.func.name==='readBoth')b.page.session.set('counter',String(++reads));
  return original(request);
 };
 await assert.rejects(captureWebStorage(policy),/采集期间持续变化/);
 assert.equal(reads,3);
});

test('Storage transaction never applies to a replacement document in the same tab',async()=>{
 const b=profile();use(b);
 const original=b.chrome.scripting.executeScript;
 let documentId='first-document';
 b.chrome.scripting.executeScript=async request=>{
  if(request.target.documentIds&&!request.target.documentIds.includes(documentId))
   throw new Error('document was replaced');
  return (await original(request)).map(reply=>({...reply,frameId:0,documentId}));
 };
 const txn=await prepareWebStorageRestore('navigation',
  {local:{token:'source-token'},session:{}},policy);
 documentId='new-document';
 await assert.rejects(txn.apply(),/document was replaced/);
 assert.equal(b.page.local.size,0);
});
