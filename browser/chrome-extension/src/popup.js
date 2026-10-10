import { parseSiteOrigin, getSettings, updateSettings } from './rules.js';

const $ = id => document.getElementById(id);
const status = text => { $('message').textContent = text; $('message').className = ''; };
const failure = error => { $('message').textContent = String(error.message || error); $('message').className = 'error'; };

function renderSites(sites) {
  const list = $('sites');
  list.replaceChildren();
  for (const origin of sites) {
    const li = document.createElement('li');
    li.textContent = origin;
    list.append(li);
  }
}

async function load() {
  const result = await chrome.runtime.sendMessage({ type: 'GET_STATUS' });
  if (!result?.ok) throw new Error(result?.error || 'Extension background unavailable');
  $('device').textContent = result.identity.deviceId;
  $('server').value = result.settings.serverOrigin;
  renderSites(result.settings.sites);
}

$('save').addEventListener('click', async () => {
  try {
    const response = await chrome.runtime.sendMessage({
      type: 'SAVE_SERVER', serverOrigin: $('server').value.trim()
    });
    if (!response?.ok) throw new Error(response?.error || 'Unable to save address');
    status('Server 地址已保存，待后端认证接口实现后可注册设备。');
  } catch (error) { failure(error); }
});

$('add-site').addEventListener('click', async () => {
  try {
    const origin = parseSiteOrigin($('site').value.trim());
    // Call request() directly from a user click; never grant silently.
    const granted = await chrome.permissions.request({ origins: [origin + '/*'] });
    if (!granted) return status('站点权限未获授予。');
    const settings = await getSettings();
    if (!settings.sites.includes(origin)) {
      settings.sites.push(origin);
      await updateSettings({ sites: settings.sites });
    }
    renderSites(settings.sites);
    status('站点权限已授予。尚未启用会话传输。');
  } catch (error) { failure(error); }
});

load().catch(failure);
