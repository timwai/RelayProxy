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
      url.search || url.hash) {
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
