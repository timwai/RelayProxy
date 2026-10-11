import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizeBrowserNetworkDraft,validateBrowserNetworkDraft,applyBrowserNetworkDraft} from './browserNetworkConfig.js';

test('browser network listener, TLS address and exit allowlist are validated together',()=>{
  const valid={listen:':21292',advertise:'relay.example.com:21292',allowed:['dev_approved']};
  assert.equal(validateBrowserNetworkDraft(valid),'');
  assert.equal(validateBrowserNetworkDraft({...valid,listen:'0.0.0.0:21292'}),'');
  assert.equal(validateBrowserNetworkDraft({...valid,listen:'[::]:21292'}),'');
  assert.equal(validateBrowserNetworkDraft({listen:'relay',advertise:'relay.example.com:443',allowed:['dev_approved']},'443'),'');
  assert.notEqual(validateBrowserNetworkDraft({listen:'relay',advertise:'relay.example.com:21292',allowed:['dev_approved']},'443'),'');
  assert.notEqual(validateBrowserNetworkDraft({listen:'relay',advertise:'relay.example.com:443',allowed:['dev_approved']},''),'');
  for(const patch of [
    {advertise:''},{listen:''},{advertise:'http://relay.example.com:21292'},
    {advertise:'relay.example.com:0'},{listen:'0.0.0.0:65536'},
    {listen:'relay.example.com:0'},{allowed:[]},
    {allowed:['server']},{allowed:Array.from({length:33},(_,i)=>'dev_agent_'+i)}
  ]) assert.notEqual(validateBrowserNetworkDraft({...valid,...patch}),'');
  assert.equal(validateBrowserNetworkDraft({...valid,listen:'',advertise:'',allowed:[]}), '');
  assert.notEqual(validateBrowserNetworkDraft({...valid,listen:'',advertise:''}),'');
});

test('browser network edits preserve unrelated Server config and dedupe allowlist',()=>{
  const original={
    admin:{listen:':21080',tlsEnabled:true},
    browserSync:{enabled:true,extensionIds:['bbbb'],networkProxyListen:':21090',
      networkProxyAdvertise:'old.example.com:21090',allowedExitIds:['dev_old']},
    tunnel:{tcpListen:':443'}
  };
  const patched=applyBrowserNetworkDraft(original,{
    listen:' :21292 ',advertise:' relay.example.com:21292 ',
    allowed:[' dev_new ','dev_new','']
  });
  assert.deepEqual(patched.admin,original.admin);
  assert.deepEqual(patched.tunnel,original.tunnel);
  assert.equal(patched.browserSync.enabled,true);
  assert.deepEqual(patched.browserSync.extensionIds,['bbbb']);
  assert.equal(patched.browserSync.networkProxyListen,':21292');
  assert.deepEqual(patched.browserSync.allowedExitIds,['dev_new']);
  assert.deepEqual(original.browserSync.allowedExitIds,['dev_old']);
  assert.deepEqual(normalizeBrowserNetworkDraft({listen:'',advertise:'',allowed:['dev_old','dev_old']}).allowed,['dev_old']);
});
