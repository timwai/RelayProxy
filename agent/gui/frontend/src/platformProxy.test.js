import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {showTransparentCapture,showWindowsCapture,showWindowsService,transparentCaptureReady,captureProtocolLabel} from './platformProxy.js';

test('IPv4-only capability enables the switch and displays its actual protocol range',()=>{
 const caps={tcp:true,udp:true,ipv6:false,process_identity:true,original_destination:true,reply_injection:true,loop_bypass:true,ipv6_unavailable_reason:'ip6tables unavailable'};
 assert.equal(transparentCaptureReady(caps),true);
 assert.equal(captureProtocolLabel(caps),'TCP / UDP · 仅 IPv4');
 assert.equal(captureProtocolLabel({...caps,ipv6:true}),'TCP / UDP · IPv4 / IPv6');
 assert.equal(captureProtocolLabel({...caps,ipv6:true},{captureFamily:'ipv4'}),'TCP / UDP · 仅 IPv4');
 assert.equal(transparentCaptureReady({...caps,unavailable_reason:'IPv4 mangle unavailable'}),false);
 assert.equal(transparentCaptureReady({...caps,reply_injection:false}),false);
});

const text = file => readFileSync(new URL(file,import.meta.url),'utf8');

test('native Windows config keeps transparent proxy controls visible', () => {
  assert.equal(showWindowsCapture('windows',true),true);
  assert.equal(showWindowsService('windows',true),true);
  assert.equal(showWindowsCapture('windows',false),true);
});

test('Windows builds without platform in legacy GetConfig still show controls', () => {
  assert.equal(showWindowsCapture(undefined,true),true);
  assert.equal(showWindowsCapture('',true),true);
  assert.equal(showWindowsService('',true),true);
});

test('macOS and other platforms do not show Windows-only network service', () => {
  for (const name of ['darwin','linux']) {
    assert.equal(showWindowsCapture(name,true),false);
    assert.equal(showWindowsService(name,true),false);
  }
  assert.equal(showWindowsCapture(undefined,false),false);
  assert.equal(showWindowsService('windows',false),false);
});

test('Linux browser UI exposes transparent capture without a Windows bridge', () => {
  assert.equal(showTransparentCapture('linux',false),true);
  assert.equal(showTransparentCapture(' LINUX ',false),true);
  assert.equal(showWindowsService('linux',false),false);
  assert.equal(showWindowsService('linux',true),false);
  assert.equal(showTransparentCapture('windows',false),true);
  assert.equal(showTransparentCapture(undefined,true),true);
  assert.equal(showTransparentCapture('darwin',false),false);
  assert.equal(showTransparentCapture('darwin',true),false);
  assert.equal(showTransparentCapture(undefined,false),false);
});

test('Windows Wails Go config explicitly exports platform', () => {
  const go = text('../../bindings_windows.go');
  assert.match(go,/Platform\s+string\s+`json:"platform"`/);
  assert.match(go,/Platform:\s+"windows"/);
});

test('transparent proxy switch and Windows service maintenance live on the 接入 page', () => {
  const app = text('./App.jsx');
  const start = app.indexOf('function AccessPage(');
  const end = app.indexOf('function ExitShare(',start);
  assert.ok(start>=0&&end>start);
  const page = app.slice(start,end);
  assert.match(page,/showTransparentCapture\(config\.platform,networkServiceBridgeAvailable\)/);
  assert.match(page,/transparentCaptureAvailable&&<Card title=\{captureTitle\}/);
  assert.match(page,/checked=\{config\.networkMode==='divert'\}/);
  assert.match(page,/setv\('network\.mode',v\?'divert':''\)/);
  assert.match(page,/disabled=\{!canToggleIntercept\}/);
  assert.match(page,/showWindowsService\(config\.platform,networkServiceBridgeAvailable\)&&<details className="details top-gap"><summary>Network Service 维护<\/summary>/);
});
