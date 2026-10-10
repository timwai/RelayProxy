// Website session snapshots use P-256 ephemeral ECDH + HKDF + AES-GCM,
// signed end-to-end by the approved source Browser Device. Server sees only
// fixed routing metadata and ciphertext, never Cookie names or values.
import { getOrCreateIdentity, signBytes, encryptionPrivateKey } from './device-identity.js';
import { toBase64url, fromBase64url, importPublic } from './envelope.js';

export const CIPHER_SUITE = 'P256-HKDF-SHA256-A256GCM-v1';
const utf8 = text => new TextEncoder().encode(text);

export function signedHeader(env) {
  const created = Date.parse(env.createdAt);
  const expires = Date.parse(env.expiresAt);
  if (!Number.isFinite(created) || !Number.isFinite(expires)) throw new Error('invalid session timestamp');
  return [env.protocol, env.type, env.messageId, env.ruleId,
    env.sourceBrowserDeviceId, env.targetBrowserDeviceId,
    String(env.sequence), String(created), String(expires),
    env.encryption.suite, env.encryption.keyId, env.encryption.enc,
    env.encryption.salt, env.encryption.iv].join('\n');
}
async function sessionAESKey(peer, ephemeral, salt, info, usage) {
  const remote = await importPublic(peer, {name:'ECDH',namedCurve:'P-256'});
  const bits = await crypto.subtle.deriveBits({name:'ECDH',public:remote}, ephemeral, 256);
  const base = await crypto.subtle.importKey('raw', bits, 'HKDF', false, ['deriveKey']);
  return crypto.subtle.deriveKey({name:'HKDF',hash:'SHA-256',salt,info:utf8(info)},
    base,{name:'AES-GCM',length:256},false,[usage]);
}
export async function encryptSnapshot(rule, target, sequence, payload) {
  const identity = await getOrCreateIdentity();
  if (rule?.status !== 'active' || rule.sourceBrowserDeviceId !== identity.deviceId ||
      rule.targetBrowserDeviceId !== target?.id || !target.encryptionPublicKey ||
      !Number.isSafeInteger(sequence) || sequence < 1) throw new Error('未授权的会话快照');
  const ephemeral = await crypto.subtle.generateKey(
    {name:'ECDH',namedCurve:'P-256'}, false,['deriveBits']);
  const salt = crypto.getRandomValues(new Uint8Array(32));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const now = Date.now();
  const env = {
    protocol:'browser.sync.v1',type:'SESSION_SNAPSHOT',messageId:crypto.randomUUID(),
    ruleId:rule.ruleId,sourceBrowserDeviceId:identity.deviceId,
    targetBrowserDeviceId:target.id,sequence,
    createdAt:new Date(now).toISOString(),expiresAt:new Date(now+5*60*1000).toISOString(),
    encryption: {
      suite:CIPHER_SUITE,keyId:target.id,
      enc:toBase64url(await crypto.subtle.exportKey('spki',ephemeral.publicKey)),
      salt:toBase64url(salt), iv:toBase64url(iv)
    }
  };
  const header = signedHeader(env);
  const key = await sessionAESKey(target.encryptionPublicKey,ephemeral.privateKey,salt,header,'encrypt');
  const raw = utf8(JSON.stringify(payload));
  if (raw.length > 64*1024) throw new Error('Cookie 快照过大');
  const ciphertext = await crypto.subtle.encrypt({name:'AES-GCM',iv,additionalData:utf8(header)},key,raw);
  env.ciphertext = toBase64url(ciphertext);
  env.signature = toBase64url(await signBytes(utf8(header+'\n'+env.ciphertext)));
  return env;
}
export async function decryptSnapshot(env, source) {
  const identity = await getOrCreateIdentity();
  if (!env || env.protocol!=='browser.sync.v1'||env.type!=='SESSION_SNAPSHOT'||
    env.encryption?.suite!==CIPHER_SUITE||env.encryption?.keyId!==identity.deviceId||
    env.targetBrowserDeviceId!==identity.deviceId||
    env.sourceBrowserDeviceId!==source.id||!env.signature||
    !Number.isSafeInteger(env.sequence)||env.sequence<1) throw new Error('快照来源或加密算法不匹配');
  const now=Date.now(),created=Date.parse(env.createdAt),expires=Date.parse(env.expiresAt);
  if (!Number.isFinite(created)||!Number.isFinite(expires)||expires<=now||
    created>now+60000||expires-created>15*60000) throw new Error('会话快照已过期');
  const header=signedHeader(env);
  const verifyKey=await importPublic(source.signingPublicKey,{name:'ECDSA',namedCurve:'P-256'});
  const valid=await crypto.subtle.verify({name:'ECDSA',hash:'SHA-256'},verifyKey,
    fromBase64url(env.signature),utf8(header+'\n'+env.ciphertext));
  if(!valid)throw new Error('来源设备签名无效');
  const remote=await importPublic(env.encryption.enc,{name:'ECDH',namedCurve:'P-256'});
  const bits=await crypto.subtle.deriveBits({name:'ECDH',public:remote},await encryptionPrivateKey(),256);
  const base=await crypto.subtle.importKey('raw',bits,'HKDF',false,['deriveKey']);
  const key=await crypto.subtle.deriveKey({
    name:'HKDF',hash:'SHA-256',salt:fromBase64url(env.encryption.salt),info:utf8(header)
  },base,{name:'AES-GCM',length:256},false,['decrypt']);
  const iv=fromBase64url(env.encryption.iv);
  if(iv.length!==12||fromBase64url(env.encryption.salt).length!==32)throw new Error('加密参数无效');
  const clear=await crypto.subtle.decrypt({name:'AES-GCM',iv,additionalData:utf8(header)},
    key,fromBase64url(env.ciphertext));
  if(clear.byteLength>64*1024)throw new Error('会话快照超限');
  return JSON.parse(new TextDecoder().decode(clear));
}
