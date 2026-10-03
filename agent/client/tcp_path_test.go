package client

import (
	"context"
	"net"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type transportSession struct {
	*scriptedSession
	transport tunnel.TransportType
}

func (s *transportSession) Transport() tunnel.TransportType { return s.transport }

func requireTCPPath(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	source, ok := conn.(interface{ ProxyPath() string })
	if !ok {
		t.Fatalf("connection %T does not expose its path", conn)
	}
	if got := source.ProxyPath(); got != want {
		t.Fatalf("connection path = %q, want %q", got, want)
	}
	if _, ok := conn.(interface{ CloseWrite() error }); !ok {
		t.Fatalf("connection %T lost TCP half-close", conn)
	}
}

func TestTCPPathFollowsChosenSessionNotP2PReadiness(t *testing.T) {
	for _, transport := range []tunnel.TransportType{tunnel.TransportTLS, tunnel.TransportQUIC} {
		t.Run(string(transport), func(t *testing.T) {
			factory := func() tunnel.TunnelStream {
				return responseStream(protocol.OpenTCPResponse{Success: true})
			}
			relay := &transportSession{newScriptedSession(factory), transport}
			direct := &transportSession{newScriptedSession(factory), tunnel.TransportQUIC}
			ready := false
			dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
			dialer.ConfigureDirectPolicy("auto", true)
			dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, ready }, nil)
			dial := func(exit string) net.Conn {
				t.Helper()
				conn, err := dialer.DialTCP(context.Background(), exit, "192.0.2.10", 443)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			wantRelay := protocol.P2PPathRelayTLS
			if transport == tunnel.TransportQUIC {
				wantRelay = protocol.P2PPathRelayQUIC
			}
			cold := dial("exit")
			requireTCPPath(t, cold, wantRelay)
			ready = true
			requireTCPPath(t, dial("exit"), protocol.P2PPathDirectQUIC)
			requireTCPPath(t, cold, wantRelay) // Existing streams keep their session.
			requireTCPPath(t, dial(protocol.ServerExitDeviceID), wantRelay)
			dialer.ConfigureDirectPolicy("relay_only", true)
			requireTCPPath(t, dial("exit"), wantRelay)
			if relay.opens.Load() != 3 || direct.opens.Load() != 1 {
				t.Fatalf("unexpected sessions: relay=%d direct=%d", relay.opens.Load(), direct.opens.Load())
			}
		})
	}
}
