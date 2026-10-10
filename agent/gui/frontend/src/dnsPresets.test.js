import test from 'node:test';
import assert from 'node:assert/strict';
import {currentDNSPreset,applyDNSPreset} from './dnsPresets.js';

test('legacy DNS settings map to a stable preset',()=>{
 assert.equal(currentDNSPreset({fake_ip_enabled:true,proxy_dns_enabled:false}),'fake_ip');
 assert.equal(currentDNSPreset({proxy_dns_enabled:true}),'auto');
 assert.equal(currentDNSPreset({dns_mode:'proxy'}),'real_ip');
});
test('automatic DNS chooses proxy real-IP interception without FakeIP',()=>{
 const result=applyDNSPreset({fake_ip_enabled:true,block_doh_endpoints:true,forward_other_dns:true,dns_exit_id:'node-1'},'auto');
 assert.equal(result.dns_mode,'proxy'); assert.equal(result.proxy_dns_enabled,true);
 assert.equal(result.fake_ip_enabled,false);assert.equal(result.block_doh_endpoints,false);
 assert.equal(result.dns_exit_id,'node-1');
});
test('real IP keeps association and does not force synthetic DNS',()=>{
 const result=applyDNSPreset({proxy_dns_enabled:true,dns_association_enabled:false},'real_ip');
 assert.equal(result.proxy_dns_enabled,false);assert.equal(result.dns_association_enabled,true);
 assert.equal(result.dns_mode,'proxy');
});
test('FakeIP is exclusive with proxy real IP and preserves advanced protection choices',()=>{
 const result=applyDNSPreset({proxy_dns_enabled:true,block_doh_endpoints:true,forward_other_dns:true},'fake_ip');
 assert.equal(result.proxy_dns_enabled,false);assert.equal(result.fake_ip_enabled,true);
 assert.equal(result.block_doh_endpoints,true);assert.equal(result.forward_other_dns,true);
});
test('unknown preset cannot silently change routing',()=>assert.throws(()=>applyDNSPreset({},'bogus')));
