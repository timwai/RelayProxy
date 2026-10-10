import test from 'node:test';
import assert from 'node:assert/strict';
import {
  shouldReconcile, acceptDeliveryAck, isTerminalAck, RECONCILE_INTERVAL_MS,
  DELIVERY_ACK_TIMEOUT_MS
} from '../src/lifecycle.js';

test('reconcile on new profile or after inactivity, not every minute', () => {
  const now=1770000000000;
  assert.equal(shouldReconcile(undefined,now),true);
  assert.equal(shouldReconcile(now-60000,now),false);
  assert.equal(shouldReconcile(now-RECONCILE_INTERVAL_MS,now),true);
  assert.equal(shouldReconcile(now+1,now),true);
  assert.equal(shouldReconcile(now-1,now),false);
});

test('ACK is correlated to the latest outbound message only', () => {
  const now=1770000000000;
  const latest={ruleId:'r1',messageId:'latest',createdAt:now-3000};
  const matching={ruleId:'r1',messageId:'latest',status:'APPLIED'};
  assert.equal(acceptDeliveryAck(latest,matching,now),true);
  assert.equal(acceptDeliveryAck(latest,{...matching,messageId:'older'},now),false);
  assert.equal(acceptDeliveryAck(latest,{...matching,ruleId:'r2'},now),false);
  assert.equal(acceptDeliveryAck(latest,{...matching,status:'LOGIN_VERIFIED'},now),false);
  assert.equal(acceptDeliveryAck(latest,matching,now+DELIVERY_ACK_TIMEOUT_MS),false);
  assert.equal(isTerminalAck('RECEIVED'),false);
  assert.equal(isTerminalAck('APPLIED'),true);
  assert.equal(isTerminalAck('CONFLICT'),true);
});
