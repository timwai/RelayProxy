// MV3 worker controller for approved A -> B Cookie-only sync.
// No credentials are persisted: snapshots exist in memory solely until the
// signed encrypted message has been produced.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, getTrustedPeer } from './rules.js';
import { sendControl, onServerPush } from './server-api.js';
import { decryptEncryptedOffer } from './envelope.js';
import { encryptSnapshot, decryptSnapshot } from './session-envelope.js';
import { captureCookies, applyCookies } from './cookies.js';

const sourceBusy = new Set();
const cookieTimers = new Map();
const LAST_SEQUENCE = 'browserSyncOutgoingSequence';
const LAST_APPLIED = 'browserSyncLastAppliedSequence';
const ALLOW_OVERRIDE = 'browserSyncAllowOverwrite';
const statusKey = 'browserSyncTransferStatus';

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
  if(sourceBusy.has(ruleId))return {state:'BUSY'};
  sourceBusy.add(ruleId);
  try{
    const ctx=await ruleContext(ruleId);
    if(!ctx.sourceRole)throw new Error('只有来源设备可以发送登录状态');
    const payload=await captureCookies(ctx.policy);
    const stored=(await chrome.storage.local.get(LAST_SEQUENCE))[LAST_SEQUENCE]||{};
    const sequence=(stored[ruleId]||0)+1;
    if(!Number.isSafeInteger(sequence))throw new Error('会话序号耗尽');
    // Reserve BEFORE asynchronous network delivery; no repeated nonce or
    // sequence even if WSS rejects the delivery or worker suspends.
    stored[ruleId]=sequence;
    await chrome.storage.local.set({[LAST_SEQUENCE]:stored});
    const envelope=await encryptSnapshot(ctx.rule,ctx.remote,sequence,payload);
    const ack=await sendControl('SESSION_SNAPSHOT',{envelope});
    if(ack.status!=='RELAYED')throw new Error('中继没有接受加密快照');
    await setStatus(ruleId,'RELAYED');
    return {state:'RELAYED',count:payload.cookies.length};
  }catch(error){
    await setStatus(ruleId,'FAILED');
    throw error;
  }finally{sourceBusy.delete(ruleId);}
}

export async function requestSnapshot(ruleId) {
  const ctx=await ruleContext(ruleId);
  if(!ctx.targetRole)throw new Error('只有接收设备能请求同步');
  const answer=await sendControl('SYNC_REQUEST',{ruleId});
  if(answer.status!=='REQUESTED')throw new Error('来源设备不在线');
  return {state:'REQUESTED'};
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
    }catch(error){
      if(String(error.message||'').startsWith('CONFLICT:'))result='CONFLICT';
      else throw error;
    }
    await setStatus(ruleId,result);
  }catch{
    // Never log or expose snapshot contents in errors.
    await setStatus(ruleId,'FAILED');
  }
  try{
    if(typeof envelope.messageId==='string')
      await sendControl('SYNC_ACK',{ruleId,messageId:envelope.messageId,status:result});
  }catch{ /* peer may be offline; session values are still never logged */ }
}

onServerPush(async message => {
  if(message.type==='SESSION_SNAPSHOT')return handleIncomingSnapshot(message.envelope);
  if(message.type==='SYNC_REQUEST'&&typeof message.ruleId==='string'){
    try{await sendSnapshot(message.ruleId);}catch{ /* offline or Cookie scope incompatible */ }
  }
  if(message.type==='SYNC_ACK'&&typeof message.ruleId==='string'&&
    ['RECEIVED','APPLIED','FAILED','CONFLICT'].includes(message.status))
    await setStatus(message.ruleId,message.status);
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

export async function restoreActiveSubscriptions() {
  const settings=await getSettings();
  if(!settings.serverOrigin)return;
  const identity=await getOrCreateIdentity();
  const rules=(await sendControl('LIST_RULES')).rules||[];
  for(const r of rules){
    if(r.status==='active' && r.targetBrowserDeviceId===identity.deviceId)
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
