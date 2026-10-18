import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const read=path=>readFileSync(new URL(path,import.meta.url),'utf8');
const app=read('./App.jsx'),css=read('./styles.css');
const section=(start,end)=>{const a=app.indexOf(start),b=app.indexOf(end,a);assert.ok(a>=0&&b>a);return app.slice(a,b)};
const overview=section('function Overview(','function Devices(');
const speed=section('function DeviceSpeedLinkDetails(','function BatchSpeedModal(');
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

test('Device Center no longer owns a global current-exit path diagnostic card',()=>{
 assert.doesNotMatch(deviceCenter,/ActiveProxyPath|当前代理出口 · 链路诊断/);
 assert.doesNotMatch(app,/function ActiveProxyPath/);
 assert.doesNotMatch(deviceCenter,/当前代理实际路径/);
 assert.match(deviceCenter,/onSpeed\(selected\.id\)/);
 assert.match(rdp,/TCP Path/);
 assert.match(rdp,/UDP Path/);
});

test('per-device speed dialog owns measured link modes and separates local gateway state',()=>{
 assert.match(speed,/function DeviceSpeedLinkDetails\(/);
 assert.match(speed,/deviceSpeedLinkEvidence\(exitID,matched,result\)/);
 assert.match(speed,/Public Direct/);
 assert.match(speed,/P2P QUIC/);
 assert.match(speed,/Relay/);
 assert.match(speed,/上行实际路径/);
 assert.match(speed,/下行实际路径/);
 assert.match(speed,/本机 UPnP 映射/);
 assert.match(speed,/本机 Native UDP/);
 assert.match(speed,/status\?\.rdpTargetId===exitID/);
 assert.match(speed,/measurement\.exitId!==requested/);
 assert.match(speed,/title="出口双向测速 · 设备链路信息"/);
 assert.match(css,/\.device-speed-path-list\{display:grid;grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/);
});
 
test('compact overview maintains low-height behavior on small screens',()=>{
 assert.match(css,/\.overview-hero\{min-height:0;padding:21px 24px/);
 assert.match(css,/@media\(max-width:760px\)\{[\s\S]*?\.overview-hero\{padding:16px\}/);
 assert.match(css,/\.overview-current-exit\{position:relative;z-index:1/);
});
