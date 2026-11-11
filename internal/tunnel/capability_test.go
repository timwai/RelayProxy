package tunnel

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestPeerStreamResumeCapabilityIsSessionScoped(t *testing.T) {
	session := &QUICSession{}
	if PeerSupportsStreamResume(session) {
		t.Fatal("stream resume unexpectedly enabled by default")
	}

	SetPeerCapabilities(session, []string{protocol.CapabilityProxyStreamResume})
	if !PeerSupportsStreamResume(session) {
		t.Fatal("stream resume capability was not recorded")
	}

	SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	if PeerSupportsStreamResume(session) {
		t.Fatal("stream resume capability leaked after peer capability refresh")
	}
}

type streamResumeTestSession struct {
	TunnelSession
	enabled bool
}

func (s *streamResumeTestSession) PeerSupportsStreamResume() bool {
	return s.enabled
}

func TestPeerStreamResumeSupportsTransportDoubles(t *testing.T) {
	session := &streamResumeTestSession{enabled: true}
	if !PeerSupportsStreamResume(session) {
		t.Fatal("interface-based stream resume capability was not detected")
	}
	session.enabled = false
	if PeerSupportsStreamResume(session) {
		t.Fatal("disabled interface-based stream resume capability was reported")
	}
}

func TestExitSystemDNSCapabilityIsNegotiatedForTLSAndQUIC(t *testing.T) {
	for name, session := range map[string]TunnelSession{"TLS": &TLSSession{}, "QUIC": &QUICSession{}} {
		t.Run(name, func(t *testing.T) {
			if PeerSupportsExitSystemDNS(session) {
				t.Fatal("old peer unexpectedly supports system DNS")
			}
			SetPeerCapabilities(session, []string{protocol.CapabilityExitSystemDNS})
			if !PeerSupportsExitSystemDNS(session) {
				t.Fatal("negotiated system DNS capability lost")
			}
			SetPeerCapabilities(session, []string{protocol.CapabilityTargetACL})
			if PeerSupportsExitSystemDNS(session) {
				t.Fatal("system DNS capability survived refresh without support")
			}
		})
	}
}
