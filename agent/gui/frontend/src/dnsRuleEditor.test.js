import test from 'node:test';
import assert from 'node:assert/strict';
import {buildDNSRule,normalizeDNSRuleDraft,validateDNSRule,validateDNSSettings,validateRoutingDNS} from './dnsRuleEditor.js';

test('DNS rule editor normalizes and serializes rules without losing action or exit',()=>{
 const draft=normalizeDNSRuleDraft({name:'Google',enabled:true,action:'REMOTE',resolver:'exit_system',exit_id:'device-1',domains:['*.google.com','googleapis.com']});
 assert.equal(draft.domains,'*.google.com\ngoogleapis.com');
 assert.deepEqual(buildDNSRule(draft),{name:'Google',enabled:true,action:'REMOTE',resolver:'exit_system',exit_id:'device-1',domains:['*.google.com','googleapis.com']});
});

test('DNS rule validation prevents the empty-domain save failure',()=>{
 assert.match(validateDNSRule({action:'REMOTE',resolver:'encrypted',domains:[]}),/至少填写/);
 assert.equal(validateDNSRule({action:'REMOTE',resolver:'encrypted',domains:'*.google.com\nexample.org'}),'');
 assert.match(validateDNSRule({action:'REMOTE',resolver:'encrypted',domains:'*.bad_domain'}),/格式无效/);
 assert.match(validateDNSRule({action:'REMOTE',resolver:'encrypted',domains:'example.com,example.com'}),/重复/);
});

test('local DNS rules discard resolver-specific exit fields',()=>{
 assert.equal(validateDNSRule({action:'LOCAL',resolver:'exit_system',exit_id:'local:proxy',domains:'example.com'}),'');
});

test('DNS settings validation catches invalid upstreams and mode combinations',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:true,fake_ip_enabled:false,dns_rules:[],dns_upstreams:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.equal(validateDNSSettings(base),'');
 assert.match(validateDNSSettings({...base,dns_upstreams:[{url:'https://dns.example/dns-query',bootstrap_ip:''}]}),/Bootstrap IP/);
 assert.match(validateDNSSettings({...base,dns_rules:[{action:'REMOTE',domains:[]}]}),/第 1 条/);
 assert.match(validateDNSSettings({...base,fake_ip_enabled:true}),/不能同时启用/);
 assert.match(validateDNSSettings({...base,proxy_dns_enabled:false,fake_ip_enabled:true,dns_mode:'local'}),/FakeIP/);
});

test('DoH upstreams require HTTPS hostname path and literal bootstrap IP',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:true,fake_ip_enabled:false,dns_rules:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.equal(validateDNSSettings({...base,dns_upstreams:[{url:'https://dns.example/dns-query',bootstrap_ip:'1.1.1.1'}]}),'');
 assert.match(validateDNSSettings({...base,dns_upstreams:[{url:'http://dns.example/dns-query',bootstrap_ip:'1.1.1.1'}]}),/HTTPS/);
});

test('IP validation blocks loopback bootstrap and private or overly broad DoH blocks',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:true,fake_ip_enabled:false,dns_rules:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.match(validateDNSSettings({...base,dns_upstreams:[{url:'https://dns.example/dns-query',bootstrap_ip:'127.0.0.1'}]}),/Bootstrap IP/);
 assert.match(validateDNSSettings({...base,doh_blocked_ips:['192.168.1.1']}),/格式无效/);
 assert.match(validateDNSSettings({...base,doh_blocked_ips:['8.8.8.0/16']}),/格式无效/);
 assert.equal(validateDNSSettings({...base,doh_blocked_ips:['8.8.8.8','1.1.1.0/24']}),'');
});

test('DNS rule system resolver follows the default exit and rejects a local custom proxy',()=>{
 assert.equal(validateDNSRule({action:'REMOTE',resolver:'exit_system',domains:'example.com'},''),'');
 assert.match(validateDNSRule({action:'REMOTE',resolver:'exit_system',domains:'example.com'},'local:custom'),/不支持本机自定义代理/);
 assert.equal(validateDNSRule({action:'REMOTE',resolver:'exit_system',exit_id:'device-1',domains:'example.com'},'local:custom'),'');
});

test('DoH URL validation matches backend path and port rules',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:true,fake_ip_enabled:false,dns_rules:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.equal(validateDNSSettings({...base,dns_upstreams:[{url:'https://dns.example:443/',bootstrap_ip:'1.1.1.1'}]}),'');
 assert.match(validateDNSSettings({...base,dns_upstreams:[{url:'https://dns.example',bootstrap_ip:'1.1.1.1'}]}),/HTTPS/);
 assert.match(validateDNSSettings({...base,dns_exit_id:'bad exit'}),/出口 ID/);
 assert.equal(validateDNSSettings({...base,dns_exit_id:'local:proxy'}),'');
});

test('DNS mode validation matches backend automatic-detection constraints',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:false,fake_ip_enabled:false,auto_detect_dns:false,dns_rules:[],dns_upstreams:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.match(validateDNSSettings({...base,dns_mode:'unknown'}),/DNS 模式无效/);
 assert.match(validateDNSSettings({...base,auto_detect_dns:true,fake_ip_enabled:true}),/自动检测与 FakeIP/);
 assert.equal(validateDNSSettings({...base,auto_detect_dns:true,dns_mode:'local'}),'');
});

test('DoH blocklist validation reads the same multiline draft as the textarea',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:false,fake_ip_enabled:true,dns_rules:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.match(validateDNSSettings({...base,doh_blocked_ips:'192.168.1.1\n'}),/格式无效/);
 assert.equal(validateDNSSettings({...base,doh_blocked_ips:'1.1.1.1\n8.8.8.0/24\n'}),'');
});

test('system DNS rule fallback is independent from encrypted DNS exit',()=>{
 const base={dns_mode:'proxy',proxy_dns_enabled:true,fake_ip_enabled:false,dns_rules:[{action:'REMOTE',resolver:'exit_system',domains:'example.com'}],dns_upstreams:[],dns_direct_domains:[],doh_blocked_ips:[]};
 assert.equal(validateDNSSettings({...base,dns_exit_id:'local:custom'},'device-1'),'');
 assert.match(validateDNSSettings({...base,dns_exit_id:'device-1'},'local:custom'),/不支持本机自定义代理/);
});


test('static DNS and per-rule DNS validate literal IPs and device resolvers',()=>{
 assert.equal(validateDNSSettings({dns_hosts:[{domain:'app.example',ip:'192.168.1.20'},{domain:'v6.example',ip:'fd00::20'}]}),'');
 assert.match(validateDNSSettings({dns_hosts:[{domain:'app.example',ip:'example.com'}]}),/IP 无效/);
 assert.match(validateDNSSettings({dns_hosts:[{domain:'app.example',ip:'192.168.1.20'},{domain:'APP.EXAMPLE.',ip:'192.168.1.21'}]}),/重复/);
 assert.equal(validateRoutingDNS({action:'PROXY',dns_resolver:'static',dns_ip:'2001:db8::1'}),'');
 assert.match(validateRoutingDNS({action:'PROXY',dns_resolver:'static',dns_ip:'fe80::1%en0'}),/固定 IP/);
 assert.match(validateRoutingDNS({action:'PROXY',exit_id:'local:proxy',dns_resolver:'exit_system'}),/设备出口/);
 assert.match(validateRoutingDNS({action:'REJECT',dns_resolver:'encrypted'}),/REJECT/);
});
