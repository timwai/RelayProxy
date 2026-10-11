import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=name=>readFileSync(new URL(name,import.meta.url),'utf8');

test('Browser Sync management approves devices only',()=>{
 const page=read('./BrowserSync.jsx');
 assert.doesNotMatch(page,/setView\('devices'\)/);
 assert.doesNotMatch(page,/setView\('extensions'\)/);
 assert.doesNotMatch(page,/role="tablist"/);
 assert.match(page,/浏览器设备/);
 assert.match(page,/历史禁用来源/);
 assert.doesNotMatch(page,/批准扩展/);
 assert.match(page,/stateFilter==='all'/);
 assert.match(page,/aria-pressed=\{stateFilter===id\}/);
 assert.match(page,/setIdentity/);
 assert.match(page,/send:e\.target\.checked/);
 assert.match(page,/receive:e\.target\.checked/);
 assert.match(page,/action\(d\.id,'approve'\)/);
 assert.match(page,/action\(d\.id,'revoke'\)/);
 assert.match(page,/action\(d\.id,'delete'\)/);
 assert.match(page,/removeLegacyBlock\(ext\.id\)/);
 assert.doesNotMatch(page,/A\/B|来源 A|接收 B/);
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
 assert.match(css,/browser-sync-refined/);
 assert.match(css,/settings-refined/);
});
