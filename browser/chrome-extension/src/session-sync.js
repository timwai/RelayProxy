// MV3 worker controller for approved A -> B encrypted website session sync.
// No credentials are persisted: snapshots exist in memory solely until the
// signed encrypted message has been produced.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, getTrustedPeer } from './rules.js';
import { sendControl, connectBrowser, onServerPush } from './server-api.js';
import { decryptEncryptedOffer } from './envelope.js';
import { encryptSnapshot, decryptSnapshot } from './session-envelope.js';
import { captureCookies, applyCookies } from './cookies.js';
import {captureAllCookies,applyAllCookies} from './cookies-all.js';
import {captureWebStorage,prepareWebStorageRestore} from './web-storage.js';
import {captureIndexedDB,prepareIndexedDBRestore,JOURNAL_KEY as IDB_JOURNAL} from './indexeddb-sync.js';
import {showRestoredWebsiteTab} from './session-tabs.js';
import { shouldReconcile, acceptDeliveryAck, isTerminalAck, nextSessionSequence, validOverwriteGrant, OVERWRITE_GRANT_TTL_MS, nextReconnectDelay, shouldRetryConnection } from './lifecycle.js';
import {ensureNetworkForRule} from './network-binding.js';

const sourceBusy = new Set();
const sourceDirty = new Set();
const cookieTimers = new Map();
const incomingQueue = new Map();
const LAST_SEQUENCE = 'browserSyncOutgoingSequence';
const LAST_APPLIED = 'browserSyncLastAppliedSequence';
const PENDING_DELIVERY = 'browserSyncPendingDelivery';
const LAST_RECONCILE = 'browserSyncLastReconcileAt';
const ALLOW_OVERRIDE = 'browserSyncAllowOverwrite';
const CONNECTION_RETRY = 'browserSyncConnectionRetry';
const PAUSED_RESTORE = 'browserSyncRestorePaused';
const statusKey = 'browserSyncTransferStatus';
const pendingOpenKey = 'browserSyncPendingOpen';

async function setStatus(ruleId, result) {
  // Explicitly store only a status enum; never Cookie values or payloads.
  const current = (await chrome.storage.local.get(statusKey))[statusKey] || {};
  current[ruleId] = { state: result, time: Date.now() };
  await chrome.storage.local.set({[statusKey]:current});
}

async function ruleContext(ruleId) {
  const identity = await getOrCreateIdentity();
  const replies = await Promise.all([sendControl('LIST_RULES'),sendControl('LIST_PEERS')]);
  const rule = (replies[0].rules||[]).find(r=>r.ruleId===ruleId&&r.status==='active'&&
    r.sourceApproved===true&&r.targetApproved===true&&r.keysConfirmed===true);
  if (!rule) throw new Error('配对规则未激活');
  const sourceRole = rule.sourceBrowserDeviceId===identity.deviceId;
  const targetRole = rule.targetBrowserDeviceId===identity.deviceId;
  if (!sourceRole&&!targetRole) throw new Error('会话规则设备不匹配');
  const peerId = sourceRole?rule.targetBrowserDeviceId:rule.sourceBrowserDeviceId;
  const remote = (replies[1].peers||[]).find(v=>v.id===peerId);
  const pinned = await getTrustedPeer(peerId);
  if(!remote||!pinned||remote.signingPublicKey!==pinned.signingPublicKey||
    remote.encryptionPublicKey!==pinned.encryptionPublicKey) throw new Error('设备公钥未通过配对确认');
  const settings=await getSettings();
  let policy;
  if(sourceRole){
    const saved=(await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers||{};
    policy=saved[ruleId];
  }else{
    policy=await decryptEncryptedOffer(rule,remote);
  }
  if(!policy||!settings.sites.includes(policy.siteOrigin))throw new Error('未授权的网站同步策略');
  const url=new URL(policy.siteOrigin);
  if(!(await chrome.permissions.contains({origins:[url.protocol+'//'+url.hostname+'/*']})))
    throw new Error('当前浏览器没有指定站点权限');
  return {rule,remote,policy,sourceRole,targetRole};
}

export async function sendSnapshot(ruleId) {
  if(sourceBusy.has(ruleId)){
    sourceDirty.add(ruleId);
    return {state:'BUSY'};
  }
  sourceBusy.add(ruleId);
  let transmissionAttempted=false;
  try{
    const ctx=await ruleContext(ruleId);
    if(!ctx.sourceRole)throw new Error('只有来源设备可以发送登录状态');
    const payload=ctx.policy.schemaVersion>=2
      ? {...await captureAllCookies(ctx.policy),storage:await captureWebStorage(ctx.policy),
          ...(ctx.policy.schemaVersion===3?{indexedDB:await captureIndexedDB(ctx.policy)}:{})}
      : await captureCookies(ctx.policy);
    const stored=(await chrome.storage.local.get(LAST_SEQUENCE))[LAST_SEQUENCE]||{};
    // The server is authoritative after a crash, upgrade or lost local
    // storage. A lower local counter must never cause silent replay failures.
    const cursorReply=await sendControl('SEQUENCE_CURSOR',{ruleId});
    const cursor=cursorReply?.lastSequence;
    if(cursorReply?.type!=='SESSION_CURSOR'||cursorReply.ruleId!==ruleId)
      throw new Error('无法安全恢复会话序号');
    const previous=stored[ruleId] ?? 0;
    const sequence=nextSessionSequence(previous,cursor);
    // Reserve BEFORE asynchronous network delivery; no repeated nonce or
    // sequence even if WSS rejects the delivery or worker suspends.
    stored[ruleId]=sequence;
    await chrome.storage.local.set({[LAST_SEQUENCE]:stored});
    const envelope=await encryptSnapshot(ctx.rule,ctx.remote,sequence,payload);
    // Keep only opaque IDs and timestamps, not credential contents.
    // This is written before sending: a fast APPLIED cannot race the record.
    const pending=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
    pending[ruleId]={ruleId,messageId:envelope.messageId,createdAt:Date.now()};
    await chrome.storage.local.set({[PENDING_DELIVERY]:pending});
    await setStatus(ruleId,'SENDING');
    transmissionAttempted=true;
    const ack=await sendControl('SESSION_SNAPSHOT',{envelope});
    if(ack.status!=='RELAYED')throw new Error('中继没有接受加密快照');
    const latest=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
    // A fast target APPLIED may have consumed this pending entry already.
    if(latest[ruleId]?.messageId===envelope.messageId)await setStatus(ruleId,'RELAYED');
    return {state:'RELAYED',count:payload.cookies.length};
  }catch(error){
    if(!transmissionAttempted){
      const pending=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
      delete pending[ruleId];
      await chrome.storage.local.set({[PENDING_DELIVERY]:pending});
      await setStatus(ruleId,'FAILED');
    }else{
      // A timed-out WSS response does NOT prove B failed to apply a Cookie.
      // Retain messageId so reconnect can query the durable terminal receipt.
      const latest=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
      if(latest[ruleId])await setStatus(ruleId,'UNKNOWN');
    }
    throw error;
  }finally{
    sourceBusy.delete(ruleId);
    if(sourceDirty.delete(ruleId)){
      // An onChanged event arrived while a previous encrypted snapshot was
      // in flight. Re-read the latest Cookie state rather than dropping it.
      queueMicrotask(()=>{sendSnapshot(ruleId).catch(()=>{});});
    }
  }
}

export async function requestSnapshot(ruleId) {
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('只有接收设备能请求同步');
  await ensureNetworkForRule(ruleId,ctx.policy.siteOrigin);
  const paused=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
  const interruptedKeys=await chrome.storage.local.get([
    'browserSyncRestoreJournal','browserSyncStorageRestoreJournal',IDB_JOURNAL]);
  if(paused[ruleId]||interruptedKeys.browserSyncRestoreJournal?.[ruleId]||
     interruptedKeys.browserSyncStorageRestoreJournal?.[ruleId]||
     interruptedKeys[IDB_JOURNAL]?.[ruleId])
    throw new Error('此规则因上次会话恢复未完成已暂停，请先检查目标网站');
  const answer=await sendControl('SYNC_REQUEST',{ruleId});
  if(answer.status!=='REQUESTED')throw new Error('来源设备不在线');
  return {state:'REQUESTED'};
}

export async function syncThenOpen(ruleId) {
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('只能由接收设备发起同步后打开');
  await ensureNetworkForRule(ruleId,ctx.policy.siteOrigin);
  const pending=(await chrome.storage.local.get(pendingOpenKey))[pendingOpenKey]||{};
  pending[ruleId]={origin:ctx.policy.siteOrigin,expiresAt:Date.now()+30000};
  await chrome.storage.local.set({[pendingOpenKey]:pending});
  try{return await requestSnapshot(ruleId);}
  catch(error){
    delete pending[ruleId];
    await chrome.storage.local.set({[pendingOpenKey]:pending});
    throw error;
  }
}

// Only fixed categories leave the browser. Never send arbitrary exception
// messages, website origin names or decrypted session payloads to Server.
export function browserRestoreFailureCode(error) {
  const message=String(error?.message||'');
  if(message.startsWith('PARTIAL_ROLLBACK:'))return 'PARTIAL_ROLLBACK';
  if(message.startsWith('CONFLICT:'))return 'CONFLICT';
  if(/旧会话快照|replay|sequence/i.test(message))return 'REPLAY_REJECTED';
  if(/IndexedDB|indexeddb/i.test(message))return 'INDEXEDDB_FAILURE';
  if(/cookie.{0,60}(?:failed|失败|rejected|denied|回滚|写入)|(?:failed|失败).{0,60}cookie/i.test(message))return 'COOKIE_FAILURE';
  if(/storage/i.test(message))return 'STORAGE_FAILURE';
  if(/解密|decrypt|signature/i.test(message))return 'DECRYPT_FAILED';
  if(/网络|proxy|PAC|出口|connect|network/i.test(message))return 'NETWORK_UNAVAILABLE';
  return 'UNSPECIFIED';
}

// Consume BEFORE any mutation, so a failed restore or MV3 restart cannot
// turn one user's account-switch confirmation into a reusable permission.
export async function consumeOneTimeOverwrite(ruleId, siteOrigin) {
  const stored=(await chrome.storage.local.get(ALLOW_OVERRIDE))[ALLOW_OVERRIDE]||{};
  const candidate=stored[ruleId];
  if(candidate===undefined)return false;
  delete stored[ruleId];
  await chrome.storage.local.set({[ALLOW_OVERRIDE]:stored});
  return validOverwriteGrant(candidate,siteOrigin,Date.now());
}

async function handleIncomingSnapshot(envelope) {
  const ruleId=envelope?.ruleId;
  if(typeof ruleId!=='string'||ruleId.length>128)return;
  let result='FAILED';
  let diagnostic='UNSPECIFIED';
  try{
    const ctx=await ruleContext(ruleId);
    if(!ctx.targetRole||ctx.remote.id!==envelope.sourceBrowserDeviceId ||
       ctx.rule.targetBrowserDeviceId!==envelope.targetBrowserDeviceId)
      throw new Error('接收设备身份不匹配');
    const paused=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
    if(paused[ruleId])throw new Error('PARTIAL_ROLLBACK: 接收端待人工检查');
    const restoreKeys=await chrome.storage.local.get([
      'browserSyncRestoreJournal','browserSyncStorageRestoreJournal',IDB_JOURNAL]);
    if(restoreKeys.browserSyncRestoreJournal?.[ruleId]||
       restoreKeys.browserSyncStorageRestoreJournal?.[ruleId]||
       restoreKeys[IDB_JOURNAL]?.[ruleId])
      throw new Error('PARTIAL_ROLLBACK: 上次状态恢复中断');
    const sequences=(await chrome.storage.local.get(LAST_APPLIED))[LAST_APPLIED]||{};
    if(!Number.isSafeInteger(envelope.sequence)||envelope.sequence<=Number(sequences[ruleId]||0))
      throw new Error('旧会话快照已拒绝');
    const payload=await decryptSnapshot(envelope,ctx.remote);
    await ensureNetworkForRule(ruleId,ctx.policy.siteOrigin);
    const allowOverwrite=await consumeOneTimeOverwrite(ruleId,ctx.policy.siteOrigin);
    let restoredTabId=null;
    try{
      if(ctx.policy.schemaVersion>=2){
        const txn=await prepareWebStorageRestore(ruleId,payload.storage,ctx.policy,
          {allowOverwrite:allowOverwrite});
        // Preflight conflicts and schema BEFORE any Cookie/Storage mutation.
        if(ctx.policy.schemaVersion===3 && payload.indexedDB===undefined)
          throw new Error('IndexedDB v3 快照缺失，拒绝不完整的登录状态');
        const idb=ctx.policy.schemaVersion===3
          ?await prepareIndexedDBRestore(ruleId,payload.indexedDB,ctx.policy,
            {allowOverwrite:allowOverwrite,tabId:txn.tabId}):null;
        let applied=false;
        try{
          if(idb)await idb.apply();
          await txn.apply();
          await applyAllCookies(ruleId,payload,ctx.policy,{allowOverwrite:allowOverwrite});
          applied=true;
          await txn.commit();
          if(idb)await idb.commit();
          restoredTabId=txn.tabId;
          await chrome.tabs.reload(txn.tabId).catch(()=>{});
        }catch(error){
          const storageRolledBack=await txn.rollback();
          const idbRolledBack=idb?await idb.rollback():true;
          if(!storageRolledBack||!idbRolledBack||applied)
            throw new Error('PARTIAL_ROLLBACK: Cookie、Storage 或 IndexedDB 恢复不完整');
          throw error;
        }
      }else{
        await applyCookies(ruleId,payload,ctx.policy,{allowOverwrite:allowOverwrite});
      }
      sequences[ruleId]=envelope.sequence;
      await chrome.storage.local.set({[LAST_APPLIED]:sequences});
      result='APPLIED';
      const pending=(await chrome.storage.local.get(pendingOpenKey))[pendingOpenKey]||{};
      const request=pending[ruleId];
      if(request) {
        delete pending[ruleId];
        await chrome.storage.local.set({[pendingOpenKey]:pending});
        if(request.expiresAt>Date.now()&&request.origin===ctx.policy.siteOrigin){
          if(ctx.policy.schemaVersion>=2 && restoredTabId!==null){
            // A new tab would have different sessionStorage and could log B out.
            await showRestoredWebsiteTab(restoredTabId,ctx.policy.siteOrigin).catch(()=>{});
          }else{
            await chrome.tabs.create({url:ctx.policy.siteOrigin}).catch(()=>{});
          }
        }
      }
    }catch(error){
      const code=String(error.message||'');
      if(code.startsWith('CONFLICT:')){result='CONFLICT';diagnostic='CONFLICT';}
      else throw error;
    }
    await setStatus(ruleId,result);
  }catch(error){
    diagnostic=browserRestoreFailureCode(error);
    // An incomplete rollback can leave a mixed website login state.
    // Fail closed until the receiver explicitly inspects and resumes.
    const paused=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
    if(String(error.message||'').startsWith('PARTIAL_ROLLBACK:')){
      paused[ruleId]=true;
      await chrome.storage.local.set({[PAUSED_RESTORE]:paused});
    }
    await setStatus(ruleId,paused[ruleId]?'PARTIAL':'FAILED');
  }
  try{
    if(typeof envelope.messageId==='string')
      await sendControl('SYNC_ACK',{ruleId,messageId:envelope.messageId,status:result,
        errorCode:result==='APPLIED'?'':diagnostic});
  }catch{ /* peer may be offline; session values are still never logged */ }
}

// WebSocket callbacks are asynchronous. Serialize per-rule restores so a
// slower older message cannot finish after and overwrite a newer snapshot.
function enqueueIncoming(envelope) {
  const ruleId=envelope?.ruleId;
  if(typeof ruleId!=='string'||ruleId.length>128)return Promise.resolve();
  const previous=incomingQueue.get(ruleId)||Promise.resolve();
  const next=previous.catch(()=>{}).then(()=>handleIncomingSnapshot(envelope));
  incomingQueue.set(ruleId,next);
  next.finally(()=>{
    if(incomingQueue.get(ruleId)===next)incomingQueue.delete(ruleId);
  }).catch(()=>{});
  return next;
}

onServerPush(async message => {
  if(message.type==='SESSION_SNAPSHOT')return enqueueIncoming(message.envelope);
  if(message.type==='SYNC_REQUEST'&&typeof message.ruleId==='string'){
    try{await sendSnapshot(message.ruleId);}catch{ /* offline or Cookie scope incompatible */ }
  }
  if(message.type==='SYNC_ACK'&&typeof message.ruleId==='string'){
    const pending=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
    const item=pending[message.ruleId];
    if(!acceptDeliveryAck(item,message,Date.now()))return;
    await setStatus(message.ruleId,message.status);
    if(isTerminalAck(message.status)){
      delete pending[message.ruleId];
      await chrome.storage.local.set({[PENDING_DELIVERY]:pending});
    }
  }
});

// Debounced Cookie change events only for active source rules with prior,
// explicit per-origin permission. The active-rule check happens again at send.
export async function onCookieChange(cookie) {
  if(!cookie||!cookie.domain)return;
  const hostname=(cookie.domain||'').replace(/^\./,'').toLowerCase();
  const local=(await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers||{};
  for(const [ruleId,policy] of Object.entries(local)){
    const host=new URL(policy.siteOrigin).hostname;
    if(!(host===hostname||host.endsWith('.'+hostname)))continue;
    if(policy.siteOrigin.startsWith('http://') && (cookie.secure || cookie.partitionKey))continue;
    if(![2,3].includes(policy.schemaVersion) &&
       (cookie.path!=='/'||!cookie.hostOnly||!cookie.secure||cookie.partitionKey||
        !policy.cookieNames.includes(cookie.name)))continue;
    clearTimeout(cookieTimers.get(ruleId));
    cookieTimers.set(ruleId,setTimeout(()=>{
      cookieTimers.delete(ruleId);
      sendSnapshot(ruleId).catch(()=>{});
    },2000));
  }
}

export async function reconcilePendingDeliveries() {
  const pending=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
  for(const [ruleId,item] of Object.entries(pending)){
    if(!item||typeof item.messageId!=='string')continue;
    let statusReply;
    try{
      statusReply=await sendControl('DELIVERY_STATUS',{ruleId,messageId:item.messageId});
    }catch{continue;} // leave pending for next reconnect
    if(statusReply?.type!=='DELIVERY_RESULT'||statusReply.ruleId!==ruleId||
       statusReply.messageId!==item.messageId)continue;
    const latest=(await chrome.storage.local.get(PENDING_DELIVERY))[PENDING_DELIVERY]||{};
    if(latest[ruleId]?.messageId!==item.messageId)continue;
    if(['APPLIED','FAILED','CONFLICT'].includes(statusReply.status)){
      await setStatus(ruleId,statusReply.status);
      delete latest[ruleId];
      await chrome.storage.local.set({[PENDING_DELIVERY]:latest});
    }else if(statusReply.status==='UNKNOWN'){
      // Outcome was lost or expired: never claim a login actually failed.
      await setStatus(ruleId,'UNKNOWN');
      delete latest[ruleId];
      await chrome.storage.local.set({[PENDING_DELIVERY]:latest});
    }else if(statusReply.status==='RECEIVED'){
      await setStatus(ruleId,'RECEIVED');
    }
  }
}

// A disconnected Chrome MV3 worker must re-establish its authenticated WSS
// listener even if the five-minute scheduled snapshot cycle is not due.
// Persist the retry deadline because the service worker can be destroyed.
export async function restoreActiveSubscriptions({force=false}={}) {
  const settings=await getSettings();
  if(!settings.serverOrigin)return;
  const now=Date.now();
  const stored=(await chrome.storage.local.get(CONNECTION_RETRY))[CONNECTION_RETRY]||null;
  if(!force&&!shouldRetryConnection(stored,settings.serverOrigin,now))return;
  try{
    await connectBrowser();
    // A live authenticated socket also lets the source receive SYNC_REQUEST
    // and the receiver accept asynchronous SESSION_SNAPSHOT pushes.
    if(stored?.failures||stored?.nextRetryAt)
      await chrome.storage.local.set({[CONNECTION_RETRY]:{
        serverOrigin:settings.serverOrigin,failures:0,nextRetryAt:0}});
  }catch{
    const failures=(stored?.serverOrigin===settings.serverOrigin
      ?Math.max(0,Math.min(10,Number(stored.failures)||0)):0)+1;
    await chrome.storage.local.set({[CONNECTION_RETRY]:{
      serverOrigin:settings.serverOrigin,failures,
      nextRetryAt:now+nextReconnectDelay(failures)}});
    return;
  }
  // Delivery reconciliation must not wait for the periodic five-minute
  // refresh: ACKs can arrive while the browser worker was asleep.
  await reconcilePendingDeliveries();
  const last=(await chrome.storage.local.get(LAST_RECONCILE))[LAST_RECONCILE];
  if(!force&&!shouldReconcile(last,now))return;
  const identity=await getOrCreateIdentity();
  const rules=(await sendControl('LIST_RULES')).rules||[];
  // Do not record the last reconciliation until the Server was reachable.
  await chrome.storage.local.set({[LAST_RECONCILE]:now});
  for(const rule of rules){
    if(rule.status==='active'&&rule.targetBrowserDeviceId===identity.deviceId)
      await requestSnapshot(rule.ruleId).catch(()=>{});
  }
}

export async function pruneOverwriteGrants(){
  const all=(await chrome.storage.local.get(ALLOW_OVERRIDE))[ALLOW_OVERRIDE]||{};
  const clean={};
  const now=Date.now();
  for(const [id,grant] of Object.entries(all)){
    if(validOverwriteGrant(grant,grant?.siteOrigin,now))clean[id]=grant;
  }
  // Old releases stored persistent booleans. They must never be honored
  // after upgrade, even if the worker has no Server connection yet.
  if(Object.keys(clean).length!==Object.keys(all).length)
    await chrome.storage.local.set({[ALLOW_OVERRIDE]:clean});
}


export async function setOverwritePermission(ruleId,allowed) {
  if(typeof ruleId!=='string'||ruleId.length>128||!ruleId)
    throw new Error('无效的配对规则');
  // Revocation must work offline. If the SYNC_REQUEST fails after user
  // approval, do not leave a two-minute overwrite grant behind.
  const grants=(await chrome.storage.local.get(ALLOW_OVERRIDE))[ALLOW_OVERRIDE]||{};
  if(allowed!==true) {
    delete grants[ruleId];
    await chrome.storage.local.set({[ALLOW_OVERRIDE]:grants});
    return {ruleId,allowed:false,expiresAt:null};
  }
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole||ctx.rule.status!=='active')
    throw new Error('仅已激活配对的接收端可以确认覆盖');
  delete grants[ruleId]; // discard any legacy boolean/expired grant first
  grants[ruleId]={siteOrigin:ctx.policy.siteOrigin,
    expiresAt:Date.now()+OVERWRITE_GRANT_TTL_MS};
  await chrome.storage.local.set({[ALLOW_OVERRIDE]:grants});
  return {ruleId,allowed:true,expiresAt:grants[ruleId].expiresAt};
}

export async function resumePausedRestore(ruleId,confirmed) {
  if(confirmed!==true)throw new Error('需要确认已检查目标网站的登录状态');
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('仅接收设备可以恢复');
  const flags=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
  const journalKey='browserSyncRestoreJournal';
  const journal=(await chrome.storage.local.get(journalKey))[journalKey]||{};
  const storageKey='browserSyncStorageRestoreJournal';
  const storageJournal=(await chrome.storage.local.get(storageKey))[storageKey]||{};
  const idbJournal=(await chrome.storage.local.get(IDB_JOURNAL))[IDB_JOURNAL]||{};
  if(!flags[ruleId]&&!journal[ruleId]&&!storageJournal[ruleId]&&!idbJournal[ruleId])
    throw new Error('当前规则未处于暂停状态');
  delete flags[ruleId];
  delete journal[ruleId];
  delete storageJournal[ruleId];
  delete idbJournal[ruleId];
  await chrome.storage.local.set({[PAUSED_RESTORE]:flags,[journalKey]:journal,
    [storageKey]:storageJournal,[IDB_JOURNAL]:idbJournal});
  await setStatus(ruleId,'UNKNOWN');
  // Do not immediately overwrite a possibly changed website session.
  return {state:'UNKNOWN'};
}

export async function forgetRuleLocalState(ruleId) {
  clearTimeout(cookieTimers.get(ruleId));
  cookieTimers.delete(ruleId);
  // Removing sync metadata does NOT delete or revoke website Cookies.
  // Credential revocation must be handled by the destination website.
  for(const key of [LAST_SEQUENCE,LAST_APPLIED,ALLOW_OVERRIDE,statusKey,
      pendingOpenKey,PENDING_DELIVERY,PAUSED_RESTORE,'browserSyncManagedCookieHmacV1',
      'browserSyncManagedAllCookieHmacV2','browserSyncManagedWebStorageV2',
      'browserSyncStorageRestoreJournal',IDB_JOURNAL,
      'browserSyncManagedIndexedDBV1','browserSyncLocalOffers']){
    const values=(await chrome.storage.local.get(key))[key]||{};
    if(Object.hasOwn(values,ruleId)){
      delete values[ruleId];
      await chrome.storage.local.set({[key]:values});
    }
  }
}
