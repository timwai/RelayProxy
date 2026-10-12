import test from 'node:test';
import assert from 'node:assert/strict';
import {consumeOneTimeOverwrite,setOverwritePermission,pruneOverwriteGrants} from '../src/session-sync.js';
import {OVERWRITE_GRANT_TTL_MS} from '../src/lifecycle.js';

const KEY='browserSyncAllowOverwrite';
const token='01234567-89ab-4cde-a321-112233445566';
const otherToken='11111111-89ab-4cde-a321-112233445566';
const origin='https://example.com';
function grant(requestId=token,siteOrigin=origin,expiresAt=Date.now()+OVERWRITE_GRANT_TTL_MS){
  return {siteOrigin,transferRequestId:requestId,expiresAt};
}
function withFakeChrome(initial){
  let data={[KEY]:initial};
  globalThis.chrome={storage:{local:{
    async get(key){return {[key]:data[key]};},
    async set(patch){data={...data,...patch};}
  }}};
  return ()=>data[KEY];
}

test('only signed snapshot with matching user request ID consumes overwrite permission',async()=>{
  const view=withFakeChrome({'rule-id':grant()});
  assert.equal(await consumeOneTimeOverwrite('rule-id',origin),false,
    'unsolicited background snapshot cannot borrow user consent');
  assert.deepEqual(view()['rule-id'],grant(token,origin,view()['rule-id'].expiresAt));
  assert.equal(await consumeOneTimeOverwrite('rule-id',origin,otherToken),false,
    'a different requested snapshot cannot borrow user consent');
  assert.ok(view()['rule-id'],'a different request cannot consume user consent');
  assert.equal(await consumeOneTimeOverwrite('rule-id',origin,token),true);
  assert.equal(view()['rule-id'],undefined);
  assert.equal(await consumeOneTimeOverwrite('rule-id',origin,token),false,
    'a replay of the previously signed snapshot cannot reuse consent');
});

test('expired, legacy and wrong-origin permissions fail closed',async()=>{
  const now=Date.now();
  const view=withFakeChrome({
    legacy:true,
    legacyGrant:{siteOrigin:origin,expiresAt:now+OVERWRITE_GRANT_TTL_MS},
    expired:grant(token,origin,now-1),
    different:grant(token,'https://other.example',now+OVERWRITE_GRANT_TTL_MS)
  });
  assert.equal(await consumeOneTimeOverwrite('legacy',origin,token),false);
  assert.equal(await consumeOneTimeOverwrite('legacyGrant',origin,token),false);
  assert.equal(await consumeOneTimeOverwrite('expired',origin,token),false);
  assert.equal(await consumeOneTimeOverwrite('different',origin,token),false);
  assert.ok(view().different,'valid different-origin grant is not discarded by unrelated snapshot');
  await pruneOverwriteGrants();
  assert.deepEqual(view(),{different:view().different});
});

test('startup prunes legacy booleans while preserving valid token-bound grants',async()=>{
  const valid=grant();
  const view=withFakeChrome({old:true,oldObject:{siteOrigin:origin,expiresAt:valid.expiresAt},fresh:valid});
  await pruneOverwriteGrants();
  assert.deepEqual(view(),{fresh:valid});
});

test('request-bound consent can always be cancelled offline',async()=>{
  const view=withFakeChrome({cancel:grant()});
  assert.deepEqual(await setOverwritePermission('cancel',false),
    {ruleId:'cancel',allowed:false,expiresAt:null});
  assert.deepEqual(view(),{});
  assert.equal(await consumeOneTimeOverwrite('cancel',origin,token),false);
});
