import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

test('DNS diagnostics action is exposed to Windows and browser bridges',()=>{
  const root=new URL('../../',import.meta.url);
  const frontend=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
  const windowBridge=readFileSync(new URL('../assets/wails-bridge.js',root),'utf8');
  assert.match(frontend,/call\('goProbeDNS'\)/);
  assert.match(frontend,/检测 DNS 上游/);
  assert.match(frontend,/DNS 出口：/);
  assert.match(windowBridge,/goProbeDNS = \(\) => invoke\('ProbeDNS'\)/);
});
