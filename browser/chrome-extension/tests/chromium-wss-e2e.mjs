// End-to-end CI with real RelayProxy production Browser Sync HTTPS/WSS
// Handler, actual MV3 service workers, and two distinct Chromium profiles.
// The only non-production control is a bearer-guarded, loopback admin fixture.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {mkdtemp,cp,readFile,writeFile,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {setTimeout as sleep} from 'node:timers/promises';
import {
  scope,extensionRoot,listenLocalServer,extensionContext,extensionIdentity,
  sourceSiteState,verifyTargetSite
} from './chromium-e2e.mjs';

const fixtureExecutable=process.env.RELAY_BROWSER_WSS_FIXTURE;
if(!fixtureExecutable)throw new Error('RELAY_BROWSER_WSS_FIXTURE must name the compiled test-only Go binary');

async function launchFixture(){
  const token=randomBytes(32).toString('hex');
  const child=spawn(fixtureExecutable,[],{
    env:{...process.env,RELAY_BROWSER_WSS_FIXTURE_TOKEN:token},
    stdio:['ignore','pipe','pipe']
  });
  scope.push(async()=>{
    if(child.exitCode!==null)return;
    child.kill('SIGTERM');
    await Promise.race([new Promise(resolve=>child.once('exit',resolve)),sleep(3000)]);
    if(child.exitCode===null)child.kill('SIGKILL');
  });
  const info=await new Promise((resolve,reject)=>{
    let output='';
    const timer=setTimeout(()=>reject(new Error('Relay fixture did not report loopback startup')),20000);
    const success=x=>{clearTimeout(timer);resolve(x);};
    const failure=e=>{clearTimeout(timer);reject(e);};
    child.once('error',failure);
    child.once('exit',code=>failure(new Error('Relay fixture exited before readiness: '+code)));
    child.stdout.on('data',chunk=>{
      output+=chunk.toString('utf8');
      if(output.length>4096)return failure(new Error('Unexpected fixture startup output'));
      const i=output.indexOf('\n');
      if(i<0)return;
      try{success(JSON.parse(output.slice(0,i)));}
      catch(e){failure(e);}
    });
    child.stderr.on('data',chunk=>{
      // Never log HTTP bodies, browser keys or registration payloads.
      if(/^(fatal error|panic:)/m.test(chunk.toString('utf8')))
        failure(new Error('Test-only Relay WSS fixture panic'));
    });
  });
  for(const item of [info.serverOrigin,info.proxyOrigin,info.adminOrigin])
    assert.equal(new URL(item).hostname,'127.0.0.1','test WSS fixture must bind loopback only');
  return {token,...info};
}

async function deviceDecision(fixture,action,id,send=false,receive=false){
  const reply=await fetch(new URL('/__fixture/device',fixture.adminOrigin),{
    method:'POST',headers:{'Content-Type':'application/json',
      Authorization:'Bearer '+fixture.token},
    body:JSON.stringify({action,id,send,receive})
  });
  assert.equal(reply.status,204,'fixture device action '+action+' was rejected');
}

async function diagnosticCategories(fixture){
  const response=await fetch(new URL('/__fixture/results',fixture.adminOrigin),{
    headers:{Authorization:'Bearer '+fixture.token},cache:'no-store'
  });
  assert.equal(response.status,200,'test-only diagnostic endpoint denied');
  const rows=await response.json();
  return rows.map(({outcome,errorCode})=>({outcome,errorCode}));
}

async function workerCommand(profile,message){
  const value=await profile.options.evaluate(async message=>
    chrome.runtime.sendMessage(message),message);
  assert.equal(value?.ok,true,`MV3 ${message.type}: ${value?.error||'no response'}`);
  return value;
}

async function setServerAndSite(profile,serverOrigin,siteOrigin,name){
  const saved=await profile.options.evaluate(async({serverOrigin,siteOrigin,name})=>{
    const {updateSettings}=await import(chrome.runtime.getURL('src/rules.js'));
    return updateSettings({
      serverOrigin,sites:[siteOrigin],deviceName:name,requestedIdentityId:'id_1'
    });
  },{serverOrigin,siteOrigin,name});
  assert.equal(saved.serverOrigin,serverOrigin);
}

async function eventually(label,run,{timeout=30000}={}){
  const until=Date.now()+timeout;
  let last;
  while(Date.now()<until){
    try{
      const result=await run();
      if(result)return result;
    }catch(e){last=e;}
    await sleep(200);
  }
  throw new Error(label+' timed out'+(last?': '+String(last.message||last):''));
}

async function main(){
  const temp=await mkdtemp(join(tmpdir(),'relay-chromium-wss-'));
  scope.push(()=>rm(temp,{recursive:true,force:true}));
  const webSite=await listenLocalServer();
  scope.push(()=>new Promise(resolve=>webSite.close(resolve)));
  const siteOrigin=`http://127.0.0.1:${webSite.address().port}`;
  const fixture=await launchFixture();
  const throughProxy=process.env.RELAY_BROWSER_WSS_USE_PROXY==='1';
  const relayOrigin=throughProxy?fixture.proxyOrigin:fixture.serverOrigin;
  assert.equal(new URL(relayOrigin).protocol,'https:');
  const extPath=join(temp,'extension');
  await cp(extensionRoot,extPath,{
    recursive:true,
    filter:src=>!src.slice(extensionRoot.length).replaceAll('\\','/')
      .split('/').some(part=>['node_modules','tests','.git'].includes(part))
  });
  // The installed extension under test is a throwaway directory copy; only
  // the two loopback website/relay origins gain CI-specific host permissions.
  // The production manifest keeps mandatory per-site user consent.
  const manifestPath=join(extPath,'manifest.json');
  const manifest=JSON.parse(await readFile(manifestPath,'utf8'));
  manifest.host_permissions=['http://127.0.0.1/*','https://127.0.0.1/*'];
  await writeFile(manifestPath,JSON.stringify(manifest,null,2));

  const a=await extensionContext(extPath,join(temp,'chrome-a'),{localTestTLS:true});
  const b=await extensionContext(extPath,join(temp,'chrome-b'),{localTestTLS:true});
  const [identityA,identityB]=await Promise.all([extensionIdentity(a),extensionIdentity(b)]);
  assert.notEqual(identityA.deviceId,identityB.deviceId);
  await setServerAndSite(a,relayOrigin,siteOrigin,'Chromium A');
  await setServerAndSite(b,relayOrigin,siteOrigin,'Chromium B');
  const siteA=await a.context.newPage();
  const siteB=await b.context.newPage();
  await sourceSiteState(siteA,siteOrigin);
  await siteB.goto(siteOrigin+'/');
  assert.deepEqual(await siteB.evaluate(async()=>(await(await fetch('/whoami')).json())),
    {user:'anonymous'});

  const [registeredA,registeredB]=await Promise.all([
    workerCommand(a,{type:'REGISTER_BROWSER'}),workerCommand(b,{type:'REGISTER_BROWSER'})
  ]);
  assert.equal(registeredA.state,'pending');
  assert.equal(registeredB.state,'pending');
  assert.equal((await workerCommand(a,{type:'GET_STATUS'})).pendingApproval,true);
  assert.equal((await workerCommand(b,{type:'GET_STATUS'})).pendingApproval,true);
  // Real Chrome must not establish a signed WSS session merely because it
  // registered a public key and claimed an Identity ID.
  const pendingAuth=await a.options.evaluate(async()=>
    chrome.runtime.sendMessage({type:'CONNECT_BROWSER'}));
  assert.equal(pendingAuth.ok,false,'pending browser authenticated before approval');
  // The production WSS Handler MUST refuse a browser until its server-side
  // approval is granted; the fixture never modifies the browser request.
  // No credential content is sent to the fixture-admin HTTP endpoint.
  await deviceDecision(fixture,'approve',identityA.deviceId,true,false);
  await deviceDecision(fixture,'approve',identityB.deviceId,false,true);
  // Do NOT invoke CONNECT_BROWSER. Exercise the real persisted pending
  // enrollment, Chrome alarm, HTTP status recheck and signed WSS auto-dial.
  // Backdate only the synthetic test Profiles' last-check timestamp to
  // avoid sleeping through the production 1-minute polling interval.
  for(const profile of [a,b]){
    await profile.options.evaluate(async()=>{
      const key='browserSyncPendingEnrollment';
      const saved=(await chrome.storage.local.get(key))[key];
      if(!saved)throw new Error('pending device enrollment was not persisted');
      await chrome.storage.local.set({[key]:{...saved,lastCheckedAt:Date.now()-65000}});
      await chrome.alarms.create('browser-sync-reconcile',{when:Date.now()+500});
    });
  }
  await eventually('approved A auto-connect WSS',async()=>{
    const state=await workerCommand(a,{type:'GET_STATUS'});
    return state.connected&&!state.pendingApproval;
  });
  await eventually('approved B auto-connect WSS',async()=>{
    const state=await workerCommand(b,{type:'GET_STATUS'});
    return state.connected&&!state.pendingApproval;
  });
  for(const profile of [a,b]){
    await profile.options.evaluate(()=>chrome.alarms.create(
      'browser-sync-reconcile',{periodInMinutes:1}));
  }
  const [peersA,peersB]=await Promise.all([
    workerCommand(a,{type:'LIST_PEERS'}),workerCommand(b,{type:'LIST_PEERS'})
  ]);
  assert.ok(peersA.peers.some(p=>p.id===identityB.deviceId&&p.receive));
  assert.ok(peersB.peers.some(p=>p.id===identityA.deviceId&&p.send));

  // The default user-visible four-checkbox selection creates a v4 policy.
  const offer=await workerCommand(a,{type:'CREATE_RULE',
    targetID:identityB.deviceId,siteOrigin,
    dataTypes:{allCookies:true,localStorage:true,sessionStorage:true,indexedDB:true}});
  assert.ok(offer.ruleId);
  const bRules=await workerCommand(b,{type:'LIST_RULES'});
  const toAccept=bRules.rules.find(x=>x.id===offer.ruleId);
  assert.equal(toAccept.role,'target');
  assert.equal(toAccept.status,'offered');
  assert.ok(toAccept.valid&&toAccept.code,'B must decrypt/verify A encrypted pairing policy');
  assert.equal(toAccept.policy.schemaVersion,4);
  assert.deepEqual([toAccept.policy.allCookies,toAccept.policy.localStorage,
    toAccept.policy.sessionStorage,toAccept.policy.indexedDB],[true,true,true,true]);
  const aRules=await workerCommand(a,{type:'LIST_RULES'});
  assert.equal(aRules.rules.find(x=>x.id===offer.ruleId).code,toAccept.code,
    'the out-of-band A/B pairing confirmation code must match');
  await workerCommand(b,{type:'ACCEPT_RULE',ruleId:offer.ruleId,
    confirmed:true,pairingCode:toAccept.code});
  await workerCommand(a,{type:'CONFIRM_RULE',ruleId:offer.ruleId,
    confirmed:true,pairingCode:toAccept.code});

  const afterPair=await workerCommand(b,{type:'LIST_RULES'});
  const active=afterPair.rules.find(x=>x.id===offer.ruleId);
  assert.equal(active.status,'active');
  assert.equal(active.keysConfirmed,true);
  assert.ok(active.sourceApproved&&active.targetApproved);

  // Observe only the opaque message ID in Chrome storage; no credentials
  // or encrypted website contents pass through the test controller.
  await a.options.evaluate(ruleId=>{
    globalThis.__relayE2EMessageIds=[];
    chrome.storage.onChanged.addListener((changes,area)=>{
      if(area!=='local')return;
      const messageId=changes.browserSyncPendingDelivery?.newValue?.[ruleId]?.messageId;
      if(messageId)globalThis.__relayE2EMessageIds.push(messageId);
    });
  },offer.ruleId);

  // No snapshot is passed by the Node test. A's real MV3 worker collects,
  // signs and encrypts website credentials; the actual RelayProxy WSS
  // validates/countersigns/routs opaque data; B's worker decrypts, restores,
  // sends ACK, and the source receives its terminal delivery result.
  const send=await workerCommand(a,{type:'SEND_SESSION',ruleId:offer.ruleId});
  assert.ok(['RELAYED','SENDING','APPLIED'].includes(send.state),
    'source did not receive Relay WSS admission');
  await eventually('receiver Chrome APPLIED ACK',async()=>{
    const status=await b.options.evaluate(async ruleId=>{
      const data=await chrome.storage.local.get('browserSyncTransferStatus');
      return data.browserSyncTransferStatus?.[ruleId]?.state;
    },offer.ruleId);
    return status==='APPLIED';
  });
  await eventually('source terminal delivery status',async()=>{
    const status=await a.options.evaluate(async ruleId=>{
      const data=await chrome.storage.local.get('browserSyncTransferStatus');
      return data.browserSyncTransferStatus?.[ruleId]?.state;
    },offer.ruleId);
    return status==='APPLIED';
  });
  await siteB.waitForLoadState('domcontentloaded');
  await verifyTargetSite(siteB,siteOrigin);

  // A lost ACK must be recovered from the durable server receipt after
  // a real WSS disconnect. Reinsert the opaque pending ID to model an MV3
  // worker suspended before it processed the server's already-committed ACK.
  const deliveredMessageId=await eventually('observe opaque source delivery ID',
    ()=>a.options.evaluate(()=>globalThis.__relayE2EMessageIds?.[0]));
  assert.match(deliveredMessageId,/^[0-9a-f-]{36}$/i);
  await deviceDecision(fixture,'drop',identityA.deviceId);
  await eventually('source WSS disconnected',async()=>{
    const state=await workerCommand(a,{type:'GET_STATUS'});
    return !state.connected;
  },{timeout:15000});
  await a.options.evaluate(async({ruleId,messageId})=>{
    await chrome.storage.local.set({
      browserSyncPendingDelivery:{[ruleId]:{ruleId,messageId,createdAt:Date.now()}},
      browserSyncTransferStatus:{[ruleId]:{state:'UNKNOWN',time:Date.now()}}
    });
    // Use the production MV3 alarm listener, not a test-only worker command.
    await chrome.alarms.create('browser-sync-reconcile',{when:Date.now()+1500});
  },{ruleId:offer.ruleId,messageId:deliveredMessageId});
  await eventually('source alarm reconnect and durable ACK recovery',async()=>{
    const state=await workerCommand(a,{type:'GET_STATUS'});
    const result=await a.options.evaluate(async ruleId=>{
      const all=await chrome.storage.local.get([
        'browserSyncPendingDelivery','browserSyncTransferStatus']);
      return {pending:all.browserSyncPendingDelivery?.[ruleId],
        status:all.browserSyncTransferStatus?.[ruleId]?.state};
    },offer.ruleId);
    return state.connected&&result.status==='APPLIED'&&!result.pending;
  },{timeout:100000});
  await a.options.evaluate(()=>chrome.alarms.create('browser-sync-reconcile',
    {periodInMinutes:1}));

  // The receiving Profile must also return to its signed WSS subscription
  // without a manual CONNECT_BROWSER call and accept another live delivery.
  await deviceDecision(fixture,'drop',identityB.deviceId);
  await eventually('receiver WSS disconnected',async()=>{
    const state=await workerCommand(b,{type:'GET_STATUS'});
    return !state.connected;
  },{timeout:15000});
  await b.options.evaluate(()=>chrome.alarms.create('browser-sync-reconcile',
    {when:Date.now()+1500}));
  await eventually('receiver alarm reconnect',async()=>{
    const state=await workerCommand(b,{type:'GET_STATUS'});
    return state.connected;
  },{timeout:100000});
  await b.options.evaluate(()=>chrome.alarms.create('browser-sync-reconcile',
    {periodInMinutes:1}));
  for(const profile of [a,b]){
    await profile.options.evaluate(async ruleId=>{
      const all=(await chrome.storage.local.get('browserSyncTransferStatus'))
        .browserSyncTransferStatus||{};
      delete all[ruleId];
      await chrome.storage.local.set({browserSyncTransferStatus:all});
    },offer.ruleId);
  }
  await workerCommand(a,{type:'SEND_SESSION',ruleId:offer.ruleId});
  for(const [profile,label] of [[a,'source'],[b,'receiver']]){
    await eventually(label+' receives APPLIED after reconnect',async()=>{
      const status=await profile.options.evaluate(async ruleId=>
        (await chrome.storage.local.get('browserSyncTransferStatus'))
          .browserSyncTransferStatus?.[ruleId]?.state,offer.ruleId);
      return status==='APPLIED';
    });
  }
  await verifyTargetSite(siteB,siteOrigin);

  // A fully stopped Chromium process is stricter than dropping its WSS.
  // The same on-disk Profile must retain its non-extractable P-256 keys and
  // paired trust; onStartup/Alarm should restore WSS without manual CONNECT.
  await b.context.close();
  const restartedB=await extensionContext(extPath,join(temp,'chrome-b'),
    {localTestTLS:true});
  assert.equal(restartedB.extensionId,b.extensionId,
    'Profile restart changed the installed extension identity');
  const restoredIdentityB=await extensionIdentity(restartedB);
  assert.deepEqual(restoredIdentityB,identityB,
    'Profile restart regenerated or lost browser device keys');
  await eventually('restarted Chromium B automatically authenticates',async()=>{
    const status=await workerCommand(restartedB,{type:'GET_STATUS'});
    return status.connected;
  },{timeout:80000});
  const reopenedRules=await workerCommand(restartedB,{type:'LIST_RULES'});
  const persistedRule=reopenedRules.rules.find(rule=>rule.id===offer.ruleId);
  assert.ok(persistedRule?.valid && persistedRule.status==='active' &&
    persistedRule.keysConfirmed,
    'Profile restart lost signed pairing trust or active rule');
  // Chrome onStartup may already have requested a new snapshot and chosen
  // a website tab for the restore. Never close that tab mid-transaction: the
  // test itself would cause an artificial PARTIAL/FAILED result. Reuse it, or
  // create a site tab only when startup has not opened one.
  const existingSitePages=restartedB.context.pages().filter(page=>{
    try{return new URL(page.url()).origin===siteOrigin;}catch{return false;}
  });
  const restartedSiteB=existingSitePages[0]||await restartedB.context.newPage();
  if(new URL(restartedSiteB.url()).origin!==siteOrigin)
    await restartedSiteB.goto(siteOrigin+'/');
  await restartedSiteB.waitForLoadState('domcontentloaded');
  for(const profile of [a,restartedB]){
    await profile.options.evaluate(async ruleId=>{
      const all=(await chrome.storage.local.get('browserSyncTransferStatus'))
        .browserSyncTransferStatus||{};
      delete all[ruleId];
      await chrome.storage.local.set({browserSyncTransferStatus:all});
    },offer.ruleId);
  }
  await workerCommand(a,{type:'SEND_SESSION',ruleId:offer.ruleId});
  try {
    for(const [profile,label] of [[a,'source'],[restartedB,'restarted receiver']]){
      await eventually(label+' confirms APPLIED after full Profile restart',async()=>{
      const state=await profile.options.evaluate(async ruleId=>{
        const stored=await chrome.storage.local.get([
          'browserSyncTransferStatus','browserSyncPendingDelivery']);
        return {status:stored.browserSyncTransferStatus?.[ruleId]?.state||'ABSENT',
          pending:!!stored.browserSyncPendingDelivery?.[ruleId]};
      },offer.ruleId);
      if(state.status!=='APPLIED')
        throw new Error('state='+state.status+', hasPending='+state.pending);
      return true;
      });
    }
  }catch(error){
    // Diagnosis stays within fixed, schema-controlled audit categories.
    // Never print the website origin, browser keys, exception stack from B,
    // cookie values, encryption payloads or session storage.
    console.error('Post-restart ACK outcome categories:',
      JSON.stringify(await diagnosticCategories(fixture)));
    throw error;
  }
  await verifyTargetSite(restartedSiteB,siteOrigin);

  // Administrator revocation invalidates the session and its pairing: even
  // a browser holding its Profile-local signing key must be denied.
  await deviceDecision(fixture,'revoke',identityB.deviceId);
  const staleSocket=await restartedB.options.evaluate(async()=>
    chrome.runtime.sendMessage({type:'LIST_RULES'}));
  assert.equal(staleSocket.ok,false,
    'revoked browser used an already-authenticated WSS socket');
  await workerCommand(restartedB,{type:'SAVE_SERVER',serverOrigin:relayOrigin});
  const denied=await restartedB.options.evaluate(async()=>chrome.runtime.sendMessage({type:'CONNECT_BROWSER'}));
  assert.equal(denied.ok,false,'revoked Chrome device reauthenticated over WSS');

  // A Profile can retain its device ID after a corrupted/uninstalled key
  // database. Losing its non-exportable private keys must rotate the device
  // identity; the Server must treat it as a brand-new pending enrollment, not
  // silently inherit the old browser's approval or encrypted pairing.
  const rotatedB=await restartedB.options.evaluate(async()=>{
    const before=(await chrome.storage.local.get('browserDeviceId')).browserDeviceId;
    // A committed key-store transaction simulates private-key loss without
    // racing open handles from Chrome's MV3 alarm or readKey(). Whole-database
    // deletion can spuriously fail with onblocked even when the keys are safe
    // to remove and correctly re-enroll.
    const db=await new Promise((resolve,reject)=>{
      const request=indexedDB.open('relayproxy-browser-sync',1);
      request.onsuccess=()=>resolve(request.result);
      request.onerror=()=>reject(request.error||new Error('identity database unavailable'));
    });
    try{
      await new Promise((resolve,reject)=>{
        const tx=db.transaction('keys','readwrite');
        tx.objectStore('keys').delete('signing');
        tx.objectStore('keys').delete('encryption');
        tx.oncomplete=resolve;
        tx.onerror=()=>reject(tx.error||new Error('identity key deletion failed'));
        tx.onabort=()=>reject(tx.error||new Error('identity key deletion aborted'));
      });
    }finally{db.close();}
    const {getOrCreateIdentity}=await import(
      chrome.runtime.getURL('src/device-identity.js'));
    const current=await getOrCreateIdentity();
    return {before,current};
  });
  assert.equal(rotatedB.before,identityB.deviceId,
    'test did not preserve device ID before losing its private keys');
  assert.notEqual(rotatedB.current.deviceId,identityB.deviceId,
    'missing private keys reused an already-enrolled browser ID');
  assert.notEqual(rotatedB.current.signingPublicKey,identityB.signingPublicKey);
  assert.notEqual(rotatedB.current.encryptionPublicKey,identityB.encryptionPublicKey);
  const rotatedEnrollment=await workerCommand(restartedB,{type:'REGISTER_BROWSER'});
  assert.equal(rotatedEnrollment.state,'pending',
    'fresh signing keys silently inherited revoked device approval');
  const rotatedDenied=await restartedB.options.evaluate(async()=>
    chrome.runtime.sendMessage({type:'CONNECT_BROWSER'}));
  assert.equal(rotatedDenied.ok,false,
    'new unapproved device authenticated using an old Profile authorization');
  console.log('Real RelayProxy TLS/WSS '+(throughProxy?'via HTTPS reverse proxy':'direct')+
    ' + Chromium A/B passed: enrolled/approved,');
  console.log('signed challenge, E2EE rule/code, opaque WSS delivery, APPLIED ACK,');
  console.log('actual website login+IndexedDB, MV3 alarm WSS reconnect + durable ACK recovery,');
  console.log('full Chromium Profile restart/key retention, post-restart delivery,');
  console.log('pending-auth denial, live/reconnect revocation and private-key loss rotation.');
}

let error;
try{await main();}catch(e){error=e;console.error(e);}finally{
  for(const cleanup of scope.reverse()){
    try{await cleanup();}catch(e){console.error('cleanup:',e);}
  }
}
if(error)process.exitCode=1;
