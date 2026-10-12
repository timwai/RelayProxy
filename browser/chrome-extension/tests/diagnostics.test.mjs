import test from 'node:test';
import assert from 'node:assert/strict';
import {browserRestoreFailureCode} from '../src/session-sync.js';

// Website-provided exception text must never be forwarded to Server.
// Only a fixed code may appear in the authenticated ACK.
test('browser sync restore diagnostics are fixed-category, not raw messages',()=>{
  const cases=[
    ['PARTIAL_ROLLBACK: local session and cookie conflict','PARTIAL_ROLLBACK'],
    ['CONFLICT: existing independent account','CONFLICT'],
    ['旧会话快照已拒绝','REPLAY_REJECTED'],
    ['IndexedDB version conflict','INDEXEDDB_FAILURE'],
    ['Cookie write failed','COOKIE_FAILURE'],
    ['Storage tab missing','STORAGE_FAILURE'],
    ['decrypt failed','DECRYPT_FAILED'],
    ['network proxy offline','NETWORK_UNAVAILABLE'],
    ['sensitive-site-name.test/secret?cookie=session-token','UNSPECIFIED'],
  ];
  for(const [message,expected] of cases){
    const code=browserRestoreFailureCode(new Error(message));
    assert.equal(code,expected);
    assert.ok(/^[A-Z_]+$/.test(code));
    assert.ok(!code.includes('secret'));
  }
  assert.equal(browserRestoreFailureCode(null),'UNSPECIFIED');
});
