import test from 'node:test';
import assert from 'node:assert/strict';
import {validChannelId, validatePortRange, normalizedCapabilities} from './api.js';
test('custom channel ids support only documented characters',()=>{
  assert.equal(validChannelId('notify.api-1'),true);
  assert.equal(validChannelId('中文'),false);
  assert.equal(validChannelId('a'.repeat(81)),false);
});
test('port ranges validate random mode and ordering',()=>{
  const good={direct:{portStart:0,portEnd:0},p2p:{portStart:20900,portEnd:20999},rdpIngress:{portStart:30000,portEnd:30100}};
  assert.doesNotThrow(()=>validatePortRange(good));
  assert.throws(()=>validatePortRange({...good,p2p:{portStart:20999,portEnd:20900}}));
  assert.throws(()=>validatePortRange({...good,p2p:{portStart:0,portEnd:20999}}));
});
test('public RDP capability always requires RDP host',()=>{
  assert.deepEqual(normalizedCapabilities(['rdp.public']),['rdp.public','rdp.host']);
});