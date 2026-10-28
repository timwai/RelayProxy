import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx');
const css=read('./styles.css');
const section=(start,end)=>{const i=app.indexOf(start),j=app.indexOf(end,i);assert.ok(i>=0&&j>i);return app.slice(i,j)};

test('active RDP sessions offer a visible, independent reopen-client action',()=>{
 const page=section('function RDPPage(','function MessagesPage(');
 assert.match(page,/const reopening=activeByID\.has\(id\)/);
 assert.match(page,/call\('goConnectRDP',id,true\)/);
 assert.match(page,/已请求再次打开本地远程桌面/);
 assert.match(page,/<Button primary disabled=\{!!busy\|\|!status\.connected\}[^>]*onClick=\{\(\)=>connect\(id\)\}>\{busy===id\?'打开中…':'再次打开'\}<\/Button>/);
 assert.match(page,/<Button danger disabled=\{!!busy\} onClick=\{\(\)=>disconnect\(id\)\}/);
});

test('authorized device rows let connected targets reopen without breaking the first connection',()=>{
 const page=section('function RDPPage(','function MessagesPage(');
 assert.match(page,/<Button primary disabled=\{!x\.online\|\|!status\.connected\|\|!!busy\}/);
 assert.doesNotMatch(page,/disabled=\{!x\.online\|\|!!busy\|\|!!active\}/);
 assert.match(page,/active\?'再次打开':'连接'/);
 const center=section('function DeviceCenter(','function AccessPage(');
 assert.match(center,/const activeRDPTargets=new Set\(arr\(status\.rdpSessions\)/);
 assert.match(center,/activeRDPTargets\.has\(d\.id\)\?'再次打开':'远程桌面'/);
 assert.match(center,/onClick=\{\(\)=>onConnectRDP\(d\.id\)\}/);
});

test('Agent reuses the current target listener and launches only a new local client',()=>{
 const go=read('../../../app/agent.go');
 const i=go.indexOf('if existing := a.rdpConnections[targetID]; existing != nil {');
 const j=go.indexOf('p2pManager := a.rdpP2P',i);
 assert.ok(i>=0&&j>i);
 const reuse=go.slice(i,j);
 assert.match(reuse,/target\.ListenAddr = existing\.ClientAddress\(\)/);
 assert.match(reuse,/if autoLaunch \{/);
 assert.match(reuse,/existing\.Launch\(\)/);
 assert.doesNotMatch(reuse,/\.Close\(\)|StartController\(/);
});

test('RDP session action layout wraps within narrow windows',()=>{
 const page=section('function RDPPage(','function MessagesPage(');
 assert.match(page,/className="target-row rdp-session-row"/);
 assert.match(page,/className="target-row rdp-authorized-row"/);
 assert.match(css,/\.rdp-session-row,\.rdp-authorized-row\{flex-wrap:wrap/);
 assert.match(css,/\.rdp-session-row>\.btn,\.rdp-authorized-row>\.btn\{white-space:nowrap/);
});
