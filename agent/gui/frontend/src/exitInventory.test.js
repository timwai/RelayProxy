import test from 'node:test';
import assert from 'node:assert/strict';
import { exitInventoryKey, exitUsable, selectedExitUnavailable } from './exitInventory.js';

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

test('exitUsable only accepts known exits that are not offline', () => {
  const exits = [{deviceId:'exit-a',online:true},{deviceId:'exit-b',online:false},{id:'exit-c'}];
  assert.equal(exitUsable(exits, 'exit-a'), true);
  assert.equal(exitUsable(exits, 'exit-c'), true, 'missing online flag counts as usable');
  assert.equal(exitUsable(exits, 'exit-b'), false, 'offline exit is not usable');
  assert.equal(exitUsable(exits, 'exit-zzz'), false, 'unknown exit is not usable');
  assert.equal(exitUsable(exits, ''), false, 'auto selection is not an explicit target');
  assert.equal(exitUsable(undefined, 'exit-a'), false);
});

test('selected exit is unavailable when it left the authoritative inventory', () => {
  const exits = [{deviceId:'exit-a',online:true}];
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:true,exits,selected:'exit-gone'}), true);
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:false,exits,selected:'exit-gone'}), true, 'revoked exit stays unavailable while offline');
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:true,exits,selected:'exit-a'}), false);
});

test('cached offline flag only marks the selected exit unavailable during a live session', () => {
  const exits = [{deviceId:'exit-a',online:false}];
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:true,exits,selected:'exit-a'}), true);
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:false,exits,selected:'exit-a'}), false, 'stale online state must not raise the banner while disconnected');
});

test('selected exit banner waits for inventory and ignores auto selection', () => {
  assert.equal(selectedExitUnavailable({exitsReady:false,connected:true,exits:[],selected:'exit-a'}), false);
  assert.equal(selectedExitUnavailable({exitsReady:true,connected:true,exits:[],selected:''}), false);
});
