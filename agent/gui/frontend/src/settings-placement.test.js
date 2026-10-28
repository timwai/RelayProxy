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

// Each feature page carries its own preferences, so there is no separate
// "settings" destination for RDP or messages and no settings sub-navigation.
test('feature pages own their preferences; 设置 is a single low-frequency page',()=>{
 assert.doesNotMatch(src,/function RDPSettingsPage\(|function MessageSettingsPage\(/);
 assert.doesNotMatch(src,/'rdp-settings','monitor'|'message-settings','bell'/);
 assert.match(src,/else if\(page==='rdp'\)content=<RDPPage [^\n]*setv=\{setv\} save=\{save\} dirty=\{dirty\} onRestart=/);
 assert.match(src,/else if\(page==='messages'\)content=<MessagesPage [^\n]*config=\{config\} setv=\{setv\} save=\{save\} dirty=\{dirty\}\/>/);
 assert.match(src,/else content=<SettingsPage config=\{config\} setv=\{setv\} save=\{save\} toast=\{toast\} onReload=/);
});

test('remote desktop page keeps operations first and folds the enable switch into a preferences card',()=>{
 const page=body('RDPPage','MessagesPage');
 const sessions=page.indexOf('goConnectRDP'),prefs=page.indexOf('<Card title="远程桌面偏好" eyebrow="PREFERENCES"');
 assert.ok(sessions>=0&&prefs>sessions,'preferences card must come after RDP operations');
 assert.match(page,/goDisconnectRDP/);
 assert.match(page,/已授权设备/);
 assert.match(page,/setv\('rdp.enabled',v\)/);
 assert.match(page,/save\(\{rdp:\{enabled:desiredEnabled\}\}\)/);
 assert.match(page,/rdpRestartPending&&<div className="actions end top-gap"><Button primary onClick=\{onRestart\}>/);
 assert.match(page,/rdp\.controller/);
});

test('messages page keeps history first and folds popup preferences into a bottom card',()=>{
 const page=body('MessagesPage','MonitorPage');
 const list=page.indexOf('className="message-list"'),prefs=page.indexOf('<Card title="弹窗偏好" eyebrow="PREFERENCES"');
 assert.ok(list>=0&&prefs>list,'popup preferences must come after the message list');
 assert.match(page,/setFilter/);
 assert.match(page,/verificationCode/);
 assert.match(page,/setv\('verificationPopupTimeoutSec',Number\(e\.target\.value\)\)/);
 assert.match(page,/save\(\{gui:\{verificationPopupTimeoutSec:Number\(config\.verificationPopupTimeoutSec\)\}\}\)/);
 for(const kind of ['verification','message','important'])assert.match(page,new RegExp("setPreview\\('"+kind+"'\\)"));
 assert.match(page,/<PopupPreview type=\{preview\}\/>/);
});

test('设置 page no longer hosts Windows network service maintenance',()=>{
 const page=body('SettingsPage','PopupPreview');
 assert.doesNotMatch(page,/goGetNetworkServiceStatus|goUninstallNetworkService|Windows 网络服务/);
 assert.match(page,/<Card title="外观与窗口" eyebrow="APPEARANCE"/);
 assert.match(page,/<Card title="启动与维护" eyebrow="APPLICATION"/);
 const access=body('AccessPage','ExitShare');
 assert.match(access,/goRepairNetworkService/);
 assert.match(access,/goUninstallNetworkService/);
});
