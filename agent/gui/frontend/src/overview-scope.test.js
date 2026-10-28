import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx'),css=read('./styles.css');
const section=(start,end)=>{const a=app.indexOf(start),b=app.indexOf(end,a);assert.ok(a>=0&&b>a);return app.slice(a,b)};
const overview=section('function Overview(','function ServerPage(');
const quick=section('function QuickActions(','function Overview(');
const speed=section('function DeviceSpeedLinkDetails(','function BatchSpeedModal(');
const deviceCenter=section('function DeviceCenter(','function AccessPage(');
const rdp=section('function RDPPage(','function MessagesPage(');

test('Overview focuses on connection, quick actions, aggregate traffic, and recent activity',()=>{
 assert.match(overview,/className="hero overview-hero"/);
 assert.match(overview,/当前运行出口/);
 assert.match(overview,/默认代理出口配置/);
 assert.match(overview,/<QuickActions status=\{status\} config=\{config\} exits=\{exits\} dirty=\{dirty\} onSave=\{onSave\} onSelectExit=\{onSelectExit\} onGoto=\{onGoto\}\/>/);
 assert.match(overview,/className="cards-4"><Metric label="下载速度"/);
 assert.match(overview,/Card title="最近连接"/);
 assert.match(overview,/<PageHead actions=\{<Button onClick=\{onRefresh\}>刷新<\/Button>\}\/>/);
 for(const label of ['代理出口可用路径','UPnP 网关映射','系统流量接管','网络出口"','本机代理"','身份状态','RDP TCP 实际路径'])
  assert.ok(!overview.includes(label),`overview should not duplicate ${label}`);
});

test('quick actions apply immediately and lock while a page holds unsaved drafts',()=>{
 assert.match(quick,/onClick=\{\(\)=>mode!==id&&onSave\(\{routing:\{mode:id\}\}\)\}/);
 assert.match(quick,/<Select aria-label="默认出口" value=\{selectedExit\} disabled=\{dirty\}[^\n]*onChange=\{e=>onSelectExit\(e\.target\.value\)\}/);
 assert.match(quick,/isMac&&<div className="row"><Switch label="macOS 系统代理" checked=\{sys\.mode==='system'\} disabled=\{dirty\} onChange=\{v=>onSave\(\{systemProxy:\{mode:v\?'system':''\}\}\)\}/);
 // Windows divert needs a restart, so the overview only reports it and links to 接入.
 assert.doesNotMatch(quick,/network:\{mode/);
 assert.match(quick,/disabled=\{dirty\} title=\{lock\}/);
});

test('Device Center no longer owns a global current-exit path diagnostic card',()=>{
 assert.doesNotMatch(deviceCenter,/ActiveProxyPath|当前代理出口 · 链路诊断/);
 assert.doesNotMatch(app,/function ActiveProxyPath/);
 assert.doesNotMatch(deviceCenter,/当前代理实际路径/);
 assert.match(deviceCenter,/onSpeed\(selected\.id\)/);
 assert.match(rdp,/rdpPathLabel\(s\.pathTcp\)/);
 assert.match(rdp,/rdpPathLabel\(s\.pathUdp\)/);
});

test('per-device speed dialog is fixed to card target and checks real link evidence one row at a time',()=>{
 assert.match(speed,/function DeviceSpeedLinkDetails\(/);
 assert.match(speed,/deviceSpeedChecks\(\{exitID,exit:matched,connected:online,phase,result,revealCount\}\)/);
 assert.match(speed,/role="listitem" className=\{'device-speed-check is-'\+row\.state\}/);
 assert.match(speed,/className="device-speed-check-icon"/);
 assert.match(speed,/const exitID=initialExit\|\|''/);
 assert.match(speed,/当前测速设备（由设备卡片指定）/);
 assert.doesNotMatch(speed,/label="测试设备 \/ 代理出口"/);
 assert.doesNotMatch(speed,/onChange=\{e=>changeExit\(e\.target\.value\)\}/);
 assert.match(speed,/phase!=='reviewing'/);
 assert.match(speed,/setTimeout\(\(\)=>setRevealCount\(n=>n\+1\),190\)/);
 assert.match(speed,/measurement\.exitId!==requested/);
 assert.match(speed,/Public Direct|链路逐项检查/);
 assert.match(speed,/本机 UPnP 映射/);
 assert.match(speed,/本机 Native UDP/);
 assert.match(speed,/arr\(status\?\.rdpSessions\)\.filter\(s=>s\.deviceId===exitID\)/);
 assert.match(speed,/title="出口双向测速 · 设备链路信息"/);
 assert.match(css,/\.device-speed-check-list\{display:flex;flex-direction:column/);
});
 
test('compact overview maintains low-height behavior on small screens',()=>{
 assert.match(css,/\.overview-hero\{min-height:0;padding:21px 24px/);
 assert.match(css,/@media\(max-width:760px\)\{[\s\S]*?\.overview-hero\{padding:16px\}/);
 assert.match(css,/\.overview-current-exit\{position:relative;z-index:1/);
});
