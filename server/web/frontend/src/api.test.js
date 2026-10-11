import test from 'node:test';
import assert from 'node:assert/strict';
import {api, validChannelId, validatePortRange, normalizedCapabilities} from './api.js';
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
import {pushURL} from './api.js';
test('message URLs use Relay TCP listener rather than Admin HTTP port',()=>{
  assert.equal(pushURL('channel.A',{tcpListen:':20800',tlsEnabled:true},'relay.example.com'),'https://relay.example.com:20800/api/v1/push/channel.A');
  assert.equal(pushURL('name 1',{tcpListen:':80',tlsEnabled:false},'127.0.0.1'),'http://127.0.0.1/api/v1/push/name%201');
});
import {fmtDate} from './api.js';
test('RDP audit timestamps never render missing/epoch as an event', () => {
  for(const invalid of [null,'',0,'1970-01-01T00:00:00Z','not a date'])
    assert.equal(fmtDate(invalid),'—');
  assert.match(fmtDate('2026-10-09T12:34:56Z'),/2026/);
});

test('HTTP 404 with an HTML/plain-text body preserves status for optional Browser Sync page', async()=>{
  const originalFetch=globalThis.fetch;
  let requested='';
  globalThis.fetch=async(url)=>{
    requested=url;
    return {ok:false,status:404,json:async()=>{throw new SyntaxError('Not JSON')}};
  };
  try{
    await assert.rejects(api('/browser-sync/admin/devices'),error=>
      error.status===404 && error.message==='服务端响应无效（HTTP 404）');
    assert.equal(requested,'/api/v1/browser-sync/admin/devices');
  }finally{globalThis.fetch=originalFetch;}
});
test('unexpected HTML 200 is not silently treated as an unavailable feature',async()=>{
  const originalFetch=globalThis.fetch;
  globalThis.fetch=async()=>({ok:true,status:200,json:async()=>{throw new SyntaxError('Unexpected HTML')}});
  try{
    await assert.rejects(api('/browser-sync/admin/devices'),error=>
      error.status===200 && /服务端响应无效/.test(error.message));
  }finally{globalThis.fetch=originalFetch;}
});
