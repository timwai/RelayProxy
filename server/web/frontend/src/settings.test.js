import test from 'node:test';import assert from 'node:assert/strict';
import {groups,configFieldCount,normalizeForSave} from './settings.js';
test('server config covers all existing groups and fields',()=>{
  assert.equal(groups.length,8);
  assert.equal(configFieldCount(),41);
  assert.deepEqual(groups.map(g=>g.key||g.id),['admin','tunnel','direct','p2p','serverExit','rdpIngress','certificate','relayACL']);
});
test('unknown future settings survive edits',()=>{
  assert.deepEqual(normalizeForSave({p2p:{portStart:20900}},{p2p:{portStart:0,futureField:42},newGroup:{enabled:true}}),{p2p:{portStart:20900,futureField:42},newGroup:{enabled:true},admin:{},tunnel:{},direct:{},serverExit:{},rdpIngress:{},certificate:{},relayACL:{}});
});