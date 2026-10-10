// MV3 worker controller for approved A -> B Cookie-only sync.
// No credentials are persisted: snapshots exist in memory solely until the
// signed encrypted message has been produced.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, getTrustedPeer } from './rules.js';
import { sendControl, onServerPush } from './server-api.js';
import { decryptEncryptedOffer } from './envelope.js';
import { encryptSnapshot, decryptSnapshot } from './session-envelope.js';
import { captureCookies, applyCookies } from './cookies.js';
import { shouldReconcile, acceptDeliveryAck, isTerminalAck } from './lifecycle.js';

const sourceBusy = new Set();
const sourceDirty = new Set();
const cookieTimers = new Map();
const incomingQueue = new Map();
const LAST_SEQUENCE = 'browserSyncOutgoingSequence';
const LAST_APPLIED = 'browserSyncLastAppliedSequence';
const PENDING_DELIVERY = 'browserSyncPendingDelivery';
const LAST_RECONCILE = 'browserSyncLastReconcileAt';
const ALLOW_OVERRIDE = 'browserSyncAllowOverwrite';
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
    const payload=await captureCookies(ctx.policy);
    const stored=(await chrome.storage.local.get(LAST_SEQUENCE))[LAST_SEQUENCE]||{};
    // The server is authoritative after a crash, upgrade or lost local
    // storage. A lower local counter must never cause silent replay failures.
    const cursorReply=await sendControl('SEQUENCE_CURSOR',{ruleId});
    const cursor=cursorReply?.lastSequence;
    if(cursorReply?.type!=='SESSION_CURSOR'||cursorReply.ruleId!==ruleId||
       !Number.isSafeInteger(cursor)||cursor<0) throw new Error('无法安全恢复会话序号');
    const previous=stored[ruleId]||0;
    if(!Number.isSafeInteger(previous)||previous<0) throw new Error('本地会话序号已损坏');
    const sequence=Math.max(previous,cursor)+1;
    if(!Number.isSafeInteger(sequence))throw new Error('会话序号耗尽，必须重新配对');
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
  const paused=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
  if(paused[ruleId])throw new Error('此规则因 Cookie 恢复不完整已暂停，请先检查目标网站');
  const answer=await sendControl('SYNC_REQUEST',{ruleId});
  if(answer.status!=='REQUESTED')throw new Error('来源设备不在线');
  return {state:'REQUESTED'};
}

export async function syncThenOpen(ruleId) {
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('只能由接收设备发起同步后打开');
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

async function handleIncomingSnapshot(envelope) {
  const ruleId=envelope?.ruleId;
  if(typeof ruleId!=='string'||ruleId.length>128)return;
  let result='FAILED';
  try{
    const ctx=await ruleContext(ruleId);
    if(!ctx.targetRole||ctx.remote.id!==envelope.sourceBrowserDeviceId ||
       ctx.rule.targetBrowserDeviceId!==envelope.targetBrowserDeviceId)
      throw new Error('接收设备身份不匹配');
    const paused=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
    if(paused[ruleId])throw new Error('PARTIAL_ROLLBACK: 接收端待人工检查');
    const sequences=(await chrome.storage.local.get(LAST_APPLIED))[LAST_APPLIED]||{};
    if(!Number.isSafeInteger(envelope.sequence)||envelope.sequence<=Number(sequences[ruleId]||0))
      throw new Error('旧会话快照已拒绝');
    const payload=await decryptSnapshot(envelope,ctx.remote);
    const switches=(await chrome.storage.local.get(ALLOW_OVERRIDE))[ALLOW_OVERRIDE]||{};
    try{
      await applyCookies(ruleId,payload,ctx.policy,{allowOverwrite:switches[ruleId]===true});
      sequences[ruleId]=envelope.sequence;
      await chrome.storage.local.set({[LAST_APPLIED]:sequences});
      result='APPLIED';
      const pending=(await chrome.storage.local.get(pendingOpenKey))[pendingOpenKey]||{};
      const request=pending[ruleId];
      if(request) {
        delete pending[ruleId];
        await chrome.storage.local.set({[pendingOpenKey]:pending});
        if(request.expiresAt>Date.now()&&request.origin===ctx.policy.siteOrigin)
          chrome.tabs.create({url:ctx.policy.siteOrigin}).catch(()=>{});
      }
    }catch(error){
      const code=String(error.message||'');
      if(code.startsWith('CONFLICT:'))result='CONFLICT';
      else throw error;
    }
    await setStatus(ruleId,result);
  }catch(error){
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
      await sendControl('SYNC_ACK',{ruleId,messageId:envelope.messageId,status:result});
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
  if(!cookie||cookie.path!=='/'||cookie.hostOnly!==true||
     cookie.secure!==true||cookie.partitionKey)return;
  const hostname=(cookie.domain||'').replace(/^\./,'').toLowerCase();
  const local=(await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers||{};
  for(const [ruleId,policy] of Object.entries(local)){
    if(new URL(policy.siteOrigin).hostname!==hostname || !policy.cookieNames.includes(cookie.name))continue;
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

export async function restoreActiveSubscriptions({force=false}={}) {
  const settings=await getSettings();
  if(!settings.serverOrigin)return;
  const now=Date.now();
  const last=(await chrome.storage.local.get(LAST_RECONCILE))[LAST_RECONCILE];
  if(!force&&!shouldReconcile(last,now))return;
  await reconcilePendingDeliveries();
  const identity=await getOrCreateIdentity();
  const rules=(await sendControl('LIST_RULES')).rules||[];
  // Network reconnection may trigger multiple async wakeups. Limit periodic
  // sync to five minutes, not every one-minute service worker alarm.
  await chrome.storage.local.set({[LAST_RECONCILE]:now});
  for(const r of rules){
    if(r.status==='active'&&r.targetBrowserDeviceId===identity.deviceId)
      await requestSnapshot(r.ruleId).catch(()=>{});
  }
}

export async function setOverwritePermission(ruleId,allowed) {
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('只能在接收设备确认覆盖');
  const flags=(await chrome.storage.local.get(ALLOW_OVERRIDE))[ALLOW_OVERRIDE]||{};
  flags[ruleId]=allowed===true;
  await chrome.storage.local.set({[ALLOW_OVERRIDE]:flags});
  return {ruleId,allowed:flags[ruleId]};
}

export async function resumePausedRestore(ruleId,confirmed) {
  if(confirmed!==true)throw new Error('需要确认已检查目标网站的登录状态');
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('仅接收设备可以恢复');
  const flags=(await chrome.storage.local.get(PAUSED_RESTORE))[PAUSED_RESTORE]||{};
  if(!flags[ruleId])throw new Error('当前规则未处于暂停状态');
  delete flags[ruleId];
  await chrome.storage.local.set({[PAUSED_RESTORE]:flags});
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
      pendingOpenKey,PENDING_DELIVERY,PAUSED_RESTORE,'browserSyncManagedCookieHmacV1','browserSyncLocalOffers']){
    const values=(await chrome.storage.local.get(key))[key]||{};
    if(Object.hasOwn(values,ruleId)){
      delete values[ruleId];
      await chrome.storage.local.set({[key]:values});
    }
  }
}
