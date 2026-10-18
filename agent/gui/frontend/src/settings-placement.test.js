import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const src=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
const body=(from,to)=>{
 const start=src.indexOf('function '+from+'(');
 const end=src.indexOf('function '+to+'(',start);
 assert.ok(start>=0&&end>start,`missing component boundary ${from} → ${to}`);
 return src.slice(start,end);
};

test('RDP and message settings are discoverable under Settings without moving operational pages',()=>{
 assert.match(src,/\['rdp-settings','monitor','远程桌面偏好'/);
 assert.match(src,/\['message-settings','bell','消息与弹窗'/);
 assert.match(src,/function SettingsNavigation\(\{page,onGoto\}\)\{return <SectionNavigation page=\{page\} onGoto=\{onGoto\} kind="settings"\/>\}/);
 assert.match(src,/else if\(page==='rdp-settings'\)content=<RDPSettingsPage/);
 assert.match(src,/else if\(page==='message-settings'\)content=<MessageSettingsPage/);
 assert.match(src,/else if\(page==='rdp'\)content=<RDPPage/);
 assert.match(src,/else if\(page==='messages'\)content=<MessagesPage/);
});

test('remote desktop operations remain on the RDP page while configuration moves to Settings',()=>{
 const operations=body('RDPPage','RDPSettingsPage');
 const settings=body('RDPSettingsPage','MessagesPage');
 assert.match(operations,/goConnectRDP/);
 assert.match(operations,/goDisconnectRDP/);
 assert.match(operations,/已授权设备/);
 assert.doesNotMatch(operations,/启用远程桌面" desc/);
 assert.doesNotMatch(operations,/保存 RDP 设置/);
 assert.match(settings,/setv\('rdp.enabled',v\)/);
 assert.match(settings,/save\(\{rdp:\{enabled:desiredEnabled\}\}\)/);
 assert.match(settings,/config\.restartRequired/);
 assert.match(settings,/goRestart/);
 assert.match(settings,/rdp.controller/);
});

test('message history stays operational and notification controls move to Settings',()=>{
 const messages=body('MessagesPage','MessageSettingsPage');
 const settings=body('MessageSettingsPage','MonitorPage');
 assert.match(messages,/setFilter/);
 assert.match(messages,/message-list/);
 assert.match(messages,/verificationCode/);
 assert.doesNotMatch(messages,/保存弹窗设置/);
 assert.doesNotMatch(messages,/setPreview/);
 assert.match(settings,/verificationPopupTimeoutSec/);
 assert.match(settings,/保存弹窗设置/);
 assert.match(settings,/setPreview\('verification'\)/);
 assert.match(settings,/setPreview\('message'\)/);
 assert.match(settings,/setPreview\('important'\)/);
 assert.match(settings,/PopupPreview type=\{preview\}/);
});
