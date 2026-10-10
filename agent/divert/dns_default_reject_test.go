package divert

import (
 "net/netip"
 "testing"
)

func TestLocalDNSDefaultRejectPassthrough(t *testing.T) {
 dnsPacket := ipPacket{Protocol: ProtoUDP, Destination: netip.MustParseAddrPort("8.8.8.8:53")}
 cases := []struct{
  name string
  packet ipPacket
  decision Decision
  fake, proxy, association bool
  want bool
 }{
  {"observational DNS default reject",dnsPacket,Decision{Action:ActionReject,Rule:"default"},false,false,true,true},
  {"explicit DNS reject",dnsPacket,Decision{Action:ActionReject,Rule:"block-dns"},false,false,true,false},
  {"fakeIP intercept",dnsPacket,Decision{Action:ActionReject,Rule:"default"},true,false,true,false},
  {"authenticated DNS intercept",dnsPacket,Decision{Action:ActionReject,Rule:"default"},false,true,true,false},
  {"association disabled",dnsPacket,Decision{Action:ActionReject,Rule:"default"},false,false,false,false},
  {"non-DNS packet",ipPacket{Protocol:ProtoUDP,Destination:netip.MustParseAddrPort("8.8.8.8:443")},Decision{Action:ActionReject,Rule:"default"},false,false,true,false},
  {"TCP DNS not bypassed",ipPacket{Protocol:ProtoTCP,Destination:netip.MustParseAddrPort("8.8.8.8:53")},Decision{Action:ActionReject,Rule:"default"},false,false,true,false},
 }
 for _,tt:=range cases {
  t.Run(tt.name,func(t *testing.T){
   got:=localDNSDefaultRejectPassthrough(tt.packet,tt.decision,tt.fake,tt.proxy,tt.association)
   if got!=tt.want {t.Fatalf("got %v, want %v",got,tt.want)}
  })
 }
}
