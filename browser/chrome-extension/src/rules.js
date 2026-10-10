// Only explicitly authorized HTTPS origins may be saved.
// Never persist secret session values in a rule.
const SETTINGS_KEY = 'browserSyncSettings';

export function parseServerOrigin(raw) {
  const url = new URL(raw);
  if (url.protocol !== 'https:' || url.username || url.password ||
      url.search || url.hash || url.pathname !== '/') {
    throw new Error('Server must be an HTTPS origin without path, credentials or query');
  }
  return url.origin;
}

export function parseSiteOrigin(raw) {
  const url = new URL(raw);
  if (url.protocol !== 'https:' || url.username || url.password ||
      url.search || url.hash || url.pathname !== '/') {
    throw new Error('Only HTTPS website origins are supported');
  }
  return url.origin;
}

export async function getSettings() {
  const saved = (await chrome.storage.local.get(SETTINGS_KEY))[SETTINGS_KEY] || {};
  return {
    serverOrigin: saved.serverOrigin || '',
    sites: Array.isArray(saved.sites) ? saved.sites : [],
  };
}

export async function updateSettings(patch) {
  const next = { ...await getSettings(), ...patch };
  await chrome.storage.local.set({ [SETTINGS_KEY]: next });
  return next;
}

const TRUSTED_KEY = 'browserSyncTrustedPeers';

export async function getTrustedPeer(id) {
  const state = (await chrome.storage.local.get(TRUSTED_KEY))[TRUSTED_KEY] || {};
  return state[id] || null;
}
export async function pinPeer(peer) {
  const state = (await chrome.storage.local.get(TRUSTED_KEY))[TRUSTED_KEY] || {};
  const existing = state[peer.id];
  const pinned = { id: peer.id, signingPublicKey: peer.signingPublicKey,
    encryptionPublicKey: peer.encryptionPublicKey };
  if (existing && (existing.signingPublicKey !== pinned.signingPublicKey ||
    existing.encryptionPublicKey !== pinned.encryptionPublicKey)) {
    throw new Error('设备公钥发生变化，需要撤销配对并重新验证');
  }
  state[peer.id] = pinned;
  await chrome.storage.local.set({ [TRUSTED_KEY]: state });
  return pinned;
}
