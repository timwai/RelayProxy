import test from 'node:test';
import assert from 'node:assert/strict';
import {
  shouldReconcile, acceptDeliveryAck, isTerminalAck, RECONCILE_INTERVAL_MS,
  DELIVERY_ACK_TIMEOUT_MS, nextSessionSequence, validOverwriteGrant,
  OVERWRITE_GRANT_TTL_MS, nextReconnectDelay, shouldRetryConnection,
  RECONNECT_BASE_MS, RECONNECT_MAX_MS, singleFlight, nextDeliveryStatus
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

test('MV3 startup and alarm restore share one in-flight run and retry afterwards',async()=>{
  let calls=0,release;
  const gate=new Promise(resolve=>{release=resolve;});
  const restore=singleFlight(async value=>{
    calls++;
    await gate;
    return value;
  });
  const startup=restore('startup');
  const alarm=restore('alarm');
  assert.strictEqual(startup,alarm,'alarm must not trigger a duplicate restore');
  assert.equal(await Promise.resolve().then(()=>calls),1);
  release();
  assert.equal(await alarm,'startup');
  assert.equal(await restore('retry'),'retry');
  assert.equal(calls,2);
});

test('rejected single-flight restore does not block future alarm retries',async()=>{
  let calls=0,release;
  const gate=new Promise(resolve=>{release=resolve;});
  const restore=singleFlight(async()=>{
    calls++;
    if(calls===1){
      await gate;
      throw new Error('WSS temporarily unavailable');
    }
    return 'connected';
  });
  const initial=restore();
  assert.strictEqual(restore(),initial);
  release();
  await assert.rejects(initial,/temporarily unavailable/);
  assert.equal(await restore(),'connected');
  assert.equal(calls,2);
});

test('late RELAYED/SENDING never downgrades APPLIED for the same message',()=>{
  const id='37c0c6b6-5d2f-47aa-a32a-1caa16785511';
  let result=nextDeliveryStatus(null,'SENDING',id,1000);
  result=nextDeliveryStatus(result,'APPLIED',id,1001);
  const immutable=result;
  result=nextDeliveryStatus(result,'RELAYED',id,1002);
  result=nextDeliveryStatus(result,'SENDING',id,1003);
  result=nextDeliveryStatus(result,'UNKNOWN',id,1004);
  assert.strictEqual(result,immutable);
  assert.equal(result.state,'APPLIED');
  assert.equal(result.messageId,id);
});

test('late older delivery cannot overwrite new message status',()=>{
  const oldId='d19e0d8e-421e-48f4-a7c3-e7815f932e1e';
  const newId='b0d6d774-ae1b-4848-a650-28b0de3312e6';
  const past=nextDeliveryStatus(null,'APPLIED',oldId,1000);
  const fresh=nextDeliveryStatus(past,'SENDING',newId,1001);
  assert.equal(fresh.state,'SENDING');
  assert.equal(fresh.messageId,newId);
  assert.strictEqual(nextDeliveryStatus(fresh,'APPLIED',oldId,1002),fresh);
  assert.equal(nextDeliveryStatus(fresh,'RELAYED',newId,1003).state,'RELAYED');
  assert.equal(nextDeliveryStatus(fresh,'APPLIED',newId,1003).state,'APPLIED');
});

test('provisional UNKNOWN can be resolved by a later durable APPLIED receipt',()=>{
  const messageId='93f3fc57-c38e-49c1-8c0b-34e653775b11';
  const unconfirmed=nextDeliveryStatus(null,'SENDING',messageId,1000);
  const unknown=nextDeliveryStatus(unconfirmed,'UNKNOWN',messageId,1001);
  const completed=nextDeliveryStatus(unknown,'APPLIED',messageId,1002);
  assert.equal(completed.state,'APPLIED');
  assert.equal(completed.messageId,messageId);
  assert.strictEqual(nextDeliveryStatus(completed,'RELAYED',messageId,1003),completed);
});
