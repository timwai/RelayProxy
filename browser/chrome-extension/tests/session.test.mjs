import test from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { getOrCreateIdentity } from '../src/device-identity.js';
import { encryptSnapshot, decryptSnapshot, signedHeader } from '../src/session-envelope.js';
import { captureCookies, applyCookies } from '../src/cookies.js';

globalThis.crypto ||= webcrypto;

// A fake Chrome Profile with persistent, non-exportable CryptoKey storage,
// mutable Cookie store, and no actual OS Chrome requirement.
function createProfile() {
  const privateKeys = new Map();
  const local = new Map();
  const jar = new Map();
  const database = {
    createObjectStore() {},
    transaction() {
      const tx = {objectStore() {
        return {
          get(name) {
            const result = {};
            queueMicrotask(() => {result.result=privateKeys.get(name);result.onsuccess?.();});
            return result;
          },
          put(value,name) {
            privateKeys.set(name,value);
            queueMicrotask(() => tx.oncomplete?.());
          }
        };
      }};
      return tx;
    },
    close() {}
  };
  return {
    jar,
    indexedDB: {open() {
      const req = {result: database};
      queueMicrotask(() => {req.onupgradeneeded?.();req.onsuccess?.();});
      return req;
    }},
    chrome: {
      storage: {local: {
        async get(key) {return {[key]:local.get(key)};},
        async set(obj) {for(const [k,v] of Object.entries(obj))local.set(k,v);}
      }},
      cookies: {
        async getAll({url}) {
          const host=new URL(url).hostname;
          return [...jar.values()].filter(c=>
            host===c.domain.replace(/^\./,'')||
            (!c.hostOnly&&host.endsWith('.'+c.domain.replace(/^\./,''))));
        },
        async remove({url,name}) {
          if (new URL(url).hostname!=='example.com'||!jar.has(name)) return null;
          jar.delete(name);
          return {url,name};
        },
        async set(details) {
          const c = {
            name:details.name,value:details.value,secure:details.secure,
            httpOnly:details.httpOnly,sameSite:details.sameSite||'unspecified',
            hostOnly:true,path:'/',domain:new URL(details.url).hostname,
            ...(details.expirationDate?{expirationDate:details.expirationDate}:{}),
          };
          jar.set(c.name,c);return c;
        }
      }
    }
  };
}
function use(profile) {
  globalThis.chrome = profile.chrome;
  globalThis.indexedDB = profile.indexedDB;
}
const policy={siteOrigin:'https://example.com',cookieNames:['session']};
const sessionValue='super-high-entropy-session-test-value';

test('two Chrome profiles encrypt/decrypt a signed session envelope and block tampering', async()=>{
  const source = createProfile(), target = createProfile();
  use(source);
  const a = await getOrCreateIdentity();
  use(target);
  const b = await getOrCreateIdentity();
  const rule = {ruleId:crypto.randomUUID(),status:'active',
    sourceBrowserDeviceId:a.deviceId,targetBrowserDeviceId:b.deviceId};
  const cookie = {name:'session',value:sessionValue,secure:true,httpOnly:true,
    sameSite:'lax',hostOnly:true,domain:'example.com',path:'/'};
  source.jar.set('session',cookie);
  use(source);
  const payload=await captureCookies(policy);
  const encrypted=await encryptSnapshot(rule,{...b,id:b.deviceId},1,payload);
  assert.equal(encrypted.type,'SESSION_SNAPSHOT');
  assert.equal(encrypted.encryption.suite,'P256-HKDF-SHA256-A256GCM-v1');
  assert.equal(encrypted.ciphertext.includes(sessionValue),false);
  assert.equal(signedHeader(encrypted).includes('example.com'),false);
  use(target);
  const clear=await decryptSnapshot(encrypted,{...a,id:a.deviceId});
  assert.deepEqual(clear,payload);
  const result=await applyCookies(rule.ruleId,clear,policy);
  assert.equal(result.status,'APPLIED');
  assert.equal(target.jar.get('session').value,sessionValue);
  const corrupted={...encrypted,ciphertext:(encrypted.ciphertext[0]==='A'?'B':'A')+encrypted.ciphertext.slice(1)};
  await assert.rejects(decryptSnapshot(corrupted,{...a,id:a.deviceId}),/签名/);
});

test('host-only root and existing-login conflicts are enforced without token storage',async()=>{
  const p=createProfile();use(p);
  const conflicting={name:'session',value:'existing-browser-account',secure:true,
    httpOnly:true,sameSite:'lax',hostOnly:true,domain:'example.com',path:'/'};
  p.jar.set('session',conflicting);
  const snapshot={siteOrigin:policy.siteOrigin,cookieNames:policy.cookieNames,
    cookies:[{name:'session',value:'remote-different-account',secure:true,httpOnly:true,sameSite:'lax'}],removedNames:[]};
  await assert.rejects(applyCookies('rule',snapshot,policy),/CONFLICT/);
  assert.equal(p.jar.get('session').value,'existing-browser-account');
  await applyCookies('rule',snapshot,policy,{allowOverwrite:true});
  assert.equal(p.jar.get('session').value,'remote-different-account');
  p.jar.get('session').value='modified-on-target';
  await assert.rejects(applyCookies('rule',{...snapshot,cookies:[{...snapshot.cookies[0],value:'next-from-source'}]},policy),/CONFLICT/);
  p.jar.set('session',{...conflicting,hostOnly:false,domain:'.example.com'});
  await assert.rejects(captureCookies(policy),/作用域/);
});

test('logout removes only Cookie values last installed by the same rule', async () => {
  const source=createProfile(),target=createProfile();
  use(source);
  source.jar.set('session',{name:'session',value:'account-on-A',secure:true,
    httpOnly:true,sameSite:'lax',hostOnly:true,domain:'example.com',path:'/'});
  const loggedIn=await captureCookies(policy);
  use(target);
  await applyCookies('logout-rule',loggedIn,policy);
  assert.equal(target.jar.get('session').value,'account-on-A');

  use(source);
  source.jar.delete('session');
  const loggedOut=await captureCookies(policy);
  assert.deepEqual(loggedOut.removedNames,['session']);
  assert.deepEqual(loggedOut.cookies,[]);
  use(target);
  const removed=await applyCookies('logout-rule',loggedOut,policy);
  assert.equal(removed.removed,1);
  assert.equal(target.jar.has('session'),false);

  target.jar.set('session',{name:'session',value:'other-local-account',secure:true,
    httpOnly:true,sameSite:'lax',hostOnly:true,domain:'example.com',path:'/'});
  await assert.rejects(applyCookies('logout-rule',loggedOut,policy),/CONFLICT/);
  assert.equal(target.jar.get('session').value,'other-local-account');
});

test('same-value Cookie from an independent login is never adopted for logout', async()=>{
  const target=createProfile();use(target);
  target.jar.set('session',{name:'session',value:'same-cookie-as-source',
    secure:true,httpOnly:true,sameSite:'lax',hostOnly:true,domain:'example.com',path:'/'});
  const original={siteOrigin:policy.siteOrigin,cookieNames:policy.cookieNames,
    cookies:[{name:'session',value:'same-cookie-as-source',secure:true,
      httpOnly:true,sameSite:'lax'}],removedNames:[]};
  await applyCookies('not-owned',original,policy);
  await assert.rejects(applyCookies('not-owned',{siteOrigin:policy.siteOrigin,
    cookieNames:policy.cookieNames,cookies:[],removedNames:['session']},policy),/CONFLICT/);
  assert.equal(target.jar.get('session').value,'same-cookie-as-source');
});

test('logout refuses deleting a Cookie modified by the receiver after sync',async()=>{
  const target=createProfile();use(target);
  const initially={siteOrigin:policy.siteOrigin,cookieNames:policy.cookieNames,
    cookies:[{name:'session',value:'initial-source-cookie',secure:true,
      httpOnly:true,sameSite:'strict'}],removedNames:[]};
  await applyCookies('logout-protected',initially,policy);
  target.jar.get('session').value='changed-by-target-site';
  const removed={siteOrigin:policy.siteOrigin,cookieNames:policy.cookieNames,
    cookies:[],removedNames:['session']};
  await assert.rejects(applyCookies('logout-protected',removed,policy),/CONFLICT/);
  assert.equal(target.jar.get('session').value,'changed-by-target-site');
  await assert.rejects(applyCookies('logout-protected',
    {...removed,removedNames:['session','unexpected']},policy));
});
