import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=name=>readFileSync(new URL(name,import.meta.url),'utf8');

test('Browser Sync keeps device approval in one place and adds rules and audit monitoring',()=>{
 const page=read('./BrowserSync.jsx');
 assert.match(page,/\['devices','浏览器设备'\]/);
 assert.match(page,/\['rules','配对规则'\]/);
 assert.match(page,/\['audit','同步记录'\]/);
 assert.doesNotMatch(page,/批准扩展/);
 assert.match(page,/stateFilter==='all'/);
 assert.match(page,/aria-pressed=\{stateFilter===id\}/);
 assert.match(page,/const validIdentity=id=>activeIdentities\.some/);
 assert.match(page,/disabled=\{!!busy\|\|!validIdentity\(d\.id\)/);
 assert.match(page,/send:e\.target\.checked/);
 assert.match(page,/receive:e\.target\.checked/);
 assert.match(page,/deviceAction\(d\.id,'approve'\)/);
 assert.match(page,/deviceAction\(d\.id,'revoke'\)/);
 assert.match(page,/deviceAction\(d\.id,'delete'\)/);
 assert.match(page,/removeLegacyBlock\(ext\.id\)/);
 assert.doesNotMatch(page,/A\/B|来源 A|接收 B/);
});

test('Browser Sync polls only presence and allows metadata rule transitions and audit pagination',()=>{
 const page=read('./BrowserSync.jsx');
 assert.match(page,/\/browser-sync\/admin\/status/);
 assert.match(page,/\/browser-sync\/admin\/rules/);
 assert.match(page,/\/browser-sync\/admin\/audit\?limit=50/);
 assert.match(page,/setInterval\(async\(\)=>\{/);
 assert.match(page,/return \(\)=>clearInterval\(id\)/);
 assert.match(page,/ruleAction\(r,'pause'\)/);
 assert.match(page,/ruleAction\(r,'resume'\)/);
 assert.match(page,/ruleAction\(r,'revoke'\)/);
 assert.match(page,/\/browser-sync\/admin\/audit\?limit=50&before=/);
 assert.match(page,/auditFilter==='all'/);
 assert.match(page,/auditDevice\?'&device='/);
 assert.match(page,/aria-label="筛选浏览器设备"/);
 assert.match(page,/audit\.nextCursor>0/);
 assert.match(page,/fmtDate\(stat\.lastSyncAt\)/);
 assert.doesNotMatch(page,/policyDigest|offer\.ciphertext|signingPublicKey|encryptionPublicKey/);
});

test('Server settings keeps category navigation, every editable field and explicit Save',()=>{
 const app=read('./App.jsx'),css=read('./react.css');
 assert.match(app,/settings-layout settings-refined/);
 assert.match(app,/settings-nav-item/);
 assert.match(app,/group\.fields\.map/);
 assert.match(app,/const config=normalizeForSave\(draft,saved\.config\)/);
 assert.match(app,/validatePortRange\(draft\)/);
 assert.match(app,/onClick=\{save\}>保存配置/);
 assert.match(app,/disabled=\{clean\|\|saving\}/);
 assert.match(app,/<SystemGrants ctx=\{ctx\}/);
 assert.match(css,/browser-sync-mode-switch/);
 assert.match(css,/browser-sync-refined/);
 assert.match(css,/settings-refined/);
});
