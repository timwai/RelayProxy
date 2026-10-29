import test from 'node:test';
import assert from 'node:assert/strict';
import {routingPagePatch,dnsPagePatch,changedGatewayPatch} from './configPatches.js';

test('unmodified gateway is omitted from transparent proxy saves',()=>{
 const saved={enabled:false,interface:'',source_cidrs:null};
 assert.equal(changedGatewayPatch(saved,saved),undefined);
 assert.equal(changedGatewayPatch({enabled:false,source_cidrs:[]},{}),undefined);
 const payload=JSON.parse(JSON.stringify({network:{mode:'divert'},gateway:changedGatewayPatch(saved,saved)}));
 assert.deepEqual(payload,{network:{mode:'divert'}});
});

test('gateway changes are saved and reverting a draft omits the gateway',()=>{
 const saved={enabled:true,interface:'eth0',source_cidrs:['192.168.1.0/24'],port:15080};
 assert.equal(changedGatewayPatch({...saved,source_cidrs:'192.168.1.0/24\n'},saved),undefined);
 assert.equal(changedGatewayPatch({...saved,enabled:false},saved).enabled,false);
 assert.equal(changedGatewayPatch({...saved,interface:'eth1'},saved).interface,'eth1');
 assert.equal(changedGatewayPatch({...saved},saved),undefined);
});

test('routing rules save cannot overwrite DNS policies',()=>{
 const r={mode:'rule',default_action:'PROXY',rules:[{name:'proxy'}],subscriptions:[{name:'gfw',url:'https://example.com/list',enabled:true,action:'PROXY'}],
  dns_mode:'proxy',auto_detect_dns:false,fake_ip_enabled:true,
  block_doh_endpoints:true,forward_other_dns:true,
  dns_exit_id:'exit1',doh_blocked_ips:['1.1.1.1']};
 assert.deepEqual(routingPagePatch(r),{mode:'rule',default_action:'PROXY',rules:[{name:'proxy'}],subscriptions:[{name:'gfw',url:'https://example.com/list',enabled:true,action:'PROXY'}]});
 assert.deepEqual(Object.keys(dnsPagePatch(r)).sort(),
  ['dns_mode','auto_detect_dns','dns_association_enabled','proxy_dns_enabled','fake_ip_enabled','block_doh_endpoints','forward_other_dns','dns_exit_id','dns_upstreams','dns_direct_domains','doh_blocked_ips'].sort());
 assert.equal(dnsPagePatch(r).fake_ip_enabled,true);
});

test('DNS save preserves local domain exceptions and supports clearing them',()=>{
 assert.deepEqual(dnsPagePatch({dns_direct_domains:['corp.example']}).dns_direct_domains,['corp.example']);
 assert.deepEqual(dnsPagePatch({dns_direct_domains:[]}).dns_direct_domains,[]);
 assert.deepEqual(dnsPagePatch({dns_direct_domains:'corp.example\n\nbranch.example\n'}).dns_direct_domains,['corp.example','branch.example']);
});

test('DNS page save includes independently persisted association and real DNS switches',()=>{
 const payload=dnsPagePatch({dns_mode:'proxy',dns_association_enabled:false,proxy_dns_enabled:true,fake_ip_enabled:false});
 assert.equal(payload.dns_association_enabled,false);
 assert.equal(payload.proxy_dns_enabled,true);
 assert.equal(dnsPagePatch({dns_mode:'proxy'}).dns_association_enabled,true);
 assert.equal(dnsPagePatch({dns_mode:'proxy'}).proxy_dns_enabled,false);
});
