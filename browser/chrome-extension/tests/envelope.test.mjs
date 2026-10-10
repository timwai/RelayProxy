import test from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { validatePolicy, fromBase64url, toBase64url, pairingCode, offerCanonical } from '../src/envelope.js';
import { parseServerOrigin, parseSiteOrigin } from '../src/rules.js';

globalThis.crypto ||= webcrypto;

test('exact HTTPS site origin and cookie names only', () => {
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

test('server origin cannot contain path or credentials', () => {
  assert.equal(parseServerOrigin('https://relay.example.com:8443'), 'https://relay.example.com:8443');
  assert.throws(() => parseServerOrigin('https://relay.example.com/admin'));
  assert.throws(() => parseServerOrigin('http://relay.example.com'));
  assert.equal(parseSiteOrigin('https://www.example.org/'), 'https://www.example.org');
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
