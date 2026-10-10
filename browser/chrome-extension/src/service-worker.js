// Agentless development milestone: device identity + local website rules.
// Registration and signed WSS authentication are implemented; remote Cookie
// transfer is intentionally disabled until mutual approval and E2EE exist.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, updateSettings, parseServerOrigin } from './rules.js';
import { registerBrowser, connectBrowser, currentConnectionState, disconnect, sendControl } from './server-api.js';
import { createEncryptedOffer, decryptEncryptedOffer, pairingCode } from './envelope.js';
import { getTrustedPeer, pinPeer } from './rules.js';

async function initialize() {
  await getOrCreateIdentity();
  await chrome.alarms.create('browser-sync-reconcile', { periodInMinutes: 15 });
}

chrome.runtime.onInstalled.addListener(() => { initialize().catch(console.error); });
chrome.runtime.onStartup.addListener(() => { initialize().catch(console.error); });

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== 'browser-sync-reconcile') return;
  // Periodic reauthentication is opt-in after the user establishes a browser pairing.
});

chrome.cookies.onChanged.addListener(async ({ cookie }) => {
  // Observe *metadata only* in development. We never read or send values
  // until a site rule has been approved on both browser devices.
  const settings = await getSettings();
  const hostname = cookie.domain.replace(/^\./, '');
  if (!settings.sites.some(origin => new URL(origin).hostname === hostname)) return;
  await chrome.storage.local.set({ browserSyncLastLocalCookieChange: Date.now() });
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (sender.id !== chrome.runtime.id) return;
  (async () => {
    switch (message?.type) {
      case 'GET_STATUS': {
        const [identity, settings] = await Promise.all([getOrCreateIdentity(), getSettings()]);
        return { ok: true, identity, settings, connected: currentConnectionState() === 'AUTHENTICATED', stage: currentConnectionState() };
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
          return { source, policy };
        })());
        const origin = summary.policy.siteOrigin;
        if (!(await chrome.permissions.contains({ origins: [origin + '/*'] }))) {
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
        await sendControl('RULE_CONFIRM', { ruleId: message.ruleId });
        return { ok: true };
      }
      case 'REVOKE_RULE':
        await sendControl('RULE_REVOKE', { ruleId: message.ruleId });
        return { ok: true };
      default:
        throw new Error('Unsupported extension message');
    }
  })().then(sendResponse, error => sendResponse({ ok: false, error: error.message }));
  return true;
});
