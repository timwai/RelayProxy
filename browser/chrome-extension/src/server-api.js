// Public browser-device transport; no local Agent or native host.
// No cookies/credentials are sent at this stage.
import { getOrCreateIdentity, signChallenge } from './device-identity.js';
import { getSettings } from './rules.js';

const endpoint = (base, path) => new URL(path, base).toString();

// Only the status of this non-authenticated probe is inspected. Never fetch
// or log browser Cookie values, auth tokens, challenge signatures or keys.
export function browserRelayHealthMessage(status) {
  if (status === 204) return 'Browser Sync TLS 接口返回 HTTP 204：只代表 HTTPS 入口可达，无法证明 WebSocket Upgrade 或设备认证成功。请检查 WSS Upgrade、浏览器设备审批与 Chrome 代理设置。';
  if (status === 404) return '当前地址没有 Browser Sync 中继接口（HTTP 404）。请确认扩展配置的是 Server 的 TLS/TCP 中继端口，并已部署新版 Server、启用 browser_sync。';
  if (status === 426) return '中继接口要求 HTTPS/TLS（HTTP 426）；请检查 TLS 终止与转发配置。';
  if (status >= 500) return 'Browser Sync 中继服务返回 HTTP ' + status + '，请检查 Server 日志与服务健康状态。';
  return '中继地址返回 HTTP ' + status + '，但未确认 Browser Sync TLS/WSS 可用；请核实 Server 版本和接入端口。';
}

// Socket events are the only reliable source of the WebSocket handshake
// stage. The anonymous HTTPS /health endpoint must never be treated as an
// authentication probe, and a successful 204 cannot establish 101 Upgrade.
export function browserWSSFailureMessage(stage, code=0) {
  const suffix=code ? '（WebSocket close '+code+'）' : '';
  if(stage==='proof')return 'WSS Upgrade 和 AUTH_CHALLENGE 已成功，但 AUTH_PROOF 未获认证'+suffix+'。请在 Server Web 核实浏览器设备仍获批准、有 send/receive 权限，并检查 Server 与扩展的版本及 Origin 签名是否一致。';
  if(stage==='challenge')return 'WSS Upgrade 已成功，收到 AUTH_CHALLENGE 但未完成 AUTH_PROOF'+suffix+'。请检查浏览器设备的签名密钥、扩展 Service Worker 和 Server 认证日志。';
  if(stage==='hello')return 'WSS Upgrade 已成功，但 AUTH_HELLO 未收到挑战响应'+suffix+'。请检查 Browser Sync 的连接限流、设备 ID 与 Server 日志。';
  return 'WebSocket 尚未触发 open'+suffix+'。请检查浏览器设备是否获 Server 批准，以及中继链路是否保留 HTTP/1.1 Upgrade。';
}

async function diagnoseBrowserWSS(serverOrigin) {
  try {
    const response = await fetch(endpoint(serverOrigin, '/api/v1/browser-sync/health'), {
      method: 'GET', cache: 'no-store', redirect: 'error',
      signal: AbortSignal.timeout(5000),
    });
    return new Error(browserRelayHealthMessage(response.status));
  } catch {
    return new Error('无法连接 Browser Sync TLS/TCP 中继。检查 Server TCP 监听端口、防火墙、DNS、Chrome 代理设置及 HTTPS 证书信任；此地址不是 Admin/QUIC 端口。');
  }
}

export async function registerBrowser() {
  const settings = await getSettings();
  if (!settings.serverOrigin) throw new Error('请先配置 HTTPS Server');
  const identity = await getOrCreateIdentity();
  const requestedIdentityId = (settings.requestedIdentityId || '').trim();
  const deviceName = (settings.deviceName || '').trim();
  if (!requestedIdentityId || !deviceName || deviceName.length > 100 || requestedIdentityId.length > 128) throw new Error('请先填写有效的身份 ID 和设备名称');
  const registration = {
    id: identity.deviceId,
    name: deviceName,
    requestedIdentityId,
    signingPublicKey: identity.signingPublicKey,
    encryptionPublicKey: identity.encryptionPublicKey,
  };
  // A single signed-key device request. Origin is recorded for binding,
  // not independently approved; older servers must be upgraded.
  const response = await fetch(endpoint(settings.serverOrigin,
    '/api/v1/browser-sync/devices/register'), {
    method:'POST', cache:'no-store', headers:{'Content-Type':'application/json'},
    body:JSON.stringify(registration),
  });
  if (!response.ok) {
    if (response.status===403) throw new Error('浏览器设备申请被拒绝（HTTP 403）。请检查历史扩展禁用记录、设备状态以及 Server 是否已升级为单设备审批版本。');
    throw new Error('浏览器设备申请失败：HTTP '+response.status);
  }
  const result = await response.json();
  if (result.id !== identity.deviceId) throw new Error('Server 返回的浏览器设备 ID 不一致');
  return {state:result.state};
}

let currentSocket = null;
let connectionState = 'DISCONNECTED';
const requests = new Map();
let keepalive = null;
let connectingPromise = null;
let pushHandler = null;
export function onServerPush(callback) { pushHandler = callback; }

export function currentConnectionState() { return connectionState; }
export function disconnect() {
  if (keepalive) clearInterval(keepalive);
  keepalive = null;
  for (const [id, value] of requests) { clearTimeout(value.timer); value.reject(new Error('WSS 已断开')); requests.delete(id); }
  if (currentSocket) currentSocket.close(1000, 'extension disconnected');
  currentSocket = null;
  connectionState = 'DISCONNECTED';
}

async function connectFreshBrowser() {
  const settings = await getSettings();
  if (!settings.serverOrigin) throw new Error('请先配置 HTTPS Server');
  const identity = await getOrCreateIdentity();
  disconnect();
  const url = new URL(settings.serverOrigin);
  url.protocol = 'wss:';
  url.pathname = '/api/v1/browser-sync/ws';
  // No long-term tokens, credentials or Cookie values in URL.
  const socket = new WebSocket(url);
  currentSocket = socket;
  connectionState = 'CONNECTING';
  return new Promise((resolve, reject) => {
    let authenticated = false;
    let finished = false;
    let diagnosing = null;
    let stage = 'upgrade';
    const failWithDiagnosis = () => {
      if (finished || diagnosing) return;
      if (stage !== 'upgrade') {
        fail(new Error(browserWSSFailureMessage(stage)));
        return;
      }
      diagnosing = diagnoseBrowserWSS(settings.serverOrigin);
      diagnosing.then(error => fail(new Error(browserWSSFailureMessage(stage) + ' ' + error.message)),
        () => fail(new Error(browserWSSFailureMessage(stage))));
    };
    const fail = error => {
      if (finished) return;
      finished = true;
      connectionState = 'DISCONNECTED';
      socket.close();
      reject(error);
    };
    socket.addEventListener('open', () => {
      stage = 'hello';
      socket.send(JSON.stringify({ type: 'AUTH_HELLO', deviceId: identity.deviceId }));
    });
    socket.addEventListener('message', async event => {
      let msg;
      try { msg = JSON.parse(event.data); } catch { return fail(new Error('Server 响应无效')); }
      try {
        if (msg.type === 'AUTH_CHALLENGE') {
          stage = 'challenge';
          if (msg.deviceId !== identity.deviceId) throw new Error('Server 设备身份不匹配');
          const proof = await signChallenge({
            serverOrigin: settings.serverOrigin,
            deviceId: identity.deviceId,
            challenge: msg.challenge,
          });
          socket.send(JSON.stringify({
            type: 'AUTH_PROOF', deviceId: identity.deviceId,
            challenge: msg.challenge, signature: proof.signature
          }));
          stage = 'proof';
        } else if (msg.type === 'AUTH_OK') {
          if (msg.deviceId !== identity.deviceId || msg.sessionTransferEnabled !== true) {
            throw new Error('Server 协议状态与预期不符');
          }
          stage = 'authenticated';
          authenticated = true;
          connectionState = 'AUTHENTICATED';
          if (!finished) { finished = true; resolve({ state: connectionState }); }
          // A live Chrome 116+ MV3 WebSocket needs periodic control traffic.
          // Reconnecting after worker suspension is handled by the caller.
          if (keepalive) clearInterval(keepalive);
          keepalive = setInterval(() => {
            if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'PING' }));
          }, 20000);
        } else if (msg.type === 'PONG') {
          return;
        } else if (['SESSION_SNAPSHOT','SYNC_REQUEST','SYNC_ACK'].includes(msg.type)) {
          if (!authenticated || !pushHandler) throw new Error('未授权的会话数据消息');
          // A push may independently issue control requests. The WebSocket
          // listener must not await it; responses are resolved above.
          Promise.resolve().then(() => pushHandler(msg)).catch(() => {
            // The handler maps safe failures to ACK; never log secrets.
          });
          return;
        } else if (msg.requestId && requests.has(msg.requestId)) {
          const item = requests.get(msg.requestId);
          requests.delete(msg.requestId);
          clearTimeout(item.timer);
          if (msg.type === 'RULE_ERROR') item.reject(new Error(msg.error || '规则未获授权'));
          else item.resolve(msg);
        } else {
          throw new Error('未支持的 Server 响应');
        }
      } catch (error) {
        if (authenticated) disconnect();
        else fail(error);
      }
    });
    socket.addEventListener('error', () => {
      if (!authenticated) failWithDiagnosis();
    });
    socket.addEventListener('close', event => {
      if (!authenticated && event.code === 1008) {
        // 1008 is delivered only AFTER a completed WebSocket Upgrade.
        // Do not mask a failed proof as a TLS/network error.
        fail(new Error(browserWSSFailureMessage(stage, event.code)));
      }
      if (socket === currentSocket) {
        if (keepalive) clearInterval(keepalive);
        keepalive = null;
        for (const [id, item] of requests) { clearTimeout(item.timer); item.reject(new Error('WSS 连接中断')); requests.delete(id); }
        currentSocket = null;
        connectionState = 'DISCONNECTED';
      }
      if (!authenticated) failWithDiagnosis();
    });
  });
}

// Coalesce user-triggered connection and MV3 alarm reconnect attempts.
// A second caller must never close a first in-progress AUTH_PROOF socket.
export function connectBrowser() {
  if (connectionState === 'AUTHENTICATED' && currentSocket?.readyState === WebSocket.OPEN)
    return Promise.resolve({ state: connectionState });
  if (connectingPromise) return connectingPromise;
  const promise = connectFreshBrowser();
  connectingPromise = promise;
  promise.finally(() => {
    if (connectingPromise === promise) connectingPromise = null;
  }).catch(() => {});
  return promise;
}

export async function sendControl(type, payload = {}) {
  if (currentConnectionState() !== 'AUTHENTICATED' ||
      !currentSocket || currentSocket.readyState !== WebSocket.OPEN) {
    await connectBrowser();
  }
  if (requests.size >= 8) throw new Error('配对请求过多，请稍后重试');
  const requestId = crypto.randomUUID();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      requests.delete(requestId);
      reject(new Error('Server 响应超时'));
    }, 15000);
    requests.set(requestId, { resolve, reject, timer });
    currentSocket.send(JSON.stringify({ type, requestId, ...payload }));
  });
}
