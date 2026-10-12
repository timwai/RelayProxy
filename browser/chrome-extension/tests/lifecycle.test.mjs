import test from 'node:test';
import assert from 'node:assert/strict';
import {
  shouldReconcile, acceptDeliveryAck, isTerminalAck, RECONCILE_INTERVAL_MS,
  DELIVERY_ACK_TIMEOUT_MS, nextSessionSequence, validOverwriteGrant,
  OVERWRITE_GRANT_TTL_MS, nextReconnectDelay, shouldRetryConnection,
  RECONNECT_BASE_MS, RECONNECT_MAX_MS
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

test('replay cursor resumes after local storage loss without decreasing sequence', () => {
  assert.equal(nextSessionSequence(0,0),1);
  assert.equal(nextSessionSequence(0,42),43);
  assert.equal(nextSessionSequence(102,42),103);
  assert.equal(nextSessionSequence(10,102),103);
  assert.throws(()=>nextSessionSequence(-1,0));
  assert.throws(()=>nextSessionSequence(0,Number.MAX_SAFE_INTEGER),/耗尽/);
  assert.throws(()=>nextSessionSequence(0,Number.MAX_SAFE_INTEGER+1));
  assert.throws(()=>nextSessionSequence(1.5,2));
});

test('one-use overwrite grant rejects legacy booleans, other origins and expiry',()=>{
  const now=1770000000000,origin='https://example.com';
  const token='01234567-89ab-4cde-a321-112233445566';
  const grant={siteOrigin:origin,transferRequestId:token,
    expiresAt:now+OVERWRITE_GRANT_TTL_MS};
  assert.equal(validOverwriteGrant(grant,origin,now,token),true);
  assert.equal(validOverwriteGrant(grant,origin,now+OVERWRITE_GRANT_TTL_MS-1,token),true);
  assert.equal(validOverwriteGrant(grant,origin,now+OVERWRITE_GRANT_TTL_MS,token),false);
  assert.equal(validOverwriteGrant(grant,'https://evil.example',now,token),false);
  assert.equal(validOverwriteGrant(grant,origin,now,'11111111-89ab-4cde-a321-112233445566'),false);
  assert.equal(validOverwriteGrant(grant,origin,now,''),false);
  assert.equal(validOverwriteGrant({...grant,expiresAt:now+OVERWRITE_GRANT_TTL_MS+1},origin,now,token),false);
  assert.equal(validOverwriteGrant(true,origin,now,token),false);
  assert.equal(validOverwriteGrant({siteOrigin:origin,expiresAt:grant.expiresAt},origin,now,token),false);
  assert.equal(validOverwriteGrant(null,origin,now,token),false);
});

test('MV3 reconnect retry survives alarm wakeups and caps exponential delay',()=>{
  const origin='https://relay.example',now=1770000000000;
  assert.equal(nextReconnectDelay(1),RECONNECT_BASE_MS);
  assert.equal(nextReconnectDelay(2),2*RECONNECT_BASE_MS);
  assert.equal(nextReconnectDelay(10),RECONNECT_MAX_MS);
  assert.equal(nextReconnectDelay(100),RECONNECT_MAX_MS);
  assert.equal(shouldRetryConnection(null,origin,now),true);
  assert.equal(shouldRetryConnection({serverOrigin:origin,failures:2,nextRetryAt:now+120000},origin,now),false);
  assert.equal(shouldRetryConnection({serverOrigin:origin,failures:2,nextRetryAt:now-1},origin,now),true);
  assert.equal(shouldRetryConnection({serverOrigin:'https://other.example',nextRetryAt:now+120000},origin,now),true);
  assert.equal(shouldRetryConnection({serverOrigin:origin,nextRetryAt:now+24*3600000},origin,now),true);
  assert.equal(shouldRetryConnection(null,'',now),false);
});
