import test from 'node:test';
import assert from 'node:assert/strict';
import { exitInventoryKey } from './exitInventory.js';

test('exit inventory key is stable for the same summary', () => {
  const status = {
    proxyExitRevision: 7,
    proxyExits: [{deviceId:'exit-a',name:'Tokyo',online:true,authorizationSource:'identity'}],
  };
  assert.equal(exitInventoryKey(status), exitInventoryKey(structuredClone(status)));
});

test('exit inventory key changes for inventory or online-state changes', () => {
  const base = {proxyExitRevision:7,proxyExits:[{deviceId:'exit-a',name:'Tokyo',online:true}]};
  assert.notEqual(exitInventoryKey(base), exitInventoryKey({...base,proxyExitRevision:8}));
  assert.notEqual(exitInventoryKey(base), exitInventoryKey({...base,proxyExits:[{...base.proxyExits[0],online:false}]}));
  assert.notEqual(exitInventoryKey(base), exitInventoryKey({...base,proxyExits:[...base.proxyExits,{deviceId:'exit-b',name:'LA',online:true}]}));
});

test('full-only direct path metadata does not affect the summary trigger', () => {
  const summary = {proxyExitRevision:3,proxyExits:[{deviceId:'exit-a',name:'Tokyo',online:true}]};
  const withDetail = {
    ...summary,
    proxyExits:[{...summary.proxyExits[0],direct:{public:{available:true,endpoints:[{dialAddress:'203.0.113.1:443'}]}}}],
  };
  assert.equal(exitInventoryKey(summary), exitInventoryKey(withDetail));
});
