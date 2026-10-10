import test from 'node:test';
import assert from 'node:assert/strict';
import {enableFakeIP,setDoHBlocking,setOtherDNSForwarding,setAutoDNS,setProxyDNS} from './dnsSettings.js';

test('enabling FakeIP automatically enables proxy DNS and ends auto mode',()=>{
 const next=enableFakeIP({dns_mode:'local',auto_detect_dns:true},true);
 assert.equal(next.dns_mode,'proxy');
 assert.equal(next.auto_detect_dns,false);
 assert.equal(next.fake_ip_enabled,true);
});
test('turning on DoH or TXT/SRV automatically enables FakeIP prerequisites',()=>{
 for(const update of [setDoHBlocking,setOtherDNSForwarding]){
  const x=update({dns_mode:'local',auto_detect_dns:true,fake_ip_enabled:false},true);
  assert.equal(x.dns_mode,'proxy');
  assert.equal(x.auto_detect_dns,false);
  assert.equal(x.fake_ip_enabled,true);
 }
});
test('turning off FakeIP also disables its dependent DNS protections',()=>{
 const x=enableFakeIP({fake_ip_enabled:true,block_doh_endpoints:true,forward_other_dns:true},false);
 assert.equal(x.fake_ip_enabled,false);
 assert.equal(x.block_doh_endpoints,false);
 assert.equal(x.forward_other_dns,false);
});
test('auto detect or explicit local mode never leaves inconsistent FakeIP settings',()=>{
 const input={dns_mode:'proxy',auto_detect_dns:false,fake_ip_enabled:true,block_doh_endpoints:true,forward_other_dns:true};
 for(const out of [setAutoDNS(input,true),setProxyDNS(input,false)]){
  assert.equal(out.fake_ip_enabled,false);
  assert.equal(out.block_doh_endpoints,false);
  assert.equal(out.forward_other_dns,false);
 }
 assert.equal(setProxyDNS(input,false).dns_mode,'local');
 assert.equal(setProxyDNS(input,true).dns_mode,'proxy');
 assert.equal(setAutoDNS(input,true).auto_detect_dns,true);
 assert.equal(setAutoDNS(input,true).dns_mode,'local', 'auto toggle must select effective local-first mode');
 assert.equal(setAutoDNS({dns_mode:'proxy',auto_detect_dns:false},true).dns_mode,'local');
});


test('automatically detecting DNS can be switched back to explicit proxy resolution',()=>{
 const autodetected=setAutoDNS({dns_mode:'proxy',auto_detect_dns:false},true);
 assert.equal(autodetected.dns_mode,'local');
 assert.equal(autodetected.auto_detect_dns,true);
 const manual=setProxyDNS(autodetected,true);
 assert.equal(manual.auto_detect_dns,false);
 assert.equal(manual.dns_mode,'proxy');
});
