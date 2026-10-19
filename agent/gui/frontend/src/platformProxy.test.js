import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {showWindowsCapture,showWindowsService} from './platformProxy.js';

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

test('Windows Wails Go config explicitly exports platform', () => {
  const go = text('../../bindings_windows.go');
  assert.match(go,/Platform\s+string\s+`json:"platform"`/);
  assert.match(go,/Platform:\s+"windows"/);
});

test('Windows transparent proxy switch stays on local proxy page', () => {
  const app = text('./App.jsx');
  const start = app.indexOf('function ProxyPage(');
  const end = app.indexOf('function ExitShare(',start);
  assert.ok(start>=0&&end>start);
  const page = app.slice(start,end);
  assert.match(page,/showWindowsCapture\(config\.platform,networkServiceBridgeAvailable\)/);
  assert.match(page,/windowsCaptureAvailable&&<Card title="Windows 系统透明代理"/);
  assert.match(page,/checked=\{config\.networkMode==='divert'\}/);
  assert.match(page,/setv\('network\.mode',v\?'divert':''\)/);
  assert.match(page,/disabled=\{!canToggleIntercept\}/);
  assert.match(app,/showWindowsService\(config\.platform,hasBridge\('goGetNetworkServiceStatus'\)\)/);
});
