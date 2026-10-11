import test from 'node:test';
import assert from 'node:assert/strict';
import {browserRelayHealthMessage} from '../src/server-api.js';

test('shared browser TLS relay probe distinguishes absent service and healthy WebSocket endpoint',()=>{
  assert.match(browserRelayHealthMessage(204),/WSS Upgrade/);
  assert.match(browserRelayHealthMessage(404),/HTTP 404/);
  assert.match(browserRelayHealthMessage(426),/HTTPS\/TLS/);
  assert.match(browserRelayHealthMessage(503),/HTTP 503/);
});

test('a healthy relay probe does not wrongly claim successful device approval',()=>{
  const message=browserRelayHealthMessage(204);
  assert.match(message,/审批/);
  assert.doesNotMatch(message,/认证已完成/);
});
