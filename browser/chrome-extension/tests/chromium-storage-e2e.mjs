// Real per-tab storage, exact string values and reserved Storage property names.
import assert from 'node:assert/strict';
import {mkdtemp,cp,readFile,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {scope,extensionRoot,listenLocalServer,extensionContext} from './chromium-e2e.mjs';

async function main(){
 const temp=await mkdtemp(join(tmpdir(),'relayproxy-storage-'));
 scope.push(()=>rm(temp,{recursive:true,force:true}));
 const server=await listenLocalServer();scope.push(()=>new Promise(resolve=>server.close(resolve)));
 const origin='http://127.0.0.1:'+server.address().port;
 const extPath=join(temp,'extension');
 await cp(extensionRoot,extPath,{recursive:true,filter:path=>!path.slice(extensionRoot.length).split('/').some(p=>['node_modules','tests','.git'].includes(p))});
 const manifest=JSON.parse(await readFile(join(extPath,'manifest.json'),'utf8'));
 manifest.host_permissions=['http://127.0.0.1/*'];
 await writeFile(join(extPath,'manifest.json'),JSON.stringify(manifest));
 const a=await extensionContext(extPath,join(temp,'profile-a'));
 const b=await extensionContext(extPath,join(temp,'profile-b'));
 const stale=await a.context.newPage();await stale.goto(origin+'/old');
 await stale.evaluate(()=>sessionStorage.setItem('only-old-tab','stale'));
 const source=await a.context.newPage();await source.goto(origin+'/account');
 const expected=await source.evaluate(()=>{
  const data={local:{},session:{}};
  for(let i=0;i<300;i++){
   data.local['setting-'+i]=JSON.stringify({index:i,text:'多行\nUnicode😀',nested:{text:'undefined is text'},items:[null,false,0,'']});
   data.session['context-'+i]='session-'+i;
  }
  for(const key of ['key','getItem','setItem','removeItem','clear','length','__proto__','constructor','toString','hasOwnProperty','', 'empty']){
   Object.defineProperty(data.local,key,{value:'local-'+key,enumerable:true,configurable:true});
   Object.defineProperty(data.session,key,{value:'session-'+key,enumerable:true,configurable:true});
  }
  data.local.empty='';data.session.empty='';
  for(const kind of ['local','session'])for(const [key,value] of Object.entries(data[kind]))
   Storage.prototype.setItem.call(kind==='local'?localStorage:sessionStorage,key,value);
  return data;
 });
 await source.bringToFront();await a.options.bringToFront();
 const policy={schemaVersion:4,siteOrigin:origin,allCookies:false,localStorage:true,sessionStorage:true,indexedDB:false};
 const capture=p=>p.options.evaluate(async policy=>{
  const {captureWebStorage}=await import(chrome.runtime.getURL('src/web-storage.js'));
  return captureWebStorage(policy);
 },policy);
 const snapshot=await capture(a);
 assert.deepEqual(snapshot,expected,'capture must preserve every key/value from the recently used tab');
 const target=await b.context.newPage();await target.goto(origin+'/account');
 await b.options.evaluate(async({policy,snapshot})=>{
  const {prepareWebStorageRestore}=await import(chrome.runtime.getURL('src/web-storage.js'));
  const tx=await prepareWebStorageRestore('storage-real',snapshot,policy,{allowOverwrite:true});
  await tx.apply();await tx.commit();
  globalThis.__storageFixtureTransaction=tx;
 },{policy,snapshot});
 const actual=await target.evaluate(()=>{
  const read=s=>Object.fromEntries(Array.from({length:Object.getOwnPropertyDescriptor(Storage.prototype,'length').get.call(s)},(_,i)=>{
   const key=Storage.prototype.key.call(s,i);return [key,Storage.prototype.getItem.call(s,key)];
  }));
  return {local:read(localStorage),session:read(sessionStorage)};
 });
 assert.deepEqual(actual,expected,'target website must see byte-identical complete Storage');
 await b.options.evaluate(async({origin,tabId})=>{
  const {reloadRestoredWebsiteTab}=await import(chrome.runtime.getURL('src/session-tabs.js'));
  await reloadRestoredWebsiteTab(tabId,origin);
  await globalThis.__storageFixtureTransaction.verifySiteState();
 },{origin,tabId:await b.options.evaluate(async origin=>{
  const {tabFor}=await import(chrome.runtime.getURL('src/web-storage.js'));return tabFor(origin);
 },origin)});
 assert.deepEqual(await capture(b),expected,'storage survives reload in the same tab');
 // A page can itself write JS undefined during initialization. Report that
 // mutation instead of claiming the earlier verified restore is still intact.
 await target.addInitScript(()=>Storage.prototype.setItem.call(localStorage,'setting-0',undefined));
 await target.reload();
 await assert.rejects(b.options.evaluate(()=>globalThis.__storageFixtureTransaction.verifySiteState()),/被目标网站脚本修改/);
 assert.equal(await target.evaluate(()=>Storage.prototype.getItem.call(localStorage,'setting-0')),'undefined');
 // A real native QuotaExceededError must survive Chrome scripting IPC and
 // rollback wrappers; do not lose it to a null InjectionResult or pause a
 // page on which the rejected first write changed nothing.
 await target.evaluate(()=>Storage.prototype.setItem.call(localStorage,'quota-filler','x'.repeat(3*1024*1024)));
 const quota=await b.options.evaluate(async policy=>{
  const {prepareWebStorageRestore}=await import(chrome.runtime.getURL('src/web-storage.js'));
  const {failureDiagnostic}=await import(chrome.runtime.getURL('src/sync-diagnostics.js'));
  const tx=await prepareWebStorageRestore('quota-real',
   {local:{'quota-fixture':'x'.repeat(3*1024*1024)},session:{}},
   {...policy,sessionStorage:false},{allowOverwrite:true});
  try{await tx.apply();throw new Error('native quota fixture did not fail');}
  catch(error){
   const diagnostic=failureDiagnostic(error,'apply_storage');
   return {diagnostic,rolledBack:await tx.rollback()};
  }
 },policy);
 assert.deepEqual(quota,{diagnostic:{errorCode:'STORAGE_FAILURE',stage:'apply_storage',reason:'STORAGE_QUOTA'},rolledBack:true});
 assert.equal(await target.evaluate(()=>Storage.prototype.getItem.call(localStorage,'quota-fixture')),null);
 console.log('Chromium Storage regression passed: correct recent source tab; 300+ Local/Session keys, reserved property names and exact string values survive restore and reload.');
}
let error;
try{await main();}catch(e){error=e;console.error(e);}finally{
 for(const cleanup of scope.reverse()){try{await cleanup();}catch(e){console.error('cleanup:',e);}}
}
if(error)process.exitCode=1;
