package androidcore

import (
	"context"
	"errors"
	"net"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
)

type fakeVPNGuardDialer struct {
	tcpCalls       int
	udpCalls       int
	lastUDPOptions proxy.UDPDialOptions
}

func (d *fakeVPNGuardDialer) DialTCP(context.Context, string, string, uint16) (net.Conn, error) {
	d.tcpCalls++
	return nil, errors.New("base tcp dial")
}

func (d *fakeVPNGuardDialer) DialUDP(context.Context, string, string, uint16) (net.PacketConn, error) {
	d.udpCalls++
	return nil, errors.New("base udp dial")
}

func (d *fakeVPNGuardDialer) DialUDPWithOptions(_ context.Context, _ string, _ string, _ uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	d.udpCalls++
	d.lastUDPOptions = options
	return nil, errors.New("base udp options dial")
}

func TestVPNMappedDNSGuardRejectsFakeIPBeforeTunnel(t *testing.T) {
	base := &fakeVPNGuardDialer{}
	guard := &vpnMappedDNSGuardDialer{base: base}

	for _, host := range []string{"198.18.0.2", "198.19.0.21", "198.19.1.248"} {
		_, err := guard.DialTCP(context.Background(), "exit", host, 443)
		var relayErr *protocol.RelayError
		if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeHostUnreach {
			t.Fatalf("DialTCP(%q) error = %v, want HOST_UNREACHABLE", host, err)
		}
		_, err = guard.DialUDPWithOptions(context.Background(), "exit", host, 53, proxy.UDPDialOptions{DatagramRequired: true})
		if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeHostUnreach {
			t.Fatalf("DialUDP(%q) error = %v, want HOST_UNREACHABLE", host, err)
		}
	}
	if base.tcpCalls != 0 || base.udpCalls != 0 {
		t.Fatalf("FakeIP reached base dialer: tcp=%d udp=%d", base.tcpCalls, base.udpCalls)
	}
}

func TestVPNMappedDNSGuardAllowsDomainsAndPublicIPs(t *testing.T) {
	base := &fakeVPNGuardDialer{}
	guard := &vpnMappedDNSGuardDialer{base: base}

	if _, err := guard.DialTCP(context.Background(), "exit", "example.com", 443); err == nil {
		t.Fatal("expected fake base dial error")
	}
	if _, err := guard.DialTCP(context.Background(), "exit", "42.81.252.103", 443); err == nil {
		t.Fatal("expected fake base dial error")
	}
	if base.tcpCalls != 2 {
		t.Fatalf("base tcp calls = %d, want 2", base.tcpCalls)
	}
}

func TestVPNMappedDNSGuardPrefersReliableStreamForOrdinaryUDP443(t *testing.T) {
	base := &fakeVPNGuardDialer{}
	guard := &vpnMappedDNSGuardDialer{base: base}

	_, _ = guard.DialUDP(context.Background(), "exit", "youtube.googleapis.com", 443)
	if base.udpCalls != 1 || !base.lastUDPOptions.PreferStream || base.lastUDPOptions.DatagramRequired {
		t.Fatalf("ordinary UDP/443 options = %+v calls=%d", base.lastUDPOptions, base.udpCalls)
	}

	base.udpCalls = 0
	base.lastUDPOptions = proxy.UDPDialOptions{}
	_, _ = guard.DialUDP(context.Background(), "exit", "dns.google", 53)
	if base.udpCalls != 1 || base.lastUDPOptions.PreferStream || base.lastUDPOptions.DatagramRequired {
		t.Fatalf("ordinary UDP/53 options = %+v calls=%d", base.lastUDPOptions, base.udpCalls)
	}
}

func TestVPNMappedDNSGuardKeepsRequiredDatagramsOnUDP443(t *testing.T) {
	base := &fakeVPNGuardDialer{}
	guard := &vpnMappedDNSGuardDialer{base: base}

	_, _ = guard.DialUDPWithOptions(
		context.Background(),
		"exit",
		"realtime.example",
		443,
		proxy.UDPDialOptions{DatagramRequired: true},
	)
	if base.udpCalls != 1 || !base.lastUDPOptions.DatagramRequired || base.lastUDPOptions.PreferStream {
		t.Fatalf("required UDP/443 options = %+v calls=%d", base.lastUDPOptions, base.udpCalls)
	}
}
