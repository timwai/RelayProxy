import test from 'node:test';
import assert from 'node:assert/strict';
import {webcrypto} from 'node:crypto';

globalThis.crypto ||= webcrypto;

// Two independent import URLs act as the MV3 service worker and Options realm.
// Both share a serialized IndexedDB database, never a JS process-local mutex.
function chromeProfile() {
  const keys=new Map(), storage=new Map();
  let writeGate=Promise.resolve();
  const db={
    createObjectStore(){},
    close(){},
    transaction(_name,mode){
      const tx={};
      let release,active=0,completed=false;
      const ready=mode==='readwrite'?writeGate:Promise.resolve();
      if(mode==='readwrite'){
        const done=new Promise(resolve=>{release=resolve;});
        writeGate=ready.then(()=>done);
      }
      function requestFor(fn) {
        const request={};active++;
        ready.then(()=>{
          try {request.result=fn();request.onsuccess?.();}
          catch(error){request.error=error;request.onerror?.();tx.onerror?.();}
          finally{
            active--;
            queueMicrotask(()=>{
              if(active!==0||completed)return;
              completed=true;
              tx.oncomplete?.();
              release?.();
            });
          }
        });
        return request;
      }
      tx.objectStore=()=>({
        get(name){return requestFor(()=>keys.get(name));},
        put(value,name){return requestFor(()=>keys.set(name,value));},
        delete(name){return requestFor(()=>keys.delete(name));}
      });
      return tx;
    }
  };
  return {
    keys,storage,
    install(){
      globalThis.indexedDB={open(){
        const q={result:db};
        queueMicrotask(()=>{q.onupgradeneeded?.();q.onsuccess?.();});
        return q;
      }};
      globalThis.chrome={storage:{local:{
        async get(name){return {[name]:storage.get(name)};},
        async set(obj){
          // Distinct microtask from the IndexedDB commit, matching Chrome's
          // real chrome.storage.local API and exercising its stale-ID window.
          await Promise.resolve();
          for(const [k,v] of Object.entries(obj))storage.set(k,v);
        }
      }}};
    }
  };
}
let unique=0;
async function realms(){
  const id=++unique;
  const [options,worker]=await Promise.all([
    import(`../src/device-identity.js?options=${id}`),
    import(`../src/device-identity.js?worker=${id}`)
  ]);
  return {options,worker};
}

test('fresh parallel Options and MV3 worker create ONE atomic identity; key loss rotates both',async()=>{
  const p=chromeProfile();p.install();
  const {options,worker}=await realms();
  const initial=await Promise.all(Array.from({length:12},(_,i)=>
    (i%2?options:worker).getOrCreateIdentity()));
  assert.ok(initial.every(x=>x.deviceId===initial[0].deviceId));
  assert.ok(initial.every(x=>x.signingPublicKey===initial[0].signingPublicKey));
  assert.ok(initial.every(x=>x.encryptionPublicKey===initial[0].encryptionPublicKey));
  assert.equal(p.keys.get('device-identity-v2').deviceId,initial[0].deviceId);
  assert.equal(p.keys.has('signing'),false);
  assert.equal(p.keys.has('encryption'),false);

  p.keys.delete('device-identity-v2'); // damaged or reset Profile IDB keys
  // chrome.storage.local still carries the OLD approved ID. That ID MUST
  // never get rebound to the newly generated P-256 signing/encryption keys.
  assert.equal(p.storage.get('browserDeviceId'),initial[0].deviceId);
  const rotated=await Promise.all(Array.from({length:12},(_,i)=>
    (i%2?options:worker).getOrCreateIdentity()));
  assert.notEqual(rotated[0].deviceId,initial[0].deviceId);
  assert.ok(rotated.every(x=>x.deviceId===rotated[0].deviceId));
  assert.ok(rotated.every(x=>x.signingPublicKey===rotated[0].signingPublicKey));
  assert.ok(rotated.every(x=>x.encryptionPublicKey===rotated[0].encryptionPublicKey));
  assert.notEqual(rotated[0].signingPublicKey,initial[0].signingPublicKey);
  assert.notEqual(rotated[0].encryptionPublicKey,initial[0].encryptionPublicKey);
  assert.equal(p.storage.get('browserDeviceId'),rotated[0].deviceId);
  await assert.rejects(worker.signChallenge({
    serverOrigin:'https://server.example',deviceId:initial[0].deviceId,challenge:'new'
  }),/mismatch/);
  assert.equal(p.keys.get('device-identity-v2').deviceId,rotated[0].deviceId);
});

test('legacy profile migrates previously approved keypairs and ID only if BOTH keys exist',async()=>{
  const p=chromeProfile();p.install();
  const previous='browser_'+crypto.randomUUID();
  const signing=await crypto.subtle.generateKey({name:'ECDSA',namedCurve:'P-256'},false,['sign','verify']);
  const encryption=await crypto.subtle.generateKey({name:'ECDH',namedCurve:'P-256'},false,['deriveKey','deriveBits']);
  p.keys.set('signing',signing);p.keys.set('encryption',encryption);
  p.storage.set('browserDeviceId',previous);
  const {options,worker}=await realms();
  const [a,b]=await Promise.all([options.getOrCreateIdentity(),worker.getOrCreateIdentity()]);
  assert.equal(a.deviceId,previous);
  assert.deepEqual(a,b);
  assert.equal(p.keys.get('device-identity-v2').signing.privateKey,signing.privateKey);
  assert.equal(p.keys.get('device-identity-v2').encryption.privateKey,encryption.privateKey);
  assert.equal(p.keys.has('signing'),false,'legacy signing key must be removed after migration');
  assert.equal(p.keys.has('encryption'),false,'legacy ECDH key must be removed after migration');
});

test('partial legacy key loss and corrupt atomic bundle never inherit old approval',async()=>{
  const p=chromeProfile();p.install();
  const old='browser_'+crypto.randomUUID();
  p.storage.set('browserDeviceId',old);
  p.keys.set('signing',await crypto.subtle.generateKey(
    {name:'ECDSA',namedCurve:'P-256'},false,['sign','verify']));
  const {options}=await realms();
  const fresh=await options.getOrCreateIdentity();
  assert.notEqual(fresh.deviceId,old,'partial legacy signing key must not preserve old device ID');
  assert.equal(p.keys.has('signing'),false);
  p.keys.set('device-identity-v2',{deviceId:fresh.deviceId,signing:{}});
  const newIdentity=await options.getOrCreateIdentity();
  assert.notEqual(newIdentity.deviceId,fresh.deviceId,'corrupt bundle must rotate ID and both keys');
  assert.notEqual(newIdentity.signingPublicKey,fresh.signingPublicKey);
  // A truthy object is not sufficient: unusable or wrongly scoped key material
  // must rotate the identity before the old approved ID can be reused.
  p.keys.set('device-identity-v2',{
    deviceId:newIdentity.deviceId,
    signing:{publicKey:{},privateKey:{}},
    encryption:{publicKey:{},privateKey:{}}
  });
  const afterFakeKeys=await options.getOrCreateIdentity();
  assert.notEqual(afterFakeKeys.deviceId,newIdentity.deviceId);
  const wrongCurve=await crypto.subtle.generateKey(
    {name:'ECDSA',namedCurve:'P-384'},false,['sign','verify']);
  p.keys.set('device-identity-v2',{
    deviceId:afterFakeKeys.deviceId,
    signing:wrongCurve,
    encryption:p.keys.get('device-identity-v2').encryption
  });
  const afterWrongCurve=await options.getOrCreateIdentity();
  assert.notEqual(afterWrongCurve.deviceId,afterFakeKeys.deviceId);
});
