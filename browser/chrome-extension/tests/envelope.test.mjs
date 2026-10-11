import test from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { validatePolicy, fromBase64url, toBase64url, pairingCode, offerCanonical } from '../src/envelope.js';
import { parseServerOrigin, parseSiteOrigin } from '../src/rules.js';

globalThis.crypto ||= webcrypto;

test('legacy cookie-name rule remains HTTPS-only', () => {
  assert.deepEqual(validatePolicy({
    siteOrigin: 'https://example.com/',
    cookieNames: ['session', 'csrf']
  }), { siteOrigin: 'https://example.com', cookieNames: ['csrf','session'] });
  for (const origin of ['http://example.com', 'https://example.com/login',
    'https://user:pass@example.com', 'https://example.com?a=1']) {
    assert.throws(() => validatePolicy({ siteOrigin: origin, cookieNames: ['session'] }));
  }
  assert.throws(() => validatePolicy({siteOrigin:'https://example.com',cookieNames:['session','session']}));
  assert.throws(() => validatePolicy({siteOrigin:'https://example.com',cookieNames:[]}));
  assert.throws(() => validatePolicy({siteOrigin:'https://example.com',cookieNames:['space name']}));
});

test('v2 policy captures all Cookie and Storage keys only for approved origin',()=>{
  const policy={siteOrigin:'https://example.com',schemaVersion:2,
    allCookies:true,localStorage:true,sessionStorage:true};
  assert.deepEqual(validatePolicy(policy),policy);
  assert.throws(()=>validatePolicy({...policy,localStorage:false}),/版本|范围/);
  assert.deepEqual(validatePolicy({...policy,siteOrigin:'http://example.com'}),
    {...policy,siteOrigin:'http://example.com'});
  assert.deepEqual(validatePolicy({...policy,siteOrigin:'http://localhost:8080/'}),
    {...policy,siteOrigin:'http://localhost:8080'});
});

test('server origin cannot contain path or credentials', () => {
  assert.equal(parseServerOrigin('https://relay.example.com:8443'), 'https://relay.example.com:8443');
  assert.throws(() => parseServerOrigin('https://relay.example.com/admin'));
  assert.throws(() => parseServerOrigin('http://relay.example.com'));
  assert.equal(parseSiteOrigin('https://www.example.org/'), 'https://www.example.org');
  assert.equal(parseSiteOrigin('http://example.org:8080/'), 'http://example.org:8080');
  assert.throws(() => parseSiteOrigin('ftp://example.org/'));
  assert.throws(() => parseSiteOrigin('http://user:password@example.org/'));
  assert.throws(() => parseSiteOrigin('http://example.org/private'));
  assert.throws(() => parseSiteOrigin('http://example.org/?credential=1'));
});

test('base64url requires valid characters and roundtrips binary', () => {
  const bytes = Uint8Array.from([0, 1, 128, 255]);
  assert.deepEqual(fromBase64url(toBase64url(bytes)), bytes);
  assert.throws(() => fromBase64url('bad+symbol'));
});

test('pairing code binds both public keys and rule ID', async () => {
  const a = {id:'a', signingPublicKey:'sign_a', encryptionPublicKey:'enc_a'};
  const b = {id:'b', signingPublicKey:'sign_b', encryptionPublicKey:'enc_b'};
  const first = await pairingCode('rule_1',a,b);
  assert.equal(first, await pairingCode('rule_1',a,b));
  assert.match(first, /^[0-9A-F]{4}(-[0-9A-F]{4}){2}$/);
  assert.notEqual(first, await pairingCode('rule_2',a,b));
  assert.notEqual(first, await pairingCode('rule_1',a,{...b,encryptionPublicKey:'substituted'}));
});

test('offer signature canonicalization binds all forwarding fields', () => {
  const offer = {
    ruleId:'r1', targetBrowserDeviceId:'target', policyDigest:'digest',
    ephemeralKey:'pub', salt:'salt', iv:'iv', ciphertext:'encrypted'
  };
  const message = offerCanonical(offer,'source');
  assert.notEqual(message, offerCanonical({...offer,targetBrowserDeviceId:'attacker'},'source'));
  assert.notEqual(message, offerCanonical(offer,'attacker'));
});

test('encrypted rule proposal decrypts only for the intended browser and rejects tampering', async () => {
  const { getOrCreateIdentity } = await import('../src/device-identity.js');
  const { createEncryptedOffer, decryptEncryptedOffer } = await import('../src/envelope.js');

  // Minimal fake extension Profile storage for exercising real WebCrypto
  // without a running Chrome instance or third-party test dependencies.
  function makeProfile() {
    const keys = new Map();
    const data = new Map();
    const database = {
      createObjectStore() {},
      transaction() {
        const tx = {
          objectStore() {
            return {
              get(name) {
                const request = {};
                queueMicrotask(() => {
                  request.result = keys.get(name);
                  request.onsuccess?.();
                });
                return request;
              },
              put(value, name) {
                keys.set(name, value);
                queueMicrotask(() => tx.oncomplete?.());
              }
            };
          }
        };
        return tx;
      },
      close() {}
    };
    return {
      idb: {
        open() {
          const request = {result:database};
          queueMicrotask(() => {
            request.onupgradeneeded?.();
            request.onsuccess?.();
          });
          return request;
        }
      },
      chrome: {
        storage: {
          local: {
            async get(name) {
              if (typeof name !== 'string') throw new Error('unexpected storage key');
              return {[name]:data.get(name)};
            },
            async set(items) {
              for (const [key,value] of Object.entries(items)) data.set(key,value);
            }
          }
        }
      }
    };
  }

  const a = makeProfile(), b = makeProfile();
  function use(profile) {
    globalThis.indexedDB = profile.idb;
    globalThis.chrome = profile.chrome;
  }
  use(a);
  const source = await getOrCreateIdentity();
  use(b);
  const target = await getOrCreateIdentity();
  use(a);
  const {offer,policy} = await createEncryptedOffer({
    target: {id: target.deviceId, signingPublicKey: target.signingPublicKey,
      encryptionPublicKey: target.encryptionPublicKey, receive:true},
    siteOrigin:'https://example.com',
  });
  assert.deepEqual(policy, {siteOrigin:'https://example.com',schemaVersion:2,
    allCookies:true,localStorage:true,sessionStorage:true});
  use(b);
  const rule = {ruleId:offer.ruleId,sourceBrowserDeviceId:source.deviceId,
    targetBrowserDeviceId:target.deviceId, offer};
  const sourcePeer = {id:source.deviceId,signingPublicKey:source.signingPublicKey};
  assert.deepEqual(await decryptEncryptedOffer(rule, sourcePeer), policy);
  assert.rejects(decryptEncryptedOffer({
    ...rule, offer:{...offer,policyDigest:'f'.repeat(64)}
  },sourcePeer), /签名/);
  assert.rejects(decryptEncryptedOffer({
    ...rule, offer:{...offer,ciphertext:(offer.ciphertext[0] === 'A' ? 'B' : 'A') + offer.ciphertext.slice(1)}
  },sourcePeer));
  use(a);
  const http = await createEncryptedOffer({
    target: {id:target.deviceId,signingPublicKey:target.signingPublicKey,
      encryptionPublicKey:target.encryptionPublicKey,receive:true},
    siteOrigin:'http://example.com:8080'
  });
  use(b);
  assert.deepEqual(await decryptEncryptedOffer({
    ruleId:http.offer.ruleId,sourceBrowserDeviceId:source.deviceId,
    targetBrowserDeviceId:target.deviceId,offer:http.offer
  },sourcePeer),{siteOrigin:'http://example.com:8080',schemaVersion:2,
    allCookies:true,localStorage:true,sessionStorage:true});
});
