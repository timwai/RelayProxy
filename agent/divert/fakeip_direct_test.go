package divert

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPDirectUsesVerifiedProxyDNSWithoutLocalFallback(t *testing.T) {
	dials := 0
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionDirect},
		FakeIPEnabled: func() bool { return true },
		DefaultExitID: func() string { return "remote-exit" },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			dials++
			if exit != "remote-exit" || host != proxyDNSResolverIP || port != 853 {
				t.Errorf("DNS unexpectedly bypassed selected proxy exit: %s %s:%d", exit, host, port)
			}
			return nil, errors.New("forced upstream DNS failure")
		}},
	})
	ip, ok := s.fakeDNS.allocate("app.example", dnsmessage.TypeA)
	if !ok {
		t.Fatal("FakeIP allocation failed")
	}
	flow := testFlow(ProtoTCP, nil)
	flow.IP = ip.String()
	route, err := s.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision().Action != ActionDirect || !route.Decision().HandleDirect {
		t.Fatalf("DIRECT FakeIP was not captured for real-IP resolution: %+v", route.Decision())
	}
	client, server := net.Pipe()
	defer client.Close()
	if err := s.ForwardTCP(context.Background(), route, server); err == nil {
		t.Fatal("unavailable remote DNS silently fell back to direct FakeIP dial")
	}
	if dials != 1 {
		t.Fatalf("expected one DoT attempt through selected proxy, got %d", dials)
	}
}
