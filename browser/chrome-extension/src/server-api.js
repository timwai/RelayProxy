// Public browser-device transport; no local Agent or native host.
// No cookies/credentials are sent at this stage.
import { getOrCreateIdentity, signChallenge } from './device-identity.js';
import { getSettings } from './rules.js';

const endpoint = (base, path) => new URL(path, base).toString();

export async function registerBrowser() {
  const settings = await getSettings();
  if (!settings.serverOrigin) throw new Error('请先配置 HTTPS Server');
  const id = await getOrCreateIdentity();
  const response = await fetch(endpoint(settings.serverOrigin,
    '/api/v1/browser-sync/devices/register'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify({
      id: id.deviceId,
      name: 'Chrome ' + navigator.platform,
      signingPublicKey: id.signingPublicKey,
      encryptionPublicKey: id.encryptionPublicKey,
    }),
  });
  if (!response.ok) throw new Error('浏览器设备注册失败：HTTP ' + response.status);
  return response.json();
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
    const fail = error => {
      if (finished) return;
      finished = true;
      connectionState = 'DISCONNECTED';
      socket.close();
      reject(error);
    };
    socket.addEventListener('open', () => {
      socket.send(JSON.stringify({ type: 'AUTH_HELLO', deviceId: identity.deviceId }));
    });
    socket.addEventListener('message', async event => {
      let msg;
      try { msg = JSON.parse(event.data); } catch { return fail(new Error('Server 响应无效')); }
      try {
        if (msg.type === 'AUTH_CHALLENGE') {
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
        } else if (msg.type === 'AUTH_OK') {
          if (msg.deviceId !== identity.deviceId || msg.sessionTransferEnabled !== true) {
            throw new Error('Server 协议状态与预期不符');
          }
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
      if (!authenticated) fail(new Error('无法建立安全 WSS 连接'));
    });
    socket.addEventListener('close', () => {
      if (socket === currentSocket) {
        if (keepalive) clearInterval(keepalive);
        keepalive = null;
        for (const [id, item] of requests) { clearTimeout(item.timer); item.reject(new Error('WSS 连接中断')); requests.delete(id); }
        currentSocket = null;
        connectionState = 'DISCONNECTED';
      }
      if (!authenticated) fail(new Error('Server 拒绝连接或未完成设备审批'));
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
