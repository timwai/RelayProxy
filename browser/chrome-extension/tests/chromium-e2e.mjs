// Real Chromium/MV3 profile integration — separate persistent browser profiles,
// a local owned test site, real Chrome Cookie/scripting/Storage/IndexedDB APIs,
// and actual per-profile WebCrypto keys. The fixture deliberately bypasses the
// Server WSS transport (covered separately by Go integration tests).
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {mkdtemp,cp,readFile,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {chromium} from 'playwright';

const extensionRoot=dirname(dirname(fileURLToPath(import.meta.url)));
const scope=[];
let server=null;

function listenLocalServer(){
  return new Promise((resolve,reject)=>{
    const instance=createServer((req,res)=>{
      const url=new URL(req.url,'http://127.0.0.1');
      res.setHeader('Cache-Control','no-store');
      if(url.pathname==='/login'){
        const name=url.searchParams.get('user')||'alice';
        if(!['alice','bob'].includes(name)){res.writeHead(400).end();return;}
        res.setHeader('Set-Cookie',`rp_e2e_session=${name}; Path=/; HttpOnly; SameSite=Lax`);
      }
      if(url.pathname==='/whoami'){
        const value=(req.headers.cookie||'').match(/(?:^|;\s*)rp_e2e_session=([^;]+)/)?.[1]||'anonymous';
        res.setHeader('Content-Type','application/json');
        res.end(JSON.stringify({user:value}));
        return;
      }
      res.setHeader('Content-Type','text/html; charset=utf-8');
      res.end('<!doctype html><meta charset="utf-8"><title>RelayProxy owned test site</title><h1>RelayProxy E2E</h1>');
    });
    instance.once('error',reject);
    instance.listen(0,'127.0.0.1',()=>resolve(instance));
  });
}

async function extensionContext(path, profileDir){
  const context=await chromium.launchPersistentContext(profileDir,{
    channel:'chromium',headless:true,
    args:[
      '--disable-extensions-except='+path,
      '--load-extension='+path,
      '--no-first-run','--no-default-browser-check'
    ]
  });
  scope.push(()=>context.close());
  let worker=context.serviceWorkers()[0];
  if(!worker)worker=await context.waitForEvent('serviceworker',{timeout:30000});
  const extensionId=new URL(worker.url()).host;
  assert.match(extensionId,/^[a-p]{32}$/);
  const options=await context.newPage();
  await options.goto(`chrome-extension://${extensionId}/src/options.html`);
  await options.locator('#server-section').waitFor();
  return {context,options,extensionId};
}

async function extensionIdentity(profile){
  return profile.options.evaluate(async()=>{
    const {getOrCreateIdentity}=await import(chrome.runtime.getURL('src/device-identity.js'));
    return getOrCreateIdentity();
  });
}

async function sourceSiteState(page,origin){
  await page.goto(origin+'/login?user=alice');
  await page.evaluate(async()=>{
    localStorage.setItem('rp_e2e_access','alice-local-token');
    localStorage.setItem('rp_e2e_second','second-key');
    sessionStorage.setItem('rp_e2e_tab','alice-session-token');
    await new Promise((resolve,reject)=>{
      const open=indexedDB.open('rp-e2e-auth',1);
      open.onupgradeneeded=()=>{
        const db=open.result;
        const store=db.createObjectStore('accounts',{keyPath:'id'});
        store.createIndex('by_user','user',{unique:true});
      };
      open.onerror=()=>reject(open.error);
      open.onsuccess=()=>{
        const db=open.result;
        const tx=db.transaction('accounts','readwrite');
        tx.objectStore('accounts').put({
          id:'primary',user:'alice',token:'alice-indexeddb-token',
          blob:new Blob(['blob-secret']),lastLogin:new Date('2026-10-12T00:00:00Z')
        });
        tx.oncomplete=()=>{db.close();resolve();};
        tx.onerror=()=>{db.close();reject(tx.error);};
      };
    });
  });
}

async function chromiumCapture(profile,policy){
  return profile.options.evaluate(async policy=>{
    const [{captureAllCookies},{captureWebStorage},{captureIndexedDB}]=await Promise.all([
      import(chrome.runtime.getURL('src/cookies-all.js')),
      import(chrome.runtime.getURL('src/web-storage.js')),
      import(chrome.runtime.getURL('src/indexeddb-sync.js'))
    ]);
    return {...await captureAllCookies(policy),
      storage:await captureWebStorage(policy),
      indexedDB:await captureIndexedDB(policy)};
  },policy);
}

async function encryptInSource(profile,rule,target,payload){
  return profile.options.evaluate(async({rule,target,payload})=>{
    const {encryptSnapshot}=await import(chrome.runtime.getURL('src/session-envelope.js'));
    return encryptSnapshot(rule,target,1,payload);
  },{rule,target,payload});
}

async function decryptInTarget(profile,env,source){
  return profile.options.evaluate(async({env,source})=>{
    const {decryptSnapshot}=await import(chrome.runtime.getURL('src/session-envelope.js'));
    return decryptSnapshot(env,source);
  },{env,source});
}

async function applyToTarget(profile,ruleId,policy,payload){
  return profile.options.evaluate(async({ruleId,policy,payload})=>{
    const [{prepareWebStorageRestore},{prepareIndexedDBRestore},{applyAllCookies}]=await Promise.all([
      import(chrome.runtime.getURL('src/web-storage.js')),
      import(chrome.runtime.getURL('src/indexeddb-sync.js')),
      import(chrome.runtime.getURL('src/cookies-all.js'))
    ]);
    const storage=await prepareWebStorageRestore(ruleId,payload.storage,policy);
    const indexedDB=await prepareIndexedDBRestore(ruleId,payload.indexedDB,policy,{tabId:storage.tabId});
    const schema=async()=>{
      const {captureIndexedDB}=await import(chrome.runtime.getURL('src/indexeddb-sync.js'));
      const data=await captureIndexedDB(policy);
      return data.databases.map(db=>({name:db.name,stores:db.stores.map(s=>s.name)}));
    };
    const stages=[];
    try{
      stages.push({step:'payload',hasIndexedDB:payload.indexedDB!==undefined,
        count:payload.indexedDB?.databases?.length,
        stores:payload.indexedDB?.databases?.map(db=>db.stores.map(s=>s.name))});
      stages.push({step:'before',db:await schema()});
      await indexedDB.apply();
      const journal=(await chrome.storage.local.get('browserSyncIndexedDBRestoreJournal')).browserSyncIndexedDBRestoreJournal;
      stages.push({step:'after-indexeddb',db:await schema(),hasJournal:!!journal?.[ruleId]});
      if(stages.at(-1).db.length===0)
        throw new Error('IndexedDB restore reported success without creating the website database');
      await storage.apply();
      stages.push({step:'after-storage',db:await schema()});
      await applyAllCookies(ruleId,payload,policy);
      stages.push({step:'after-cookies',db:await schema()});
      await storage.commit();
      await indexedDB.commit();
      stages.push({step:'after-commit',db:await schema()});
      return {status:'APPLIED',stages};
    }catch(err){
      const storageRecovered=await storage.rollback().catch(()=>false);
      const indexedDBRecovered=await indexedDB.rollback().catch(()=>false);
      throw new Error(`restore failed: ${err.message}; stages=${JSON.stringify(stages)}; rolled back: ${storageRecovered}/${indexedDBRecovered}`);
    }
  },{ruleId,policy,payload});
}

async function verifyTargetSite(page,origin){
  assert.equal(new URL(page.url()).origin,origin);
  await page.waitForLoadState('domcontentloaded');
  // Do not reload the tab immediately after injected sessionStorage/IDB writes:
  // the goal is to verify this exact site's real browser storage context.
  assert.deepEqual(await page.evaluate(async()=>{
    const res=await fetch('/whoami');
    return res.json();
  }),{user:'alice'},'the WEBSITE must accept the migrated HTTP-only Cookie');
  const state=await page.evaluate(async()=>{
    const record=await new Promise((resolve,reject)=>{
      const events=[];
      const timer=setTimeout(()=>reject(new Error('IndexedDB page read timed out: '+events.join(','))),9000);
      const ok=value=>{clearTimeout(timer);resolve(value);};
      const fail=error=>{clearTimeout(timer);reject(error);};
      const open=indexedDB.open('rp-e2e-auth');
      open.onblocked=()=>events.push('open-blocked');
      open.onerror=()=>fail(open.error||new Error('open failed'));
      open.onsuccess=()=>{
        events.push('opened');
        const db=open.result;
        let tx;
        try{
          tx=db.transaction('accounts','readonly');
          const get=tx.objectStore('accounts').get('primary');
          get.onsuccess=()=>{const row=get.result;db.close();ok({
            id:row.id,user:row.user,token:row.token,
            blob:row.blob instanceof Blob,
            validDate:row.lastLogin instanceof Date
          });};
          get.onerror=()=>{db.close();fail(get.error||new Error('get failed'));};
          tx.onabort=()=>{db.close();fail(tx.error||new Error('transaction aborted'));};
        }catch(e){db.close();fail(e);}
      };
    });
    return {local:localStorage.getItem('rp_e2e_access'),
      second:localStorage.getItem('rp_e2e_second'),
      session:sessionStorage.getItem('rp_e2e_tab'),record};
  });
  assert.equal(state.local,'alice-local-token');
  assert.equal(state.second,'second-key');
  assert.equal(state.session,'alice-session-token');
  assert.deepEqual(state.record,{id:'primary',user:'alice',token:'alice-indexeddb-token',
    blob:true,validDate:true});
}

async function main(){
  const temp=await mkdtemp(join(tmpdir(),'relayproxy-chromium-e2e-'));
  scope.push(()=>rm(temp,{recursive:true,force:true}));
  server=await listenLocalServer();
  scope.push(()=>new Promise(resolve=>server.close(resolve)));
  const origin=`http://127.0.0.1:${server.address().port}`;
  const extPath=join(temp,'extension');
  // The production manifest deliberately requests host permissions only
  // through a user gesture. Give ONLY our loopback fixture Origin a temporary
  // install-time grant in this throwaway test copy. Never alter the shipped MV3
  // manifest or bypass Server/website approval in production.
  await cp(extensionRoot,extPath,{recursive:true,filter:src=>{
    const rel=src.slice(extensionRoot.length).replaceAll('\\','/');
    return !rel.split('/').some(part=>['node_modules','tests','.git'].includes(part));
  }});
  const manifestFile=join(extPath,'manifest.json');
  const manifest=JSON.parse(await readFile(manifestFile,'utf8'));
  manifest.host_permissions=['http://127.0.0.1/*'];
  await writeFile(manifestFile,JSON.stringify(manifest,null,2));
  const a=await extensionContext(extPath,join(temp,'chrome-a'));
  const b=await extensionContext(extPath,join(temp,'chrome-b'));
  const [idA,idB]=await Promise.all([extensionIdentity(a),extensionIdentity(b)]);
  assert.notEqual(idA.deviceId,idB.deviceId,'profiles may never share identity keys');
  const policy={schemaVersion:3,siteOrigin:origin,allCookies:true,
    localStorage:true,sessionStorage:true,indexedDB:true};
  const pageA=await a.context.newPage();
  const pageB=await b.context.newPage();
  await sourceSiteState(pageA,origin);
  await pageB.goto(origin+'/');
  assert.deepEqual(await pageB.evaluate(async()=>(await(await fetch('/whoami')).json())),{user:'anonymous'});
  const payload=await chromiumCapture(a,policy);
  assert.equal(payload.cookies.some(c=>c.name==='rp_e2e_session'&&c.httpOnly&&c.value==='alice'),true,
    'the source Chrome API must collect HTTP-only session Cookies');
  const sourceDB=payload.indexedDB.databases.find(db=>db.name==='rp-e2e-auth');
  assert.ok(sourceDB,'the real website IndexedDB must be visible from the extension');
  assert.deepEqual(sourceDB.stores.map(x=>x.name),['accounts'],
    'source IndexedDB must include the real website Object Store, not only DB metadata');
  assert.equal(payload.indexedDB.databases.some(db=>db.name==='rp-e2e-auth'),true);
  const ruleId=crypto.randomUUID();
  const rule={ruleId,status:'active',sourceBrowserDeviceId:idA.deviceId,targetBrowserDeviceId:idB.deviceId};
  const target={id:idB.deviceId,encryptionPublicKey:idB.encryptionPublicKey};
  const source={id:idA.deviceId,signingPublicKey:idA.signingPublicKey};
  const envelope=await encryptInSource(a,rule,target,payload);
  const restored=await decryptInTarget(b,envelope,source);
  assert.deepEqual(restored,payload,'two real profile-local WebCrypto keys must decrypt identical site snapshot');
  const rejected=structuredClone(envelope);
  rejected.ciphertext=rejected.ciphertext.replace(/^./,v=>v==='A'?'B':'A');
  await assert.rejects(decryptInTarget(b,rejected,source),/签名|decryption|decrypt/i,
    'tampered Chrome encrypted envelope must be rejected');
  const applied=await applyToTarget(b,ruleId,policy,restored);
  console.log('IndexedDB restore phases (schema only):',JSON.stringify(applied.stages));
  assert.equal(applied.status,'APPLIED');
  const [extView,pageView]=await Promise.all([
    b.options.evaluate(async policy=>{
      const {captureIndexedDB}=await import(chrome.runtime.getURL('src/indexeddb-sync.js'));
      const result=await captureIndexedDB(policy);
      return result.databases.map(db=>({name:db.name,stores:db.stores.map(s=>s.name)}));
    },policy),
    pageB.evaluate(async()=>indexedDB.databases())
  ]);
  console.log('IndexedDB post-restore schemas (no values):',JSON.stringify({extView,pageView}));
  await verifyTargetSite(pageB,origin);
  // A separate login on B must not be silently overwritten by an old A
  // snapshot. This checks actual browser Cookie ownership/HMAC integration.
  await pageB.goto(origin+'/login?user=bob');
  await assert.rejects(b.options.evaluate(async({ruleId,policy,payload})=>{
    const {applyAllCookies}=await import(chrome.runtime.getURL('src/cookies-all.js'));
    return applyAllCookies(ruleId,payload,policy);
  },{ruleId,policy,payload:restored}),/CONFLICT/);
  assert.deepEqual(await pageB.evaluate(async()=>(await(await fetch('/whoami')).json())),
    {user:'bob'},'new independent B account must stay logged in after refused overwrite');
  console.log('Chromium A/B integration passed: two real profiles, MV3 permissions, HttpOnly login,');
  console.log('all local/session storage, IndexedDB v3 Blob/Date, E2EE, tamper + account conflict.');
}

let error;
try{await main();}catch(e){error=e;console.error(e);}finally{
  for(const cleanup of scope.reverse()){
    try{await cleanup();}catch(e){console.error('cleanup:',e);}
  }
}
if(error)process.exitCode=1;
