package divert

import (
 "net/netip"
 "testing"

 "golang.org/x/net/dns/dnsmessage"
)

func TestInterceptedTCPRealDNSAssociation(t *testing.T) {
 target:=netip.MustParseAddr("142.250.1.10")
 key:=FlowKey{Protocol:ProtoTCP,Source:netip.MustParseAddrPort("192.0.2.10:51234"),Destination:netip.MustParseAddrPort("192.0.2.53:53")}
 query:=fakeDNSQuestion(t,"www.google.com",dnsmessage.TypeA)
 answer:=authenticatedTestDNSResponse(t,query,target)
 for _,tt:=range []struct{name string; assoc, proxy bool; want string}{
  {"enabled",true,true,"www.google.com"},
  {"disabled association",false,true,""},
  {"not intercepted",true,false,""},
 } {
  t.Run(tt.name,func(t *testing.T){
   i,_:=newTestInterceptor(t,Options{
    Config:Config{DefaultAction:ActionProxy},
    ProxyDNSEnabled:func()bool{return tt.proxy},
    DNSAssociationEnabled:func()bool{return tt.assoc},
   })
   i.observeInterceptedDNSTCP(key,query,answer)
   if got:=i.dns.lookup(target);got!=tt.want {t.Fatalf("DNS TCP hostname = %q, want %q",got,tt.want)}
  })
 }
}
