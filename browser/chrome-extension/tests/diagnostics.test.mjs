import test from 'node:test';
import assert from 'node:assert/strict';
import {browserRestoreFailureCode} from '../src/session-sync.js';
import {syncFailure,failureDiagnostic,safeDiagnostic,diagnosticText,ruleTransferStatus} from '../src/sync-diagnostics.js';

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
    ['browser sync rate limit exceeded','NETWORK_UNAVAILABLE'],
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

test('local diagnostics retain the failure stage and original reason through rollback wrappers',()=>{
  const cause=syncFailure('Cookie write failed: secret-token','COOKIE_MISMATCH');
  const wrapped=syncFailure('PARTIAL_ROLLBACK: incomplete',null,cause);
  const diagnostic=failureDiagnostic(wrapped,'apply_cookies');
  assert.deepEqual(diagnostic,{errorCode:'PARTIAL_ROLLBACK',stage:'apply_cookies',reason:'COOKIE_MISMATCH'});
  assert.match(diagnosticText(diagnostic),/写入 Cookie.*属性或值/);
  assert.doesNotMatch(JSON.stringify(diagnostic),/secret-token/);
});

test('untrusted or legacy diagnostic fields cannot display or persist session contents',()=>{
  const malicious={stage:'https://private-site/token',reason:'SESSION=secret',errorCode:'token=secret',message:'secret'};
  assert.deepEqual(safeDiagnostic(malicious),{});
  assert.equal(diagnosticText(malicious),'');
  assert.deepEqual(failureDiagnostic(new Error('https://private-site/secret'),'token=secret'),{errorCode:'UNSPECIFIED'});
  assert.match(diagnosticText({errorCode:'PARTIAL_ROLLBACK',stage:'remote_restore'}),/接收端恢复.*暂停/);
  assert.equal(diagnosticText(null),'');
});

test('paused summaries retain the original failure and recognize all restore journals',()=>{
 const previous={state:'FAILED',time:123,stage:'apply_cookies',reason:'COOKIE_MISMATCH',errorCode:'PARTIAL_ROLLBACK'};
 for(const key of ['browserSyncRestorePaused','browserSyncRestoreJournal',
  'browserSyncStorageRestoreJournal','browserSyncIndexedDBRestoreJournal']){
  const stored={browserSyncTransferStatus:{r:previous},[key]:{r:true}};
  assert.deepEqual(ruleTransferStatus('r',stored),{...previous,state:'PARTIAL'});
  assert.equal(ruleTransferStatus('other',stored),null);
 }
});
