package divert

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"relayproxy/internal/traffic"
)

func makeTLSClientHelloForTest(host string) []byte {
	name := []byte(host)
	names := make([]byte, 5+len(name))
	binary.BigEndian.PutUint16(names[:2], uint16(3+len(name)))
	names[2] = 0
	binary.BigEndian.PutUint16(names[3:5], uint16(len(name)))
	copy(names[5:], name)
	ext := make([]byte, 4+len(names))
	binary.BigEndian.PutUint16(ext[2:4], uint16(len(names)))
	copy(ext[4:], names)
	hello := append([]byte{3, 3}, make([]byte, 32)...)
	hello = append(hello, 0) // session id
	hello = append(hello, 0, 2, 0x13, 0x01) // cipher suites
	hello = append(hello, 1, 0) // compression methods
	hello = append(hello, byte(len(ext)>>8), byte(len(ext)))
	hello = append(hello, ext...)
	handshake := append([]byte{1, byte(len(hello) >> 16), byte(len(hello) >> 8), byte(len(hello))}, hello...)
	record := []byte{0x16, 3, 1, byte(len(handshake) >> 8), byte(len(handshake))}
	return append(record, handshake...)
}

func TestApplicationHostnameExtraction(t *testing.T) {
	cases := []struct {
		name, source, expected string
		payload []byte
	}{
		{"tls", "tls-sni", "play.google.com", makeTLSClientHelloForTest("Play.Google.Com")},
		{"http", "http-host", "play.google.com", []byte("GET /x HTTP/1.1\r\nHost: Play.Google.Com:80\r\nConnection: close\r\n\r\n")},
		{"non-http", "", "", []byte("SSH-2.0-OpenSSH_9.0\r\n")},
		{"invalid TLS", "", "", []byte{0x16, 3, 3, 0, 1, 2}},
		{"fake HTTP host", "", "", []byte("GET / HTTP/1.1\r\nHost: 203.0.113.10\r\n\r\n")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, source, incomplete := parseApplicationHostname(tt.payload)
			if got != tt.expected || source != tt.source || incomplete {
				t.Fatalf("parse host=%q source=%q needMore=%v", got, source, incomplete)
			}
		})
	}
}

func TestTCPHostnameTelemetryAndNoRuleReclassification(t *testing.T) {
	stats := traffic.NewRegistry(0, 0)
	i, device := newTestInterceptor(t, Options{Traffic: stats, Config: Config{DefaultAction: ActionProxy}})
	syn := interceptedSYN(false)
	p, _ := parseIPPacket(syn)
	if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	key := FlowKey{Protocol: ProtoTCP, Source: p.Source, Destination: p.Destination}
	i.mu.Lock()
	redirect := i.tcp[key]
	i.mu.Unlock()
	if redirect == nil {
		t.Fatal("missing TCP redirect")
	}
	hello := makeTLSClientHelloForTest("Play.Google.Com")
	first := hello[:17]
	second := hello[17:]
	probe := func(data []byte, seq uint32) {
		i.mu.Lock()
		i.observeTCPHostnameLocked(redirect, ipPacket{Protocol: ProtoTCP, Payload: data, TCPSequence: seq})
		i.mu.Unlock()
	}
	probe(first, 100)
	if got := stats.Snapshot().Connections[0].Host; got != "" {
		t.Fatalf("incomplete ClientHello disclosed hostname: %q", got)
	}
	probe(second, 100+uint32(len(first)))
	snap := stats.Snapshot()
	if len(snap.Connections) != 1 || snap.Connections[0].Host != "play.google.com" || snap.Connections[0].DomainSource != "tls-sni" {
		t.Fatalf("TCP hostname telemetry missing: %+v", snap.Connections)
	}
	if redirect.route.flow.Host != "" || redirect.route.Decision().Action != ActionProxy {
		t.Fatal("untrusted SNI changed an already selected route")
	}
	probe(makeTLSClientHelloForTest("spoof.example"), 200)
	if snap := stats.Snapshot(); snap.Connections[0].Host != "play.google.com" {
		t.Fatal("late SNI replaced initial telemetry")
	}
	_ = netip.IPv4Unspecified()
}

func TestTCPHostnameCannotOverrideObservedDNS(t *testing.T) {
	stats := traffic.NewRegistry(0, 0)
	record := stats.Start(traffic.Metadata{Host: "verified.example", DomainSource: "dns", IP: "198.51.100.3"})
	record.SetObservedDomain("spoof.example", "tls-sni")
	record.SetObservedDomain("spoof.example", "http-host")
	snapshot := stats.Snapshot()
	if snapshot.Connections[0].Host != "verified.example" || snapshot.Connections[0].DomainSource != "dns" {
		t.Fatal("SNI or HTTP Host replaced stronger DNS attribution")
	}
}
