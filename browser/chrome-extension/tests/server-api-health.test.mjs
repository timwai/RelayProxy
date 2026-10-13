import test from 'node:test';
import assert from 'node:assert/strict';
import {browserRelayHealthMessage,browserWSSFailureMessage} from '../src/server-api.js';

test('shared browser TLS relay probe distinguishes absent service and healthy WebSocket endpoint',()=>{
  assert.match(browserRelayHealthMessage(204),/只代表 HTTPS 入口可达/);
  assert.match(browserRelayHealthMessage(404),/HTTP 404/);
  assert.match(browserRelayHealthMessage(426),/HTTPS\/TLS/);
  assert.match(browserRelayHealthMessage(503),/HTTP 503/);
});

test('a healthy relay probe does not wrongly claim successful device approval',()=>{
  const message=browserRelayHealthMessage(204);
  assert.match(message,/无法证明 WebSocket Upgrade 或设备认证成功/);
  assert.doesNotMatch(message,/认证已完成/);
});

test('WSS error diagnosis uses actual WebSocket lifecycle stage, not health 204',()=>{
  assert.match(browserWSSFailureMessage('upgrade'),/尚未触发 open/);
  assert.match(browserWSSFailureMessage('hello',1008),/AUTH_HELLO.*1008/);
  assert.match(browserWSSFailureMessage('challenge'),/AUTH_CHALLENGE/);
  assert.match(browserWSSFailureMessage('proof',1008),/AUTH_PROOF.*1008/);
  assert.doesNotMatch(browserWSSFailureMessage('proof'),/Upgrade 未完成/);
});
