// Encrypted rule invitations: WebCrypto P-256 ephemeral ECDH + HKDF-SHA256 +
// AES-256-GCM, authenticated with the sender's pinned ECDSA P-256 signature.
// SESSION/Cookie encryption and transport are NOT enabled by this module.
import { getOrCreateIdentity, signBytes, encryptionPrivateKey } from './device-identity.js';
import { parseSiteOrigin } from './rules.js';

export function toBase64url(bytes) {
  let binary = '';
  for (const v of new Uint8Array(bytes)) binary += String.fromCharCode(v);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}
export function fromBase64url(value) {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]+$/.test(value)) throw new Error('无效的 Base64url');
  const binary = atob(value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4-value.length%4)%4));
  return Uint8Array.from(binary, c => c.charCodeAt(0));
}
const utf8 = value => new TextEncoder().encode(value);

export function validatePolicy(policy) {
  if (!policy || typeof policy !== 'object') throw new Error('站点策略无效');
  const siteOrigin = parseSiteOrigin(policy.siteOrigin);
  if (new URL(siteOrigin).pathname !== '/' || siteOrigin.length > 256) throw new Error('只支持 HTTPS Origin');
  const raw = policy.cookieNames;
  if (!Array.isArray(raw) || raw.length < 1 || raw.length > 20) throw new Error('需要选择 1–20 个 Cookie 名称');
  const cookieNames = [...new Set(raw.map(v => String(v).trim()))].sort();
  if (cookieNames.length !== raw.length || cookieNames.some(n => n.length > 128 || !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(n))) {
    throw new Error('Cookie 名称无效或重复');
  }
  return { siteOrigin, cookieNames };
}
export async function importPublic(spki, algorithm) {
  return crypto.subtle.importKey('spki', fromBase64url(spki), algorithm, true,
    algorithm.name === 'ECDH' ? [] : ['verify']);
}
async function encryptKey(peerSpki, ephemeral, salt, info) {
  const peerPublic = await importPublic(peerSpki, { name: 'ECDH', namedCurve: 'P-256' });
  const shared = await crypto.subtle.deriveBits({ name: 'ECDH', public: peerPublic }, ephemeral.privateKey, 256);
  const base = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
  return crypto.subtle.deriveKey({
    name: 'HKDF', hash: 'SHA-256', salt, info: utf8(info)
  }, base, { name: 'AES-GCM', length: 256 }, false, ['encrypt']);
}
async function decryptKey(ephemeralSpki, salt, info) {
  const publicKey = await importPublic(ephemeralSpki, { name: 'ECDH', namedCurve: 'P-256' });
  const shared = await crypto.subtle.deriveBits({ name: 'ECDH', public: publicKey }, await encryptionPrivateKey(), 256);
  const base = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
  return crypto.subtle.deriveKey({
    name: 'HKDF', hash: 'SHA-256', salt, info: utf8(info)
  }, base, { name: 'AES-GCM', length: 256 }, false, ['decrypt']);
}
export function offerCanonical(offer, sourceID) {
  return ['browser.sync.v1', 'RULE_OFFER', offer.ruleId, sourceID, offer.targetBrowserDeviceId,
    offer.policyDigest, offer.ephemeralKey, offer.salt, offer.iv, offer.ciphertext].join('\n');
}
export async function createEncryptedOffer({ target, siteOrigin, cookieNames }) {
  const identity = await getOrCreateIdentity();
  if (!target?.receive || !target.encryptionPublicKey || !target.signingPublicKey ||
    !target.id || target.id === identity.deviceId) throw new Error('无权选用该接收设备');
  const policy = validatePolicy({ siteOrigin, cookieNames });
  const ruleId = crypto.randomUUID();
  const plaintext = utf8(JSON.stringify(policy));
  const ephemeral = await crypto.subtle.generateKey({ name: 'ECDH', namedCurve: 'P-256' }, false, ['deriveBits']);
  const salt = crypto.getRandomValues(new Uint8Array(32));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const info = ['browser.sync.v1', 'RULE_OFFER', ruleId, identity.deviceId, target.id].join('\n');
  const key = await encryptKey(target.encryptionPublicKey, ephemeral, salt, info);
  const encrypted = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: utf8(info) }, key, plaintext);
  // Hash opaque ciphertext, NOT guessable website/Cookie-name policy text.
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', encrypted));
  const offer = {
    ruleId, targetBrowserDeviceId: target.id, policyDigest: Array.from(digest, b => b.toString(16).padStart(2,'0')).join(''),
    ephemeralKey: toBase64url(await crypto.subtle.exportKey('spki', ephemeral.publicKey)),
    salt: toBase64url(salt), iv: toBase64url(iv), ciphertext: toBase64url(encrypted)
  };
  offer.signature = toBase64url(await signBytes(utf8(offerCanonical(offer, identity.deviceId))));
  return { offer, policy };
}
export async function decryptEncryptedOffer(rule, source) {
  const identity = await getOrCreateIdentity();
  const offer = rule?.offer;
  if (!offer || rule.sourceBrowserDeviceId !== source?.id || rule.targetBrowserDeviceId !== identity.deviceId ||
    offer.targetBrowserDeviceId !== identity.deviceId || rule.ruleId !== offer.ruleId) {
    throw new Error('配对消息设备或规则不匹配');
  }
  const sourceKey = await importPublic(source.signingPublicKey, { name: 'ECDSA', namedCurve: 'P-256' });
  const verified = await crypto.subtle.verify({
    name: 'ECDSA', hash: 'SHA-256'
  }, sourceKey, fromBase64url(offer.signature), utf8(offerCanonical(offer, source.id)));
  if (!verified) throw new Error('配对签名校验失败');
  const info = ['browser.sync.v1', 'RULE_OFFER', offer.ruleId, source.id, identity.deviceId].join('\n');
  const key = await decryptKey(offer.ephemeralKey, fromBase64url(offer.salt), info);
  const plaintext = await crypto.subtle.decrypt({
    name: 'AES-GCM', iv: fromBase64url(offer.iv), additionalData: utf8(info)
  }, key, fromBase64url(offer.ciphertext));
  const bytes = new Uint8Array(plaintext);
  const hash = new Uint8Array(await crypto.subtle.digest('SHA-256', fromBase64url(offer.ciphertext)));
  const digest = Array.from(hash, b => b.toString(16).padStart(2,'0')).join('');
  if (digest !== offer.policyDigest) throw new Error('站点策略摘要错误');
  return validatePolicy(JSON.parse(new TextDecoder().decode(bytes)));
}
export async function pairingCode(ruleID, source, target) {
  // Compare this displayed code between the *two devices through a separate
  // trusted channel*, never solely using Server messages.
  const text = ['browser.sync.v1', ruleID, source.id, source.signingPublicKey,
    source.encryptionPublicKey, target.id, target.signingPublicKey, target.encryptionPublicKey].join('\n');
  const hash = new Uint8Array(await crypto.subtle.digest('SHA-256', utf8(text)));
  return Array.from(hash.slice(0, 6), b => b.toString(16).padStart(2, '0')).join('').toUpperCase().match(/.{4}/g).join('-');
}
