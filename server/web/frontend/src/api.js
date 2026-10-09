export async function api(path, options={}) {
  const headers = { Accept: 'application/json', ...options.headers };
  if (options.body != null) headers['Content-Type'] = 'application/json';
  const result = await fetch('/api/v1' + path, {
    credentials: 'same-origin', cache: 'no-store', ...options, headers
  });
  let body;
  try { body = await result.json(); }
  catch { throw new Error(`服务端响应无效（HTTP ${result.status}）`); }
  if (!result.ok) {
    const error = new Error(body?.error || `请求失败（HTTP ${result.status}）`);
    error.status = result.status;
    throw error;
  }
  return body;
}
export const json = (method, body) => ({ method, body: JSON.stringify(body) });
export const list = x => Array.isArray(x) ? x : [];
export const fmtDate = x => x && !Number.isNaN(Date.parse(x)) ? new Date(x).toLocaleString('zh-CN', {hour12:false}) : '—';
export function fmtBytes(value) { let n = Math.max(0,Number(value)||0),i=0;const units=['B','KiB','MiB','GiB','TiB'];while(n>=1024&&i<units.length-1){n/=1024;i++;}return `${n.toFixed(i?1:0)} ${units[i]}`; }
export const localDate = v => v ? new Date(new Date(v).getTime()-new Date(v).getTimezoneOffset()*60000).toISOString().slice(0,16) : '';
export const isoDate = v => v ? new Date(v).toISOString() : '';
export function normalizedCapabilities(input) {
  const set = new Set(list(input));
  if (set.has('rdp.public')) set.add('rdp.host');
  return [...set];
}
export const permissionDefs = [
  ['proxy.client','代理客户端'],['proxy.exit','出口节点'],['rdp.controller','RDP 控制端'],['rdp.host','RDP 主机'],['rdp.public','RDP 公网入口']
];
export const validChannelId = x => /^[A-Za-z0-9._-]{1,80}$/.test(x);
export function validatePortRange(cfg) {
  for(const group of ['direct','p2p','rdpIngress']) {
    const a=Number(cfg[group]?.portStart),b=Number(cfg[group]?.portEnd);
    if(a>b || ((a===0)!==(b===0))) throw new Error(`${group} UDP/端口范围起始必须小于等于结束，随机端口需填写 0 / 0`);
  }
}

// Public pushes are served on the Relay TCP listener, not the Admin listener.
export function pushURL(id, info, hostname = (typeof location === 'undefined' ? '' : location.hostname)) {
  const listen = info?.tcpListen;
  const match = /^(?:\[[^\]]+\]|[^:]*):(\d{1,5})$/.exec(String(listen||'').trim());
  if (!match) return '';
  const port = Number(match[1]);
  const secure = !!info.tlsEnabled;
  const host = hostname.includes(':') && !hostname.startsWith('[') ? '['+hostname+']' : hostname;
  const suffix = ((secure&&port===443)||(!secure&&port===80))?'':':'+port;
  return (secure?'https://':'http://')+host+suffix+'/api/v1/push/'+encodeURIComponent(id);
}