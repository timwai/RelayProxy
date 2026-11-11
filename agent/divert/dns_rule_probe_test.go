package divert

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestStaticDNSAnswersBothFamiliesBeforeFakeIP(t *testing.T) {
	for _, address := range []string{"192.168.1.20", "fd00::20"} {
		ip := netip.MustParseAddr(address)
		s := newTestServer(t, Options{FakeIPEnabled: func() bool { return true }, DNSHostIP: func(host string) (string, bool) { return address, host == "app.example" }})
		for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
			q := fakeDNSQuestion(t, "app.example", kind)
			r := fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), q, true))
			want := 0
			if (kind == dnsmessage.TypeA) == ip.Is4() {
				want = 1
			}
			if r.RCode != dnsmessage.RCodeSuccess || len(r.Answers) != want {
				t.Fatalf("%s %s: %+v", address, kind, r)
			}
			values := dnsAnswerValues(r.Questions[0].Name, kind, r.Answers)
			if want == 1 && (len(values) != 1 || values[0] != address) {
				t.Fatalf("mapping became fake IP: %v", values)
			}
		}
	}
}

func TestDNSRuleProbeReportsRemoteAnswersAndSelectedExit(t *testing.T) {
	exits := []string{}
	s := newTestServer(t, Options{
		DNSExitID: func() string { return "dedicated-encrypted-exit" }, DefaultExitID: func() string { return "traffic-default" },
		ExchangeExitSystemDNS: func(_ context.Context, exit string, q []byte) ([]byte, error) {
			exits = append(exits, exit)
			var msg dnsmessage.Message
			_ = msg.Unpack(q)
			ip := netip.MustParseAddr("203.0.113.44")
			if msg.Questions[0].Type == dnsmessage.TypeAAAA {
				ip = netip.MustParseAddr("2001:db8::44")
			}
			return authenticatedTestDNSResponse(t, q, ip), nil
		},
	})
	r, err := s.ProbeDNSRule(context.Background(), "app.example", "chosen", "exit_system")
	if err != nil || r.ExitID != "chosen" || len(r.Results) != 2 {
		t.Fatalf("report=%+v error=%v", r, err)
	}
	for i, want := range []string{"203.0.113.44", "2001:db8::44"} {
		if !r.Results[i].OK || len(r.Results[i].Answers) != 1 || r.Results[i].Answers[0] != want || exits[i] != "chosen" {
			t.Fatalf("result=%+v exits=%v", r.Results[i], exits)
		}
	}
	r, err = s.ProbeDNSRule(context.Background(), "app.example", "", "exit_system")
	if err != nil || r.ExitID != "traffic-default" {
		t.Fatalf("system DNS used encrypted DNS exit: %+v %v", r, err)
	}
}

func TestDNSRuleProbeFailureAndRefusalRemainVisible(t *testing.T) {
	s := newTestServer(t, Options{ExchangeExitSystemDNS: func(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("exit unavailable") }})
	r, err := s.ProbeDNSPolicy(context.Background(), "app.example", "exit", "exit_system", "")
	if err != nil || len(r.Results) != 2 {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	for _, r := range r.Results {
		if r.OK || r.Error == "" || len(r.Answers) > 0 {
			t.Fatalf("failure hidden: %+v", r)
		}
	}
	r, err = s.ProbeDNSPolicy(context.Background(), "app.example", "", "reject", "")
	if err != nil || r.Results[0].RCode != dnsmessage.RCodeRefused.String() || r.Results[0].OK {
		t.Fatalf("refusal hidden: %+v %v", r, err)
	}
}

func TestTransparentStaticDNSDialsIPWithDomainRule(t *testing.T) {
	for _, transport := range []Protocol{ProtoTCP, ProtoUDP} {
		called := false
		check := func(exit, host string) {
			called = true
			if exit != "chosen" || host != "203.0.113.60" {
				t.Errorf("dialed %s via %s", host, exit)
			}
		}
		sentinel := errors.New("recorded target")
		s := newTestServer(t, Options{SharedPolicy: func(f Flow) Decision {
			if f.Host != "app.example" {
				t.Errorf("lost original domain: %+v", f)
			}
			return Decision{Action: ActionProxy, ExitID: "chosen", DNSResolver: "static", DNSIP: "203.0.113.60"}
		}, Dialer: &testDialer{
			tcp: func(_ context.Context, exit, host string, _ uint16) (net.Conn, error) {
				check(exit, host)
				return nil, sentinel
			},
			udp: func(_ context.Context, exit, host string, _ uint16) (net.PacketConn, error) {
				check(exit, host)
				return nil, sentinel
			},
		}})
		f := testFlow(transport, nil)
		f.Host = "app.example"
		f.DomainSource = "dns"
		route, err := s.ClassifyFlow(f)
		if err != nil {
			t.Fatal(err)
		}
		if transport == ProtoTCP {
			client, downstream := tcpPair(t)
			defer client.Close()
			err = s.ForwardTCP(context.Background(), route, downstream)
		} else {
			err = s.ForwardUDP(context.Background(), route, []byte("hello"), func(context.Context, FlowKey, []byte) error { return nil })
		}
		if !called || !errors.Is(err, sentinel) {
			t.Fatalf("protocol=%s called=%v err=%v", transport, called, err)
		}
	}
}

func TestGatewayDNSUsesRuleResolverExitAndRefusal(t *testing.T) {
	called := 0
	s := newTestServer(t, Options{
		DNSRoute:    func(string) (bool, string) { return false, "rule-exit" },
		DNSResolver: func(string) string { return "exit_system" },
		DNSBlocked:  func(host string) bool { return host == "blocked.example" },
		ExchangeExitSystemDNS: func(_ context.Context, exit string, q []byte) ([]byte, error) {
			called++
			if exit != "rule-exit" {
				t.Errorf("gateway DNS exit=%s", exit)
			}
			return authenticatedTestDNSResponse(t, q, netip.MustParseAddr("203.0.113.21")), nil
		},
	})
	q := fakeDNSQuestion(t, "app.example", dnsmessage.TypeA)
	r := fakeDNSAnswer(t, s.ExchangeGatewayDNS(context.Background(), "gateway-default", q, true))
	if called != 1 || len(r.Answers) != 1 {
		t.Fatalf("gateway ignored domain DNS policy: %+v", r)
	}
	q = fakeDNSQuestion(t, "blocked.example", dnsmessage.TypeA)
	r = fakeDNSAnswer(t, s.ExchangeGatewayDNS(context.Background(), "gateway-default", q, true))
	if called != 1 || r.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("gateway ignored DNS rejection: %+v", r)
	}
}
