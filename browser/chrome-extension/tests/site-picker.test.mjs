import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const html=readFileSync(new URL('../src/popup.html',import.meta.url),'utf8');
const popup=readFileSync(new URL('../src/popup.js',import.meta.url),'utf8');
const manifest=JSON.parse(readFileSync(new URL('../manifest.json',import.meta.url),'utf8'));
test('website grant uses a tab picker, not manual URL entry',()=>{
 assert.match(html,/<select id="site"/);
 assert.doesNotMatch(html,/<input id="site"/);
 assert.match(popup,/chrome\.tabs\.query\(\{\}\)/);
 assert.match(popup,/u\.protocol==='https:'/);
 assert.match(popup,/chrome\.permissions\.request/);
 assert.ok(manifest.permissions.includes('tabs'));
});
test('extension ID is reported without manual copying',()=>{
 const api=readFileSync(new URL('../src/server-api.js',import.meta.url),'utf8');
 assert.match(api,/extensions\/register/);
 assert.match(api,/extension_pending/);
});
