package divert

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestHTTPSAndSVCBQueriesDegradeToNODATA(t *testing.T) {
	d := newFakeIPDNS()
	for _, typ := range []dnsmessage.Type{dnsmessage.Type(64), dnsmessage.Type(65)} {
		answer := fakeDNSAnswer(t, d.reply(fakeDNSQuestion(t, "play.google.com", typ), "", nil))
		if answer.RCode != dnsmessage.RCodeSuccess || len(answer.Answers) != 0 {
			t.Fatalf("RFC 9460 fallback should be NOERROR/NODATA: %+v", answer)
		}
		if len(d.byIP) != 0 {
			t.Fatal("SVCB query unexpectedly allocated a fake IP or returned real-IP hints")
		}
	}
}

func TestTXTAndSRVOnlyForwardViaSelectedProxyWithOptIn(t *testing.T) {
	var tcpCalls int
	var seenExit, seenIP string
	var seenPort uint16
	enabled := true
	s := newTestServer(t, Options{
		Config:          Config{DefaultAction: ActionProxy},
		FakeIPEnabled:   func() bool { return true },
		ForwardOtherDNS: func() bool { return enabled },
		ProxyReady:      func() bool { return true },
		DefaultExitID:   func() string { return "chosen-exit" },
		Dialer: &testDialer{tcp: func(ctx context.Context, exit, host string, port uint16) (net.Conn, error) {
			tcpCalls++
			seenExit, seenIP, seenPort = exit, host, port
			return nil, errors.New("proxy unavailable")
		}},
	})
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeTXT, dnsmessage.TypeSRV} {
		answer := fakeDNSAnswer(t, s.replyFakeDNS(context.Background(), fakeDNSQuestion(t, "example.com", kind)))
		if answer.RCode != dnsmessage.RCodeServerFailure || len(answer.Answers) != 0 {
			t.Fatalf("TXT/SRV proxy failure silently fell back: %+v", answer)
		}
	}
	if tcpCalls != 2 || seenExit != "chosen-exit" || seenIP != "9.9.9.9" || seenPort != 853 {
		t.Fatalf("proxy DNS selected incorrect exit or destination: calls=%d exit=%s dest=%s:%d", tcpCalls, seenExit, seenIP, seenPort)
	}
	enabled = false
	response := fakeDNSAnswer(t, s.replyFakeDNS(context.Background(), fakeDNSQuestion(t, "example.com", dnsmessage.TypeTXT)))
	if response.RCode != dnsmessage.RCodeRefused || tcpCalls != 2 {
		t.Fatal("disabled TXT forwarding used an external resolver")
	}
}
