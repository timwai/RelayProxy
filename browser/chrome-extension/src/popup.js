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
  $('extension-id').textContent = chrome.runtime.id;
  $('server').value = result.settings.serverOrigin;
  renderSites(result.settings.sites);
}

$('save').addEventListener('click', async () => {
  try {
    const raw = $('server').value.trim();
    const origin = new URL(raw);
    if (origin.protocol !== 'https:' || origin.pathname !== '/' ||
        origin.search || origin.hash || origin.username || origin.password) {
      throw new Error('仅支持 HTTPS Server Origin');
    }
    // Explicit click grants the extension access to the Admin HTTPS origin.
    const pattern = `${origin.protocol}//${origin.hostname}/*`; // Chrome host permission patterns omit ports
    const granted = await chrome.permissions.request({ origins: [pattern] });
    if (!granted) { status('未授予 Server 访问权限。'); return; }
    const response = await chrome.runtime.sendMessage({
      type: 'SAVE_SERVER', serverOrigin: raw
    });
    if (!response?.ok) throw new Error(response?.error || 'Unable to save address');
    status('Server 地址已保存，待后端认证接口实现后可注册设备。');
  } catch (error) { failure(error); }
});

$('add-site').addEventListener('click', async () => {
  try {
    const origin = parseSiteOrigin($('site').value.trim());
    // Call request() directly from a user click; never grant silently.
    const websiteURL = new URL(origin);
    const pattern = `${websiteURL.protocol}//${websiteURL.hostname}/*`;
    const granted = await chrome.permissions.request({ origins: [pattern] });
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

$('register').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'REGISTER_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '设备注册失败');
    status(result.state === 'pending'
      ? '已注册，等待管理员在 Server 后台审批。'
      : '浏览器设备状态：' + result.state);
  } catch (error) { failure(error); }
});

$('connect').addEventListener('click', async () => {
  try {
    const result = await chrome.runtime.sendMessage({ type: 'CONNECT_BROWSER' });
    if (!result?.ok) throw new Error(result?.error || '连接失败');
    status('浏览器设备已认证，尚未开启 Cookie 同步。');
  } catch (error) { failure(error); }
});

load().catch(failure);
