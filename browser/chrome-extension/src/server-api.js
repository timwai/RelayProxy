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

export function currentConnectionState() { return connectionState; }
export function disconnect() {
  if (currentSocket) currentSocket.close(1000, 'extension disconnected');
  currentSocket = null;
  connectionState = 'DISCONNECTED';
}

export async function connectBrowser() {
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
          if (msg.deviceId !== identity.deviceId || msg.sessionTransferEnabled !== false) {
            throw new Error('Server 协议状态与预期不符');
          }
          authenticated = true;
          connectionState = 'AUTHENTICATED';
          if (!finished) { finished = true; resolve({ state: connectionState }); }
        } else if (msg.type !== 'PONG') {
          throw new Error('未支持的 Server 响应');
        }
      } catch (error) { fail(error); }
    });
    socket.addEventListener('error', () => {
      if (!authenticated) fail(new Error('无法建立安全 WSS 连接'));
    });
    socket.addEventListener('close', () => {
      if (socket === currentSocket) {
        currentSocket = null;
        connectionState = 'DISCONNECTED';
      }
      if (!authenticated) fail(new Error('Server 拒绝连接或未完成设备审批'));
    });
  });
}
