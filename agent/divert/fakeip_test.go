package divert

import (
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func fakeDNSQuestion(t *testing.T, host string, typ dnsmessage.Type) []byte {
	t.Helper()
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 733, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(host + "."), Type: typ, Class: dnsmessage.ClassINET}},
	}
	data, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fakeDNSAnswer(t *testing.T, data []byte) dnsmessage.Message {
	t.Helper()
	var msg dnsmessage.Message
	if err := msg.Unpack(data); err != nil {
		t.Fatal(err)
	}
	if msg.ID != 733 || !msg.Response {
		t.Fatalf("DNS response header invalid: %+v", msg.Header)
	}
	return msg
}

func TestFakeIPDNSAnswersAndLookupBothFamilies(t *testing.T) {
	d := newFakeIPDNS()
	for _, typ := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		query := fakeDNSQuestion(t, "Play.Google.Com", typ)
		m := fakeDNSAnswer(t, d.reply(query, "", nil))
		if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 1 || m.Answers[0].Header.TTL != fakeIPTTL {
			t.Fatalf("invalid fake DNS answer: %+v", m)
		}
		var ip netip.Addr
		switch v := m.Answers[0].Body.(type) {
		case *dnsmessage.AResource:
			ip = netip.AddrFrom4(v.A)
		case *dnsmessage.AAAAResource:
			ip = netip.AddrFrom16(v.AAAA)
		default:
			t.Fatalf("invalid resource: %T", v)
		}
		if !isFakeIP(ip) {
			t.Fatalf("fake DNS returned a real/routable IP: %s", ip)
		}
		if got, ok := d.lookup(ip); !ok || got != "play.google.com" {
			t.Fatalf("fake IP mapping lost: %s %s %v", ip, got, ok)
		}
		repeat := fakeDNSAnswer(t, d.reply(query, "", nil))
		if repeat.Answers[0].Header.Type != typ || len(d.byIP) != (1+map[bool]int{true: 1}[typ == dnsmessage.TypeAAAA]) {
			t.Fatal("repeated DNS question consumed another fake IP")
		}
	}
	if _, ok := d.lookup(netip.MustParseAddr("198.18.17.88")); ok {
		t.Fatal("unknown FakeIP was accepted")
	}
}

func TestFakeIPDNSAlwaysFailsClosedAndProtectsRelayBootstrap(t *testing.T) {
	d := newFakeIPDNS()
	q := fakeDNSQuestion(t, "relay.example.com", dnsmessage.TypeA)
	m := fakeDNSAnswer(t, d.reply(q, "relay.example.com", []string{"203.0.113.19"}))
	if m.RCode != dnsmessage.RCodeSuccess || len(d.byIP) != 0 {
		t.Fatal("relay bootstrap assigned FakeIP instead of known relay IP")
	}
	ip := m.Answers[0].Body.(*dnsmessage.AResource)
	if netip.AddrFrom4(ip.A).String() != "203.0.113.19" {
		t.Fatalf("bootstrap returned wrong address: %v", ip)
	}
	m = fakeDNSAnswer(t, d.reply(q, "relay.example.com", nil))
	if m.RCode != dnsmessage.RCodeServerFailure {
		t.Fatal("bootstrap without a known relay IP did not fail closed")
	}
	m = fakeDNSAnswer(t, d.reply(fakeDNSQuestion(t, "example.com", dnsmessage.TypeTXT), "", nil))
	if m.RCode != dnsmessage.RCodeRefused {
		t.Fatal("unsupported DNS records were sent upstream")
	}
	if d.reply([]byte{0, 1, 0}, "", nil) != nil {
		t.Fatal("malformed DNS query received a response")
	}
	d.mu.Lock()
	d.next4 = 131070
	d.mu.Unlock()
	m = fakeDNSAnswer(t, d.reply(fakeDNSQuestion(t, "last.example", dnsmessage.TypeA), "", nil))
	if m.RCode != dnsmessage.RCodeServerFailure {
		t.Fatal("exhausted fake IP pool assigned duplicate or forwarded DNS")
	}
}

func TestFakeIPDNSMappingExpiryFailsClosed(t *testing.T) {
	d := newFakeIPDNS()
	now := time.Unix(5000, 0)
	d.now = func() time.Time { return now }
	ip, ok := d.allocate("app.example", dnsmessage.TypeA)
	if !ok {
		t.Fatal("failed to allocate")
	}
	now = now.Add(16 * time.Minute)
	if _, ok := d.lookup(ip); ok {
		t.Fatal("expired FakeIP mapping can still route")
	}
	if !isFakeIP(ip) {
		t.Fatal("placeholder fell outside reserved range")
	}
}

func TestFakeIPReservedLocalhostAndInvalidNames(t *testing.T) {
	d := newFakeIPDNS()
	for _, tc := range []struct {
		hostname string
		kind     dnsmessage.Type
		wantIP   string
	}{
		{"localhost", dnsmessage.TypeA, "127.0.0.1"},
		{"API.Localhost", dnsmessage.TypeAAAA, "::1"},
	} {
		response := fakeDNSAnswer(t, d.reply(fakeDNSQuestion(t, tc.hostname, tc.kind), "", nil))
		if response.RCode != dnsmessage.RCodeSuccess || len(response.Answers) != 1 {
			t.Fatalf("localhost failed: %+v", response)
		}
		var actual netip.Addr
		switch body := response.Answers[0].Body.(type) {
		case *dnsmessage.AResource:
			actual = netip.AddrFrom4(body.A)
		case *dnsmessage.AAAAResource:
			actual = netip.AddrFrom16(body.AAAA)
		default:
			t.Fatalf("unexpected body %T", body)
		}
		if actual.String() != tc.wantIP || isFakeIP(actual) {
			t.Fatalf("reserved local name returned %s, want %s", actual, tc.wantIP)
		}
	}
	if len(d.byIP) != 0 {
		t.Fatal("localhost consumed or registered FakeIP addresses")
	}
	for _, hostname := range []string{"invalid", "no-host.invalid"} {
		response := fakeDNSAnswer(t, d.reply(fakeDNSQuestion(t, hostname, dnsmessage.TypeA), "", nil))
		if response.RCode != dnsmessage.RCodeNameError || len(response.Answers) != 0 {
			t.Fatalf("invalid domain was proxied or fabricated: %+v", response)
		}
	}
}
