import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const source=path=>readFileSync(new URL('../src/'+path,import.meta.url),'utf8');

test('one registration request sends extension Origin and device keys together',()=>{
  const api=source('server-api.js');
  assert.match(api,/\/api\/v1\/browser-sync\/extensions\/register/);
  assert.match(api,/body:JSON\.stringify\(registration\)/);
  assert.match(api,/signingPublicKey: identity\.signingPublicKey/);
  assert.match(api,/encryptionPublicKey: identity\.encryptionPublicKey/);
  assert.match(api,/approval\.id === identity\.deviceId && approval\.extensionState/);
  assert.match(api,/if\(approval\.state!=='approved'\) return \{state:'extension_pending',legacy:true\}/);
});

test('options UI provides one application action, not repeated extension approval',()=>{
  const html=source('options.html');
  const code=source('options.js');
  assert.match(html,/提交统一授权申请/);
  assert.match(code,/申请已提交/);
  assert.match(code,/身份 ID/);
  assert.match(html,/id="device-name"/);
  assert.doesNotMatch(code,/批准后再次点击注册浏览器设备/);
});
