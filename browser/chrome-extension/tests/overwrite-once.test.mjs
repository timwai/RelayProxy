import test from 'node:test';
import assert from 'node:assert/strict';
import { consumeOneTimeOverwrite, setOverwritePermission, pruneOverwriteGrants } from '../src/session-sync.js';
import { OVERWRITE_GRANT_TTL_MS } from '../src/lifecycle.js';

const KEY='browserSyncAllowOverwrite';
function withFakeChrome(initial){
  let data={ [KEY]:initial };
  globalThis.chrome={storage:{local:{
    async get(key){return { [key]:data[key] };},
    async set(patch){data={...data,...patch};}
  }}};
  return ()=>data[KEY];
}

test('override is atomically single-use for its exact site, not an ongoing switch',async()=>{
  const grant={siteOrigin:'https://example.com',expiresAt:Date.now()+OVERWRITE_GRANT_TTL_MS};
  const view=withFakeChrome({'rule-id':grant});
  assert.equal(await consumeOneTimeOverwrite('rule-id','https://example.com'),true);
  assert.equal(view()['rule-id'],undefined);
  assert.equal(await consumeOneTimeOverwrite('rule-id','https://example.com'),false);
});

test('mismatched, expired and legacy boolean grants are removed and rejected',async()=>{
  const now=Date.now();
  const view=withFakeChrome({
    legacy:true,
    different:{siteOrigin:'https://other.example',expiresAt:now+OVERWRITE_GRANT_TTL_MS},
    expired:{siteOrigin:'https://example.com',expiresAt:now-1}
  });
  assert.equal(await consumeOneTimeOverwrite('legacy','https://example.com'),false);
  assert.equal(await consumeOneTimeOverwrite('different','https://example.com'),false);
  assert.equal(await consumeOneTimeOverwrite('expired','https://example.com'),false);
  assert.deepEqual(view(),{});
});

test('legacy grants are pruned on worker start while valid short-lived grant survives',async()=>{
  const valid={siteOrigin:'https://example.com',expiresAt:Date.now()+OVERWRITE_GRANT_TTL_MS};
  const view=withFakeChrome({old:true,fresh:valid});
  await pruneOverwriteGrants();
  assert.deepEqual(view(),{fresh:valid});
});

test('one-time grant can always be cancelled offline after a failed sync request',async()=>{
  const view=withFakeChrome({
    cancel:{siteOrigin:'https://example.com',expiresAt:Date.now()+OVERWRITE_GRANT_TTL_MS}
  });
  assert.deepEqual(await setOverwritePermission('cancel',false),
    {ruleId:'cancel',allowed:false,expiresAt:null});
  assert.deepEqual(view(),{});
  assert.equal(await consumeOneTimeOverwrite('cancel','https://example.com'),false);
});
