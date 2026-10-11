// Agentless development milestone: device identity + local website rules.
// Session transfer uses existing mutual pairing, E2EE and precise cookie scopes.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, updateSettings, parseServerOrigin } from './rules.js';
import { registerBrowser, connectBrowser, currentConnectionState, disconnect, sendControl } from './server-api.js';
import { createEncryptedOffer, decryptEncryptedOffer, pairingCode } from './envelope.js';
import { getTrustedPeer, pinPeer } from './rules.js';
import { sendSnapshot, requestSnapshot, onCookieChange, restoreActiveSubscriptions, setOverwritePermission, syncThenOpen, forgetRuleLocalState, resumePausedRestore } from './session-sync.js';
import {parseBoundHost, installNetworkGuard, startNetworkGuard, startLocalNetworkGuard, stopNetworkGuard, selectedNetworkRules, refreshNetworkBinding, registerNetworkAuth} from './network-binding.js';

registerNetworkAuth();

async function initialize() {
  await getOrCreateIdentity();
  // Remove legacy unsalted SHA-256 Cookie fingerprints on extension startup.
  await chrome.storage.local.remove('browserSyncManagedCookieHashes');
  await chrome.alarms.create('browser-sync-reconcile', { periodInMinutes: 1 });
  await installNetworkGuard();
  await refreshNetworkBinding({force:true}).catch(()=>{});
  restoreActiveSubscriptions({force:true}).catch(() => {});
}

chrome.runtime.onInstalled.addListener(() => { initialize().catch(console.error); });
chrome.runtime.onStartup.addListener(() => { initialize().catch(console.error); });

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== 'browser-sync-reconcile') return;
  refreshNetworkBinding().catch(()=>{});
  restoreActiveSubscriptions().catch(() => {});
});

chrome.cookies.onChanged.addListener(({ cookie }) => {
  onCookieChange(cookie).catch(() => {});
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (sender.id !== chrome.runtime.id) return;
  (async () => {
    switch (message?.type) {
      case 'GET_STATUS': {
        const [identity, settings] = await Promise.all([getOrCreateIdentity(), getSettings()]);
        return { ok: true, identity, settings, connected: currentConnectionState() === 'AUTHENTICATED', stage: currentConnectionState(), transferEnabled: true };
      }
      case 'SAVE_SERVER': {
        const serverOrigin = parseServerOrigin(message.serverOrigin);
        disconnect();
        return { ok: true, settings: await updateSettings({ serverOrigin }) };
      }
      case 'REGISTER_BROWSER': {
        const result = await registerBrowser();
        return { ok: true, ...result };
      }
      case 'CONNECT_BROWSER': {
        const result = await connectBrowser();
        return { ok: true, ...result };
      }
      case 'LIST_PEERS': {
        const answer = await sendControl('LIST_PEERS');
        return { ok: true, peers: answer.peers || [] };
      }
      case 'LIST_RULES': {
        const identity = await getOrCreateIdentity();
        const [peersAnswer, rulesAnswer] = await Promise.all([
          sendControl('LIST_PEERS'), sendControl('LIST_RULES')
        ]);
        const peers = peersAnswer.peers || [];
        const rules = [];
        for (const rule of rulesAnswer.rules || []) {
          const remoteID = rule.sourceBrowserDeviceId === identity.deviceId
            ? rule.targetBrowserDeviceId : rule.sourceBrowserDeviceId;
          const remote = peers.find(p => p.id === remoteID);
          const mine = {
            id: identity.deviceId, signingPublicKey: identity.signingPublicKey,
            encryptionPublicKey: identity.encryptionPublicKey
          };
          const summary = { id: rule.ruleId, status: rule.status,
            role: rule.sourceBrowserDeviceId === identity.deviceId ? 'source' : 'target',
            sourceApproved: rule.sourceApproved, targetApproved: rule.targetApproved,
            keysConfirmed: rule.keysConfirmed, remoteID, valid: false };
          if (remote) {
            try {
              const pinned = await getTrustedPeer(remote.id);
              if (pinned && (pinned.signingPublicKey !== remote.signingPublicKey ||
                pinned.encryptionPublicKey !== remote.encryptionPublicKey)) {
                throw new Error('设备公钥已改变');
              }
              summary.code = await pairingCode(rule.ruleId,
                summary.role === 'source' ? mine : remote,
                summary.role === 'source' ? remote : mine);
              if (summary.role === 'target') {
                summary.policy = await decryptEncryptedOffer(rule, remote);
              } else {
                const local = (await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers || {};
                summary.policy = local[rule.ruleId] || null;
              }
              summary.valid = !!summary.policy;
            } catch (error) { summary.error = String(error.message || error); }
          }
          rules.push(summary);
        }
        for (const summary of rules) {
          if (!summary.valid || summary.status!=='active')continue;
          try {
            const binding=(await sendControl('NETWORK_BINDING',{ruleId:summary.id})).binding;
            if(binding && binding.status!=='none')
              summary.network={...binding,matchingHost:binding.host===parseBoundHost(summary.policy.siteOrigin)};
          }catch{ /* network proxy is optional and disabled by default */ }
        }
        const desired=await selectedNetworkRules();
        for(const summary of rules){
          if(summary.role==='target'&&summary.valid&&summary.status==='active'&&desired[summary.id]){
            summary.localNetworkMode=desired[summary.id].mode||'source_agent';
            summary.localNetworkPort=desired[summary.id].port||null;
          }
        }
        const transfer = (await chrome.storage.local.get('browserSyncTransferStatus')).browserSyncTransferStatus || {};
        const paused = (await chrome.storage.local.get('browserSyncRestorePaused')).browserSyncRestorePaused || {};
        const interrupted = (await chrome.storage.local.get('browserSyncRestoreJournal')).browserSyncRestoreJournal || {};
        for (const summary of rules) {
          summary.lastTransfer = paused[summary.id] || interrupted[summary.id]
            ? {state:'PARTIAL',time:Date.now()} : (transfer[summary.id] || null);
        }
        return { ok: true, rules };
      }
      case 'CREATE_RULE': {
        const settings = await getSettings();
        if (!settings.sites.includes(message.siteOrigin)) throw new Error('请先授权站点');
        const peers = (await sendControl('LIST_PEERS')).peers || [];
        const peer = peers.find(p => p.id === message.targetID && p.receive);
        if (!peer) throw new Error('接收设备未审批或不可用');
        const pinned = await getTrustedPeer(peer.id);
        if (pinned && (pinned.signingPublicKey !== peer.signingPublicKey ||
          pinned.encryptionPublicKey !== peer.encryptionPublicKey)) {
          throw new Error('目标设备密钥变更');
        }
        const result = await createEncryptedOffer({
          target: peer, siteOrigin: message.siteOrigin, cookieNames: message.cookieNames
        });
        const response = await sendControl('RULE_OFFER', { offer: result.offer });
        const saved = (await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers || {};
        saved[result.offer.ruleId] = result.policy;
        await chrome.storage.local.set({ browserSyncLocalOffers: saved });
        await pinPeer(peer);
        return { ok: true, ruleId: result.offer.ruleId, status: response.status };
      }
      case 'ACCEPT_RULE': {
        const summary = (await (async () => {
          const identity = await getOrCreateIdentity();
          const peers = (await sendControl('LIST_PEERS')).peers || [];
          const rules = (await sendControl('LIST_RULES')).rules || [];
          const rule = rules.find(v => v.ruleId === message.ruleId &&
            v.targetBrowserDeviceId === identity.deviceId && v.status === 'offered');
          if (!rule) throw new Error('邀请不存在或已过期');
          const source = peers.find(p => p.id === rule.sourceBrowserDeviceId);
          if (!source) throw new Error('找不到来源设备');
          const policy = await decryptEncryptedOffer(rule, source);
          const pinned = await getTrustedPeer(source.id);
          if (pinned && (pinned.signingPublicKey !== source.signingPublicKey ||
            pinned.encryptionPublicKey !== source.encryptionPublicKey)) throw new Error('来源密钥已变更');
          if (!message.confirmed) throw new Error('请先核对配对校验码');
          const local = await getOrCreateIdentity();
          const code = await pairingCode(rule.ruleId, source, {
            id: local.deviceId, signingPublicKey: local.signingPublicKey,
            encryptionPublicKey: local.encryptionPublicKey
          });
          if (code !== message.pairingCode) throw new Error('配对校验码已改变，请重新核对');
          return { source, policy };
        })());
        const origin = summary.policy.siteOrigin;
        const url = new URL(origin);
        if (!(await chrome.permissions.contains({ origins: [`${url.protocol}//${url.hostname}/*`] }))) {
          throw new Error('请先在扩展中授予接收网站权限');
        }
        await sendControl('RULE_ACCEPT', { ruleId: message.ruleId });
        await pinPeer(summary.source);
        const settings = await getSettings();
        if (!settings.sites.includes(origin)) await updateSettings({ sites: [...settings.sites, origin] });
        return { ok: true };
      }
      case 'CONFIRM_RULE': {
        if (!message.confirmed) throw new Error('请先核对配对校验码');
        const identity = await getOrCreateIdentity();
        const rules = (await sendControl('LIST_RULES')).rules || [];
        const rule = rules.find(v => v.ruleId === message.ruleId &&
          v.sourceBrowserDeviceId === identity.deviceId && v.targetApproved);
        if (!rule) throw new Error('接收端尚未确认');
        const peers = (await sendControl('LIST_PEERS')).peers || [];
        const remote = peers.find(p => p.id === rule.targetBrowserDeviceId);
        const pinned = remote && await getTrustedPeer(remote.id);
        if (!remote || !pinned || pinned.signingPublicKey !== remote.signingPublicKey ||
          pinned.encryptionPublicKey !== remote.encryptionPublicKey) throw new Error('接收方密钥校验失败');
        const code = await pairingCode(rule.ruleId, {
          id: identity.deviceId, signingPublicKey: identity.signingPublicKey,
          encryptionPublicKey: identity.encryptionPublicKey
        }, remote);
        if (code !== message.pairingCode) throw new Error('配对校验码已改变，请重新核对');
        await sendControl('RULE_CONFIRM', { ruleId: message.ruleId });
        return { ok: true };
      }
      case 'REVOKE_RULE':
        await sendControl('RULE_REVOKE', { ruleId: message.ruleId });
        await stopNetworkGuard(message.ruleId);
        await forgetRuleLocalState(message.ruleId);
        return { ok: true };
      case 'NETWORK_EXITS': {
        return {ok:true,exits:(await sendControl('NETWORK_EXITS')).exits||[]};
      }
      case 'NETWORK_OFFER': {
        const identity=await getOrCreateIdentity();
        const rules=(await sendControl('LIST_RULES')).rules||[];
        const rule=rules.find(r=>r.ruleId===message.ruleId && r.status==='active' &&
          r.sourceBrowserDeviceId===identity.deviceId);
        if(!rule)throw new Error('仅来源 Chrome 可以选择 Agent 出口');
        const saved=(await chrome.storage.local.get('browserSyncLocalOffers')).browserSyncLocalOffers||{};
        if(!saved[message.ruleId])throw new Error('来源站点策略已丢失');
        const host=parseBoundHost(saved[message.ruleId].siteOrigin);
        const exits=(await sendControl('NETWORK_EXITS')).exits||[];
        if(!exits.some(e=>e.id===message.exitDeviceId))throw new Error('Agent 出口未在线或未授权');
        await sendControl('NETWORK_OFFER',{ruleId:message.ruleId,host,exitDeviceId:message.exitDeviceId});
        return {ok:true};
      }
      case 'NETWORK_ACCEPT': {
        if(message.confirmed!==true)throw new Error('需要接收端明确确认');
        const identity=await getOrCreateIdentity();
        const rules=(await sendControl('LIST_RULES')).rules||[];
        const rule=rules.find(r=>r.ruleId===message.ruleId && r.status==='active' &&
          r.targetBrowserDeviceId===identity.deviceId);
        if(!rule)throw new Error('网络规则无效或未激活');
        const peers=(await sendControl('LIST_PEERS')).peers||[];
        const source=peers.find(p=>p.id===rule.sourceBrowserDeviceId);
        const pinned=source&&await getTrustedPeer(source.id);
        if(!pinned || pinned.signingPublicKey!==source.signingPublicKey ||
          pinned.encryptionPublicKey!==source.encryptionPublicKey)
          throw new Error('来源设备密钥未经核对');
        const policy=await decryptEncryptedOffer(rule,source);
        const host=parseBoundHost(policy.siteOrigin);
        const binding=(await sendControl('NETWORK_BINDING',{ruleId:message.ruleId})).binding;
        if(binding?.status!=='offered'||binding.host!==host)
          throw new Error('来源网络与加密站点策略不匹配');
        // Guard before approval: even a failed network lease cannot expose B's IP.
        const desired=(await chrome.storage.local.get('browserSyncNetworkDesired')).browserSyncNetworkDesired||{};
        desired[message.ruleId]={siteOrigin:policy.siteOrigin};
        await chrome.storage.local.set({browserSyncNetworkDesired:desired});
        await installNetworkGuard();
        await sendControl('NETWORK_ACCEPT',{ruleId:message.ruleId,host});
        return {ok:true,...await startNetworkGuard(message.ruleId,policy.siteOrigin)};
      }
      case 'NETWORK_REVOKE':
        await sendControl('NETWORK_REVOKE',{ruleId:message.ruleId});
        if((await selectedNetworkRules())[message.ruleId]?.mode!=='local_agent')
          await stopNetworkGuard(message.ruleId);
        return {ok:true};
      case 'NETWORK_LOCAL_ENABLE':{
        if(message.confirmed!==true)throw new Error('必须确认使用 B 设备本机 Agent');
        const identity=await getOrCreateIdentity();
        const rules=(await sendControl('LIST_RULES')).rules||[];
        const rule=rules.find(r=>r.ruleId===message.ruleId&&r.status==='active'&&
          r.targetBrowserDeviceId===identity.deviceId&&r.keysConfirmed===true);
        if(!rule)throw new Error('只能为已确认的接收端 Cookie 规则选择本机 Agent');
        const peers=(await sendControl('LIST_PEERS')).peers||[];
        const source=peers.find(p=>p.id===rule.sourceBrowserDeviceId);
        const pinned=source&&await getTrustedPeer(source.id);
        if(!pinned||pinned.signingPublicKey!==source.signingPublicKey||
          pinned.encryptionPublicKey!==source.encryptionPublicKey)
          throw new Error('来源端设备密钥未经核对');
        const policy=await decryptEncryptedOffer(rule,source);
        const settings=await getSettings();
        if(!settings.sites.includes(policy.siteOrigin))
          throw new Error('站点权限未授权');
        const port=Number(message.port);
        const result=await startLocalNetworkGuard(message.ruleId,policy.siteOrigin,port,message.token);
        return {ok:true,...result};
      }
      case 'NETWORK_LOCAL_DISABLE':
        await stopNetworkGuard(message.ruleId);
        return {ok:true};
      case 'NETWORK_USE_SOURCE':{
        const ruleID=message.ruleId;
        const identity=await getOrCreateIdentity();
        const rules=(await sendControl('LIST_RULES')).rules||[];
        const rule=rules.find(r=>r.ruleId===ruleID&&r.status==='active'&&
          r.targetBrowserDeviceId===identity.deviceId&&r.keysConfirmed===true);
        if(!rule)throw new Error('来源网络规则不可用');
        const peer=(await sendControl('LIST_PEERS')).peers?.find(p=>p.id===rule.sourceBrowserDeviceId);
        const pinned=peer&&await getTrustedPeer(peer.id);
        if(!pinned||pinned.signingPublicKey!==peer.signingPublicKey||
          pinned.encryptionPublicKey!==peer.encryptionPublicKey)
          throw new Error('来源端公钥校验失败');
        const policy=await decryptEncryptedOffer(rule,peer);
        const binding=(await sendControl('NETWORK_BINDING',{ruleId:ruleID})).binding;
        if(binding?.status!=='active'||binding.host!==parseBoundHost(policy.siteOrigin))
          throw new Error('A 的网络绑定尚未授权或已经撤销');
        await startNetworkGuard(ruleID,policy.siteOrigin);
        return {ok:true};
      }
      case 'SEND_SESSION':
        return { ok: true, ...(await sendSnapshot(message.ruleId)) };
      case 'REQUEST_SESSION':
        return { ok: true, ...(await requestSnapshot(message.ruleId)) };
      case 'SYNC_THEN_OPEN':
        return { ok: true, ...(await syncThenOpen(message.ruleId)) };
      case 'ALLOW_OVERWRITE':
        return { ok: true, ...(await setOverwritePermission(message.ruleId, message.allowed)) };
      case 'RESUME_PARTIAL_RESTORE':
        return { ok: true, ...(await resumePausedRestore(message.ruleId, message.confirmed)) };
      default:
        throw new Error('Unsupported extension message');
    }
  })().then(sendResponse, error => sendResponse({ ok: false, error: error.message }));
  return true;
});
