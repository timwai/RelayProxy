import test from 'node:test';
import assert from 'node:assert/strict';
import {routingPagePatch,dnsPagePatch} from './configPatches.js';

test('routing rules save cannot overwrite DNS policies',()=>{
 const r={mode:'rule',default_action:'PROXY',rules:[{name:'proxy'}],
  dns_mode:'proxy',auto_detect_dns:false,fake_ip_enabled:true,
  block_doh_endpoints:true,forward_other_dns:true,
  dns_exit_id:'exit1',doh_blocked_ips:['1.1.1.1']};
 assert.deepEqual(routingPagePatch(r),{mode:'rule',default_action:'PROXY',rules:[{name:'proxy'}]});
 assert.deepEqual(Object.keys(dnsPagePatch(r)).sort(),
  ['dns_mode','auto_detect_dns','fake_ip_enabled','block_doh_endpoints','forward_other_dns','dns_exit_id','doh_blocked_ips'].sort());
 assert.equal(dnsPagePatch(r).fake_ip_enabled,true);
});
