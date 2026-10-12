import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const asset=name=>readFileSync(new URL('../src/'+name,import.meta.url),'utf8');
const optionsHTML=asset('options.html');
const optionsJS=asset('options.js');
const popupHTML=asset('popup.html');
const serverAPI=asset('server-api.js');
const worker=asset('service-worker.js');
const manifest=JSON.parse(readFileSync(new URL('../manifest.json',import.meta.url),'utf8'));

test('website permission is selected from open HTTP or HTTPS tabs in Settings',()=>{
 assert.match(optionsHTML,/<select id="site"/);
 assert.doesNotMatch(optionsHTML,/<input id="site"/);
 assert.match(optionsJS,/chrome\.tabs\.query\(\{\}\)/);
 assert.match(optionsJS,/\['http:','https:'\]\.includes\(u\.protocol\)/);
 assert.match(optionsJS,/chrome\.permissions\.request/);
 assert.ok(manifest.permissions.includes('tabs'));
 assert.doesNotMatch(popupHTML,/<select id="site"/);
});

test('one browser-device registration includes its identity preference and public keys',()=>{
 assert.match(optionsJS,/type:\\s*'REGISTER_BROWSER'/);
 assert.match(worker,/case 'REGISTER_BROWSER'/);
 assert.match(serverAPI,/\/api\/v1\/browser-sync\/devices\/register/);
 assert.match(serverAPI,/requestedIdentityId/);
 assert.match(serverAPI,/signingPublicKey/);
 assert.match(serverAPI,/encryptionPublicKey/);
 assert.doesNotMatch(serverAPI,/extensions\/register|extension_pending/);
});
