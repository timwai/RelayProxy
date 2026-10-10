// Agentless development milestone: device identity + local website rules.
// Registration and signed WSS authentication are implemented; remote Cookie
// transfer is intentionally disabled until mutual approval and E2EE exist.
import { getOrCreateIdentity } from './device-identity.js';
import { getSettings, updateSettings, parseServerOrigin } from './rules.js';
import { registerBrowser, connectBrowser, currentConnectionState, disconnect } from './server-api.js';

async function initialize() {
  await getOrCreateIdentity();
  await chrome.alarms.create('browser-sync-reconcile', { periodInMinutes: 15 });
}

chrome.runtime.onInstalled.addListener(() => { initialize().catch(console.error); });
chrome.runtime.onStartup.addListener(() => { initialize().catch(console.error); });

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name !== 'browser-sync-reconcile') return;
  // Future: request authorized rule versions after WSS reauthentication.
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
      default:
        throw new Error('Unsupported extension message');
    }
  })().then(sendResponse, error => sendResponse({ ok: false, error: error.message }));
  return true;
});
