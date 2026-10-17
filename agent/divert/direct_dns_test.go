package divert

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDirectDNSQuestionRequiresExplicitSuffixBoundary(t *testing.T) {
	domains := []string{"Corp.Example."}
	s := newTestServer(t, Options{
		DNSDirectDomains: func() []string { return domains },
		Guard:            LoopGuard{RelayHost: "relay.corp.example"},
	})
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"corp.example", true}, {"host.corp.example", true}, {"_ldap._tcp.corp.example", true},
		{"evilcorp.example", false}, {"corp.example.attacker.test", false},
		{"www.example.com", false}, {"relay.corp.example", false},
	} {
		if got := s.directDNSQuestion(fakeDNSQuestion(t, tc.name, dnsmessage.TypeA)); got != tc.want {
			t.Fatalf("%s direct = %t, want %t", tc.name, got, tc.want)
		}
	}
	if s.directDNSQuestion([]byte("invalid DNS")) {
		t.Fatal("malformed DNS bypassed interception")
	}
	domains = nil
	if s.directDNSQuestion(fakeDNSQuestion(t, "host.corp.example", dnsmessage.TypeA)) {
		t.Fatal("removed exception remained active")
	}
}

func TestDirectDNSUDPUsesOriginalResolverOnlyForConfiguredDomains(t *testing.T) {
	for _, fake := range []bool{false, true} {
		i, device := newTestInterceptor(t, Options{
			FakeIPEnabled:    func() bool { return fake },
			ProxyDNSEnabled:  func() bool { return !fake },
			ProxyReady:       func() bool { return false },
			DNSDirectDomains: func() []string { return []string{"corp.example"} },
		})
		for _, domain := range []string{"printer.corp.example", "corp.example.attacker.test"} {
			query := fakeDNSQuestion(t, domain, dnsmessage.TypeA)
			packet, _ := packetTestFixture(false, ProtoUDP, query, false)
			p, _ := parseIPPacket(packet)
			resolver := netip.MustParseAddrPort("192.168.1.1:53")
			if err := rewriteIPPacket(packet, p.Source, resolver); err != nil {
				t.Fatal(err)
			}
			if err := i.handlePacket(packet, packetMetadata{outbound: true}); err != nil {
				t.Fatal(err)
			}
			out := expectInterceptedPacket(t, device)
			got, _ := parseIPPacket(out.data)
			if domain == "printer.corp.example" {
				if !out.meta.outbound || got.Destination != resolver {
					t.Fatalf("local DNS exception did not preserve resolver: %+v", got)
				}
				answer := authenticatedTestDNSResponse(t, query, netip.MustParseAddr("10.0.0.42"))
				reply, _ := makeUDPReply(FlowKey{Protocol: ProtoUDP, Source: p.Source, Destination: resolver}, answer)
				if err := i.handlePacket(reply, packetMetadata{}); err != nil {
					t.Fatal(err)
				}
				_ = expectInterceptedPacket(t, device)
				if host := i.dns.lookup(netip.MustParseAddr("10.0.0.42")); host != domain {
					t.Fatalf("local DNS answer lost hostname association: %q", host)
				}
			} else if out.meta.outbound {
				t.Fatal("unconfigured domain leaked to a private resolver")
			}
		}
	}
}

func TestDNSTCPKeepAliveSeparatesLocalExceptionFromFakeIP(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	resolver := listener.Addr().(*net.TCPAddr).AddrPort()
	resolverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			resolverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var prefix [2]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			resolverDone <- err
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(prefix[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			resolverDone <- err
			return
		}
		answer := authenticatedTestDNSResponse(t, query, netip.MustParseAddr("10.0.0.42"))
		binary.BigEndian.PutUint16(prefix[:], uint16(len(answer)))
		_, err = conn.Write(append(prefix[:], answer...))
		resolverDone <- err
	}()
	s := newTestServer(t, Options{
		FakeIPEnabled:    func() bool { return true },
		DNSDirectDomains: func() []string { return []string{"corp.example"} },
	})
	server, client := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() { done <- s.serveDNSTCPAtResolver(context.Background(), server, resolver, nil) }()
	for _, domain := range []string{"public.example", "printer.corp.example", "other.example"} {
		query := fakeDNSQuestion(t, domain, dnsmessage.TypeA)
		wire := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(wire, uint16(len(query)))
		copy(wire[2:], query)
		if _, err := client.Write(wire); err != nil {
			t.Fatal(err)
		}
		var prefix [2]byte
		if _, err := io.ReadFull(client, prefix[:]); err != nil {
			t.Fatal(err)
		}
		answer := make([]byte, binary.BigEndian.Uint16(prefix[:]))
		if _, err := io.ReadFull(client, answer); err != nil {
			t.Fatal(err)
		}
		message := fakeDNSAnswer(t, answer)
		if len(message.Answers) != 1 {
			t.Fatalf("%s: unexpected DNS reply: %+v", domain, message)
		}
		ip := netip.AddrFrom4(message.Answers[0].Body.(*dnsmessage.AResource).A)
		if domain == "printer.corp.example" {
			if ip != netip.MustParseAddr("10.0.0.42") {
				t.Fatalf("local resolver answer changed: %s", ip)
			}
		} else if !isFakeIP(ip) {
			t.Fatalf("%s escaped FakeIP: %s", domain, ip)
		}
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-resolverDone; err != nil {
		t.Fatal(err)
	}
}

func TestLocalDNSAgentTCPRetainsBypassAfterSYN(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		FakeIPEnabled:    func() bool { return true },
		DNSDirectDomains: func() []string { return []string{"corp.example"} },
	})
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{pid: uint32(os.Getpid()), path: "relayproxy"}, nil
	}
	packet := interceptedSYN(false)
	p, _ := parseIPPacket(packet)
	resolver := netip.MustParseAddrPort("192.168.1.1:53")
	_ = rewriteIPPacket(packet, p.Source, resolver)
	if err := i.handlePacket(packet, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	_ = expectInterceptedPacket(t, device)
	packet[p.TransportOffset+13] = 0x10 // ACK after the SYN must also be direct.
	if err := i.handlePacket(packet, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	got := expectInterceptedPacket(t, device)
	if !got.meta.outbound {
		t.Fatal("Agent DNS connection entered its own resolver")
	}
}
