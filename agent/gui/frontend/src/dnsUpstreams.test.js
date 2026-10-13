import test from 'node:test';
import assert from 'node:assert/strict';
import {formatDNSUpstreamLines,parseDNSUpstreamLines} from './dnsUpstreams.js';
import {dnsPagePatch} from './configPatches.js';
import {applyDNSPreset} from './dnsPresets.js';

test('custom encrypted resolver lines round trip through DNS save',()=>{
  const list=[
    {url:'https://dns.exit.example/dns-query',bootstrap_ip:'10.0.0.53'},
    {url:'https://backup.example/dns-query',bootstrap_ip:'192.0.2.53'},
  ];
  assert.deepEqual(parseDNSUpstreamLines(formatDNSUpstreamLines(list)),list);
  const patch=dnsPagePatch({dns_upstreams:list});
  assert.deepEqual(patch.dns_upstreams,list);
});
test('auto DNS preset does not erase pinned custom resolver list',()=>{
  const dns_upstreams=[{url:'https://dns.exit.example/dns-query',bootstrap_ip:'10.0.0.53'}];
  const next=applyDNSPreset({dns_upstreams,fake_ip_enabled:true},'auto');
  assert.deepEqual(next.dns_upstreams,dns_upstreams);
  assert.equal(next.proxy_dns_enabled,true);
});
test('incomplete custom resolvers are passed to backend validation, not discarded',()=>{
  assert.deepEqual(parseDNSUpstreamLines('https://dns.exit.example/dns-query'),[
    {url:'https://dns.exit.example/dns-query',bootstrap_ip:''},
  ]);
});
