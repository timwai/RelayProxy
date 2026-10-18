import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx'),css=read('./styles.css');
const section=(start,end)=>{const a=app.indexOf(start),b=app.indexOf(end,a);assert.ok(a>=0&&b>a);return app.slice(a,b)};
const overview=section('function Overview(','function Devices(');
const pathDetails=section('function ActiveProxyPath(','function DeviceCenter(');
const deviceCenter=section('function DeviceCenter(','function ProxyPage(');
const rdp=section('function RDPPage(','function RDPSettingsPage(');

test('Overview focuses on connection, aggregate traffic, and recent activity',()=>{
 assert.match(overview,/className="hero overview-hero"/);
 assert.match(overview,/当前运行出口/);
 assert.match(overview,/默认代理出口配置/);
 assert.match(overview,/className="cards-4"><Metric label="下载速度"/);
 assert.match(overview,/Card title="最近连接"/);
 assert.match(overview,/onGoto\('exits'\)/);
 for(const label of ['代理出口可用路径','UPnP 网关映射','系统流量接管','网络出口"','本机代理"','身份状态','RDP TCP 实际路径'])
  assert.ok(!overview.includes(label),`overview should not duplicate ${label}`);
});

test('live proxy path details are in Device Center with explicit current-session provenance',()=>{
 assert.match(pathDetails,/当前代理出口 · 链路诊断/);
 assert.match(pathDetails,/const \{path,directReady,p2pReady,relay\}=proxyPathStatus\(status\)/);
 assert.match(pathDetails,/当前代理会话的路径，不代表其他设备/);
 assert.match(pathDetails,/Public Direct/);
 assert.match(pathDetails,/P2P QUIC/);
 assert.match(pathDetails,/本机 UPnP/);
 assert.match(pathDetails,/Native UDP/);
 assert.match(deviceCenter,/<ActiveProxyPath status=\{status\} config=\{config\} exits=\{exits\} onGoto=\{onGoto\}\/>/);
 assert.match(css,/\.device-path-grid\{display:grid;grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/);
});

test('actual route is only shown on the matching currently selected proxy device',()=>{
 assert.match(deviceCenter,/selected\.proxy&&status\.connected&&status\.selectedExit===selected\.id/);
 assert.match(deviceCenter,/仅显示本设备报告的候选能力，不套用其他出口的运行态路径/);
 assert.match(deviceCenter,/onSpeed\(selected\.id\)/);
 assert.match(rdp,/TCP Path/);
 assert.match(rdp,/UDP Path/);
 assert.match(app,/function Devices\(/);
});

test('compact overview maintains low-height behavior on small screens',()=>{
 assert.match(css,/\.overview-hero\{min-height:0;padding:21px 24px/);
 assert.match(css,/@media\(max-width:760px\)\{[\s\S]*?\.overview-hero\{padding:16px\}/);
 assert.match(css,/\.overview-current-exit\{position:relative;z-index:1/);
});
