import test from 'node:test';
import assert from 'node:assert/strict';
import {
  PENDING_ENROLLMENT_KEY,ENROLLMENT_POLL_MS,
  validPendingEnrollment,enrollmentPollDue
} from '../src/enrollment-auto.js';

const settings={serverOrigin:'https://relay.example:21000',
  requestedIdentityId:'identity_1',deviceName:'Office Chrome'};

test('background auto-approval needs a specific user-submitted pending identity',()=>{
  const enrollment={serverOrigin:settings.serverOrigin,deviceId:'browser_a',
    requestedIdentityId:settings.requestedIdentityId,
    deviceName:settings.deviceName,lastCheckedAt:Date.now()};
  assert.equal(PENDING_ENROLLMENT_KEY,'browserSyncPendingEnrollment');
  assert.equal(validPendingEnrollment(enrollment,settings,'browser_a'),true);
  assert.equal(validPendingEnrollment(null,settings,'browser_a'),false);
  assert.equal(validPendingEnrollment(enrollment,settings,'browser_b'),false);
  assert.equal(validPendingEnrollment(enrollment,{...settings,
    serverOrigin:'https://attacker.example'},'browser_a'),false);
  assert.equal(validPendingEnrollment(enrollment,{...settings,
    requestedIdentityId:'other'},'browser_a'),false);
  assert.equal(validPendingEnrollment(enrollment,{...settings,
    deviceName:'other'},'browser_a'),false);
  assert.equal(validPendingEnrollment(enrollment,{...settings,
    requestedIdentityId:''},'browser_a'),false);
  assert.equal(validPendingEnrollment(enrollment,{...settings,
    deviceName:''},'browser_a'),false);
});

test('pending approval recheck is rate-limited and survives MV3 worker restart',()=>{
  const base=1_790_000_000_000;
  const enrollment={lastCheckedAt:base};
  assert.equal(enrollmentPollDue(enrollment,base),false);
  assert.equal(enrollmentPollDue(enrollment,base+ENROLLMENT_POLL_MS-1),false);
  assert.equal(enrollmentPollDue(enrollment,base+ENROLLMENT_POLL_MS),true);
  assert.equal(enrollmentPollDue(enrollment,base-100),true);
  assert.equal(enrollmentPollDue({...enrollment,lastCheckedAt:null},base),true);
  assert.equal(enrollmentPollDue(null,base),false);
});
