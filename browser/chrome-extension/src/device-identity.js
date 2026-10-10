// Browser device identity is scoped to this Chrome extension Profile.
// Non-extractable CryptoKey objects persist in IndexedDB; never store
// private keys, cookies, or access tokens in chrome.storage.sync.
const DB_NAME = 'relayproxy-browser-sync';
const STORE = 'keys';
const DEVICE_ID_STORAGE = 'browserDeviceId';

function openDatabase() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

async function readKey(name) {
  const db = await openDatabase();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, 'readonly');
      const request = tx.objectStore(STORE).get(name);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  } finally {
    db.close();
  }
}

async function saveKeys(entries) {
  const db = await openDatabase();
  try {
    await new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, 'readwrite');
      for (const [name, value] of entries) tx.objectStore(STORE).put(value, name);
      tx.oncomplete = resolve;
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
  } finally {
    db.close();
  }
}

function base64url(bytes) {
  let binary = '';
  for (const value of new Uint8Array(bytes)) binary += String.fromCharCode(value);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

export async function getOrCreateIdentity() {
  let { [DEVICE_ID_STORAGE]: deviceId } = await chrome.storage.local.get(DEVICE_ID_STORAGE);
  let signing = await readKey('signing');
  let encryption = await readKey('encryption');

  if (!deviceId || !signing?.privateKey || !encryption?.privateKey) {
    // Do not silently reuse the previous device ID when private keys are lost:
    // reapproval and a fresh pairing are required.
    deviceId = 'browser_' + crypto.randomUUID();
    signing = await crypto.subtle.generateKey(
      { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify']
    );
    encryption = await crypto.subtle.generateKey(
      { name: 'ECDH', namedCurve: 'P-256' }, false, ['deriveKey', 'deriveBits']
    );
    await saveKeys([['signing', signing], ['encryption', encryption]]);
    await chrome.storage.local.set({ [DEVICE_ID_STORAGE]: deviceId });
  }

  const signingPublicKey = base64url(await crypto.subtle.exportKey('spki', signing.publicKey));
  const encryptionPublicKey = base64url(await crypto.subtle.exportKey('spki', encryption.publicKey));
  return { deviceId, signingPublicKey, encryptionPublicKey };
}

export async function signChallenge({ serverOrigin, challenge, deviceId }) {
  if (typeof challenge !== 'string' || !challenge || challenge.length > 512)
    throw new Error('Invalid server challenge');
  const identity = await getOrCreateIdentity();
  if (identity.deviceId !== deviceId) throw new Error('Browser identity mismatch');
  const signing = await readKey('signing');
  const canonical = ['browser.sync.v1', serverOrigin, deviceId, challenge].join('\n');
  const signature = await crypto.subtle.sign(
    { name: 'ECDSA', hash: 'SHA-256' }, signing.privateKey,
    new TextEncoder().encode(canonical)
  );
  return { deviceId, signature: base64url(signature), protocol: 'browser.sync.v1' };
}
