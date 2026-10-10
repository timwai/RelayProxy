import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

// Smoke test for the desktop DNS page's advanced controls. The first draft
// accidentally rendered an empty advanced fragment, hiding persisted settings.
test('simplified DNS page retains advanced settings and diagnostics',()=>{
  const source=readFileSync(new URL('./App.jsx',import.meta.url),'utf8');
  const begin=source.indexOf('function DNSPage(');
  const end=source.indexOf('function RDPPage(',begin);
  assert.ok(begin>=0 && end>begin,'DNSPage must be present');
  const dnsPage=source.slice(begin,end);
  for(const field of ['routing.dns_association_enabled','routing.dns_exit_id','routing.doh_blocked_ips','setDoHBlocking(r,v)','setOtherDNSForwarding(r,v)']){
    assert.ok(dnsPage.includes(field),'missing DNS advanced option '+field);
  }
  assert.ok(dnsPage.includes('disabled={!r.fake_ip_enabled}'),'FakeIP-dependent protection needs explicit mode guard');
  assert.ok(dnsPage.includes('onGoto(\'diagnostics\')'),'DNS diagnostics navigation must remain available');
});
