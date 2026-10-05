package client

import (
	"context"
	"net"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type pathSelectionTestSession struct {
	transport tunnel.TransportType
}

func (s *pathSelectionTestSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	panic("not used")
}

func (s *pathSelectionTestSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	panic("not used")
}

func (s *pathSelectionTestSession) Transport() tunnel.TransportType { return s.transport }
func (s *pathSelectionTestSession) RemoteAddr() net.Addr             { return nil }
func (s *pathSelectionTestSession) LocalAddr() net.Addr              { return nil }
func (s *pathSelectionTestSession) Close() error                     { return nil }
func (s *pathSelectionTestSession) Done() <-chan struct{}            { return nil }

func TestConfigureDirectPathTagsLegacyP2P(t *testing.T) {
	relay := &pathSelectionTestSession{transport: tunnel.TransportQUIC}
	p2p := &pathSelectionTestSession{transport: tunnel.TransportQUIC}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) {
		return p2p, true
	}, nil)

	selected := dialer.sessionForExit("exit-1")
	if selected.Session != p2p {
		t.Fatalf("selected session = %p, want P2P %p", selected.Session, p2p)
	}
	if selected.Path != protocol.ProxyPathP2PQUIC {
		t.Fatalf("selected path = %q, want %q", selected.Path, protocol.ProxyPathP2PQUIC)
	}
}

func TestConfigureDirectProviderPreservesPublicPath(t *testing.T) {
	public := &pathSelectionTestSession{transport: tunnel.TransportQUIC}
	dialer := NewTunnelDialer(nil, nil)
	dialer.ConfigureDirectProvider(func(string) (SelectedSession, bool) {
		return SelectedSession{Session: public, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)

	selected := dialer.sessionForExit("exit-1")
	if selected.Session != public {
		t.Fatalf("selected session = %p, want public %p", selected.Session, public)
	}
	if selected.Path != protocol.ProxyPathPublicDirectQUIC {
		t.Fatalf("selected path = %q, want %q", selected.Path, protocol.ProxyPathPublicDirectQUIC)
	}
}

func TestP2POnlyRejectsPublicDirectSession(t *testing.T) {
	relay := &pathSelectionTestSession{transport: tunnel.TransportQUIC}
	public := &pathSelectionTestSession{transport: tunnel.TransportQUIC}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectProvider(func(string) (SelectedSession, bool) {
		return SelectedSession{Session: public, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)
	dialer.ConfigureDirectPolicy("p2p_only", true)

	selected := dialer.sessionForExit("exit-1")
	if selected.Session != nil || selected.Path != "" {
		t.Fatalf("selected = %#v, want no usable session", selected)
	}
}

func TestDirectOnlyDoesNotFallBackToRelay(t *testing.T) {
	relay := &pathSelectionTestSession{transport: tunnel.TransportTLS}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPolicy("direct_only", true)

	selected := dialer.sessionForExit("exit-1")
	if selected.Session != nil || selected.Path != "" {
		t.Fatalf("selected = %#v, want no relay fallback", selected)
	}
	if dialer.directFallbackEnabled() {
		t.Fatal("direct fallback unexpectedly enabled in direct_only mode")
	}
}

func TestRelaySelectionCarriesTransportPath(t *testing.T) {
	relay := &pathSelectionTestSession{transport: tunnel.TransportTLS}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)

	selected := dialer.sessionForExit("exit-1")
	if selected.Session != relay {
		t.Fatalf("selected session = %p, want relay %p", selected.Session, relay)
	}
	if selected.Path != protocol.ProxyPathRelayTLS {
		t.Fatalf("selected path = %q, want %q", selected.Path, protocol.ProxyPathRelayTLS)
	}
}
