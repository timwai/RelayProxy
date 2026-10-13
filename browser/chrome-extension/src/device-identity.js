// Browser device identity is scoped to this Chrome extension Profile.
// Non-extractable CryptoKey objects persist in IndexedDB; never store
// private keys, cookies, or access tokens in chrome.storage.sync.
const DB_NAME = 'relayproxy-browser-sync';
const STORE = 'keys';
const DEVICE_ID_STORAGE = 'browserDeviceId';
// The authoritative device ID and BOTH non-extractable private keys live in
// one IndexedDB record committed by a single readwrite transaction.
const IDENTITY_RECORD = 'device-identity-v2';

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

// Keep the legacy readKey / saveKeys helpers for the unrelated Cookie HMAC
// fingerprint key and for migration of existing pre-v2 Chrome Profiles.
function validKeyPair(pair) {
  return !!pair?.publicKey && !!pair?.privateKey;
}
function validIdentity(record) {
  return typeof record?.deviceId === 'string' &&
    /^browser_[0-9a-f-]{36}$/.test(record.deviceId) &&
    validKeyPair(record.signing) && validKeyPair(record.encryption);
}

async function generateIdentity() {
  const signing = await crypto.subtle.generateKey(
    {name:'ECDSA',namedCurve:'P-256'},false,['sign','verify']);
  const encryption = await crypto.subtle.generateKey(
    {name:'ECDH',namedCurve:'P-256'},false,['deriveKey','deriveBits']);
  return {deviceId:'browser_'+crypto.randomUUID(),signing,encryption};
}

// Multiple MV3 execution worlds (options page + Service Worker) can create an
// identity concurrently. Do NOT use a JS mutex as the cross-world lock: only
// the IndexedDB readwrite transaction is shared by all of these contexts.
//
// The bundle takes precedence over the legacy separate keys. Migrating legacy
// metadata requires seeing BOTH keys inside THIS transaction and a stored ID;
// an interrupted earlier rotation must never rebind fresh keys to an old ID.
async function commitIdentity(candidate,legacyDeviceId) {
  const db=await openDatabase();
  try {
    return await new Promise((resolve,reject)=>{
      const tx=db.transaction(STORE,'readwrite');
      const store=tx.objectStore(STORE);
      let selected;
      const read=store.get(IDENTITY_RECORD);
      read.onsuccess=()=>{
        if(validIdentity(read.result)){
          selected=read.result;
          return;
        }
        const commit=(value)=>{
          selected=value;
          store.put(value,IDENTITY_RECORD);
          store.delete('signing');
          store.delete('encryption');
        };
        // Invalid existing v2 bundle represents key corruption: rotate rather
        // than falling back to stale legacy keys and their approved identity.
        if(read.result!==undefined){
          commit(candidate);
          return;
        }
        const signingReq=store.get('signing');
        const encryptionReq=store.get('encryption');
        encryptionReq.onsuccess=()=>{
          const signing=signingReq.result;
          const encryption=encryptionReq.result;
          if(typeof legacyDeviceId==='string' &&
             /^browser_[0-9a-f-]{36}$/.test(legacyDeviceId) &&
             validKeyPair(signing) && validKeyPair(encryption))
            commit({deviceId:legacyDeviceId,signing,encryption});
          else commit(candidate);
        };
        signingReq.onerror=()=>reject(signingReq.error);
        encryptionReq.onerror=()=>reject(encryptionReq.error);
      };
      read.onerror=()=>reject(read.error);
      tx.oncomplete=()=>{
        if(!validIdentity(selected))reject(new Error('Identity transaction incomplete'));
        else resolve(selected);
      };
      tx.onerror=()=>reject(tx.error||new Error('Identity transaction failed'));
      tx.onabort=()=>reject(tx.error||new Error('Identity transaction aborted'));
    });
  } finally {
    db.close();
  }
}

let identityFlight=null;
function identityRecord(){
  if(identityFlight)return identityFlight;
  const task=(async()=>{
    const stored=await readKey(IDENTITY_RECORD);
    let identity=stored;
    if(!validIdentity(stored)){
      const legacy=(await chrome.storage.local.get(DEVICE_ID_STORAGE))[DEVICE_ID_STORAGE];
      const candidate=await generateIdentity();
      identity=await commitIdentity(candidate,legacy);
    }
    // chrome.storage.local is only a mirror for UI/tests. It is NOT used to
    // authorize a key or select the ID after successful v2 initialization.
    const mirror=(await chrome.storage.local.get(DEVICE_ID_STORAGE))[DEVICE_ID_STORAGE];
    if(mirror!==identity.deviceId)
      await chrome.storage.local.set({[DEVICE_ID_STORAGE]:identity.deviceId});
    return identity;
  })();
  identityFlight=task;
  task.finally(()=>{if(identityFlight===task)identityFlight=null;}).catch(()=>{});
  return task;
}

function base64url(bytes) {
  let binary = '';
  for (const value of new Uint8Array(bytes)) binary += String.fromCharCode(value);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

export async function getOrCreateIdentity() {
  const identity=await identityRecord();
  const signingPublicKey=base64url(await crypto.subtle.exportKey('spki',identity.signing.publicKey));
  const encryptionPublicKey=base64url(await crypto.subtle.exportKey('spki',identity.encryption.publicKey));
  return {deviceId:identity.deviceId,signingPublicKey,encryptionPublicKey};
}

export async function signChallenge({ serverOrigin, challenge, deviceId }) {
  if (typeof challenge !== 'string' || !challenge || challenge.length > 512)
    throw new Error('Invalid server challenge');
  const identity = await identityRecord();
  if (identity.deviceId !== deviceId) throw new Error('Browser identity mismatch');
  const canonical = ['browser.sync.v1', serverOrigin, deviceId, challenge].join('\n');
  const signature = await crypto.subtle.sign(
    { name: 'ECDSA', hash: 'SHA-256' }, identity.signing.privateKey,
    new TextEncoder().encode(canonical)
  );
  return { deviceId, signature: base64url(signature), protocol: 'browser.sync.v1' };
}

export async function signBytes(bytes) {
  const identity=await identityRecord();
  return crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, identity.signing.privateKey, bytes);
}

export async function encryptionPrivateKey() {
  const identity=await identityRecord();
  return identity.encryption.privateKey;
}

// An unextractable, Profile-local HMAC key prevents browser storage from
// holding unsalted SHA-256 hashes of possibly low-entropy session values.
// Tags are scoped to a rule + Cookie name, so equal values cannot be
// correlated between different website rules.
export async function cookieValueTag(ruleId, name, value) {
  if (typeof ruleId !== 'string' || typeof name !== 'string' || typeof value !== 'string')
    throw new Error('Invalid Cookie fingerprint input');
  let key = await readKey('cookie-hmac-v1');
  if (!key) {
    key = await crypto.subtle.generateKey(
      {name:'HMAC', hash:'SHA-256', length:256}, false, ['sign']
    );
    await saveKeys([['cookie-hmac-v1', key]]);
  }
  const message = new TextEncoder().encode(['browser.sync.cookie.v1', ruleId, name, value].join('\n'));
  return base64url(await crypto.subtle.sign('HMAC', key, message));
}
