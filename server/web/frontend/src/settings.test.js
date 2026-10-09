import test from 'node:test';import assert from 'node:assert/strict';
import {groups,configFieldCount,normalizeForSave,configEqual} from './settings.js';
test('server config covers all existing groups and fields',()=>{
  assert.equal(groups.length,8);
  assert.equal(configFieldCount(),41);
  assert.deepEqual(groups.map(g=>g.key||g.id),['admin','tunnel','direct','p2p','serverExit','rdpIngress','certificate','relayACL']);
});
test('unknown future settings survive edits',()=>{
  assert.deepEqual(normalizeForSave({p2p:{portStart:20900}},{p2p:{portStart:0,futureField:42},newGroup:{enabled:true}}),{p2p:{portStart:20900,futureField:42},newGroup:{enabled:true}});
});

test('multiline CIDR and domain fields preserve newlines until save, then serialize to arrays',()=>{
 const initial={p2p:{portStart:20900,portEnd:20999},relayACL:{domains:['example.org'],cidrs:['10.0.0.0/8']}};
 const draft={relayACL:{domains:'example.org\n*.internal.test\n\n',cidrs:'10.0.0.0/8\n 192.168.0.0/16 '},__pending:true};
 const saved=normalizeForSave(draft,initial);
 assert.deepEqual(saved.relayACL.domains,['example.org','*.internal.test']);
 assert.deepEqual(saved.relayACL.cidrs,['10.0.0.0/8','192.168.0.0/16']);
 assert.equal(saved.__pending,undefined);
 assert.deepEqual(initial.relayACL.domains,['example.org']);
});

test('config comparisons ignore editor-only flags and normalize equivalent line inputs',()=>{
 const initial={relayACL:{domains:['a.example','b.example'],cidrs:[]}};
 const draft={__pending:true,relayACL:{domains:'a.example\nb.example\n',cidrs:''}};
 assert.equal(configEqual(draft,initial),true);
 assert.equal(configEqual({...draft,relayACL:{domains:'a.example\nc.example',cidrs:[]}},initial),false);
});