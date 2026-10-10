package divert

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func authenticatedTestDNSResponse(t *testing.T, raw []byte, address netip.Addr) []byte {
	t.Helper()
	var query dnsmessage.Message
	if err := query.Unpack(raw); err != nil { t.Fatal(err) }
	q := query.Questions[0]
	rr := dnsmessage.Resource{Header:dnsmessage.ResourceHeader{Name:q.Name,Type:q.Type,Class:dnsmessage.ClassINET,TTL:120}}
	switch {
	case q.Type == dnsmessage.TypeA && address.Is4():
		rr.Body = &dnsmessage.AResource{A:address.As4()}
	case q.Type == dnsmessage.TypeAAAA && address.Is6():
		rr.Body = &dnsmessage.AAAAResource{AAAA:address.As16()}
	default:
		t.Fatal("unexpected DNS family")
	}
	response,err := (dnsmessage.Message{Header:dnsmessage.Header{ID:query.ID,Response:true,RecursionAvailable:true},Questions:query.Questions,Answers:[]dnsmessage.Resource{rr}}).Pack()
	if err != nil {t.Fatal(err)}
	return response
}

func TestRealProxyDNSReturnsRealIPsNotFakeIPs(t *testing.T) {
	for _, tc := range []struct{name string; family dnsmessage.Type; ip string}{
		{"ipv4",dnsmessage.TypeA,"142.250.1.10"},
		{"ipv6",dnsmessage.TypeAAAA,"2001:4860:4860::8888"},
	} {
		t.Run(tc.name,func(t *testing.T){
			query := fakeDNSQuestion(t,"www.google.com",tc.family)
			expected := netip.MustParseAddr(tc.ip)
			result := realProxyDNSReply(context.Background(),query,true,func(ctx context.Context, raw []byte)([]byte,error){
				return authenticatedTestDNSResponse(t,raw,expected),nil
			})
			answer := fakeDNSAnswer(t,result)
			if answer.RCode != dnsmessage.RCodeSuccess || len(answer.Answers)!=1 {t.Fatalf("invalid answer: %+v",answer)}
			var ip netip.Addr
			switch body:=answer.Answers[0].Body.(type) {
			case *dnsmessage.AResource: ip=netip.AddrFrom4(body.A)
			case *dnsmessage.AAAAResource: ip=netip.AddrFrom16(body.AAAA)
			}
			if ip!=expected || isFakeIP(ip) {t.Fatalf("real proxy DNS returned fake or wrong IP: %s",ip)}
		})
	}
}

func TestRealProxyDNSFailsClosedAndValidatesUpstreamQuestion(t *testing.T) {
	query:=fakeDNSQuestion(t,"www.google.com",dnsmessage.TypeA)
	for _,tc:=range []struct{name string; exchange func(context.Context,[]byte)([]byte,error)}{
		{"no_exit",func(context.Context,[]byte)([]byte,error){return nil,errDNSForwardUnavailable}},
		{"tampered_question",func(_ context.Context,_ []byte)([]byte,error){
			return authenticatedTestDNSResponse(t,fakeDNSQuestion(t,"attacker.invalid",dnsmessage.TypeA),netip.MustParseAddr("203.0.113.42")),nil
		}},
		{"empty",func(context.Context,[]byte)([]byte,error){return nil,nil}},
	} {
		t.Run(tc.name,func(t *testing.T){
			answer:=fakeDNSAnswer(t,realProxyDNSReply(context.Background(),query,true,tc.exchange))
			if answer.RCode != dnsmessage.RCodeServerFailure {t.Fatalf("expected fail closed SERVFAIL: %v",answer.RCode)}
		})
	}
	ctx,cancel:=context.WithCancel(context.Background());cancel()
	called:=false
	reply:=realProxyDNSReply(ctx,query,true,func(context.Context,[]byte)([]byte,error){called=true;return nil,errors.New("never called")})
	if called || fakeDNSAnswer(t,reply).RCode!=dnsmessage.RCodeServerFailure {t.Fatal("canceled DNS contacted upstream")}
}

func TestRealProxyDNSForwardsModernHTTPSAndTXTQueries(t *testing.T) {
	for _,kind:=range []dnsmessage.Type{dnsmessage.TypeTXT,dnsmessage.TypeSRV,dnsmessage.Type(65)} {
		q:=fakeDNSQuestion(t,"www.google.com",kind)
		called:=false
		res:=realProxyDNSReply(context.Background(),q,false,func(_ context.Context,raw []byte)([]byte,error){
			called=true
			var request dnsmessage.Message
			if err:=request.Unpack(raw);err!=nil {t.Fatal(err)}
			return (dnsmessage.Message{Header:dnsmessage.Header{ID:request.ID,Response:true,RecursionAvailable:true},Questions:request.Questions}).Pack()
		})
		if !called || fakeDNSAnswer(t,res).RCode != dnsmessage.RCodeSuccess {t.Fatalf("record type %v was not forwarded",kind)}
	}
}

func TestRealProxyDNSLargeUDPResponseRequestsTCPRetry(t *testing.T) {
	query:=fakeDNSQuestion(t,"www.google.com",dnsmessage.TypeTXT)
	response:=func(_ context.Context,raw []byte)([]byte,error){
		var q dnsmessage.Message
		if err:=q.Unpack(raw);err!=nil {return nil,err}
		var answers []dnsmessage.Resource
		for n:=0;n<18;n++ {
			answers=append(answers,dnsmessage.Resource{Header:dnsmessage.ResourceHeader{Name:q.Questions[0].Name,Type:dnsmessage.TypeTXT,Class:dnsmessage.ClassINET,TTL:30},Body:&dnsmessage.TXTResource{TXT:[]string{strings.Repeat("a",100)}}})
		}
		return (dnsmessage.Message{Header:dnsmessage.Header{ID:q.ID,Response:true},Questions:q.Questions,Answers:answers}).Pack()
	}
	udp:=fakeDNSAnswer(t,realProxyDNSReply(context.Background(),query,true,response))
	tcp:=fakeDNSAnswer(t,realProxyDNSReply(context.Background(),query,false,response))
	if !udp.Truncated || len(udp.Answers)!=0 {t.Fatalf("UDP did not advertise truncated reply: %+v",udp)}
	if tcp.Truncated || len(tcp.Answers)!=18 {t.Fatalf("TCP discarded large DNS response: %+v",tcp)}
}

func TestProxyDNSRelayBootstrapOnlyUsesConfiguredHost(t *testing.T) {
	if !isRelayDNSQuestion(fakeDNSQuestion(t,"RELAY.EXAMPLE",dnsmessage.TypeA),"relay.example") {t.Fatal("missed relay bootstrap name")}
	if isRelayDNSQuestion(fakeDNSQuestion(t,"www.google.com",dnsmessage.TypeA),"relay.example") {t.Fatal("non-relay DNS bypassed proxy")}
}
