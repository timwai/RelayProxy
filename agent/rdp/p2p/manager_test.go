package p2p

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestStartTargetUDPCloseRaceDoesNotPublishSockets(t *testing.T) {
	m := NewManager(context.Background(), func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}, "127.0.0.1:9", time.Minute, "")
	defer m.Close()

	for i := uint64(1); i <= 32; i++ {
		item := m.newSession(i, protocol.P2PPurposeRDP, "controller", "target", []byte("0123456789abcdef"), nil, 0)
		if item == nil {
			t.Fatal("session was not created")
		}
		started := make(chan error, 1)
		go func() { started <- m.startTargetUDP(item) }()
		item.closeLocal()
		err := <-started
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("target UDP startup failed unexpectedly: %v", err)
		}
		item.mu.Lock()
		udp, local := item.udp, item.localUDP
		item.mu.Unlock()
		if udp != nil || local != nil {
			t.Fatalf("closed session retained UDP sockets: udp=%v local=%v", udp, local)
		}
	}
}

func TestStartControllerForDesktopMediaCarriesPurpose(t *testing.T) {
	var got protocol.RDPControlMessage
	m := NewManager(context.Background(), func(_ context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		got = message
		return protocol.RDPControlMessage{
			Type:         protocol.RDPControlConnectResponse,
			Purpose:      protocol.P2PPurposeDesktopMedia,
			SessionID:    99,
			ControllerID: "controller",
			TargetID:     "target",
			SessionToken: []byte("0123456789abcdef"),
		}, nil
	}, "127.0.0.1:9", time.Minute, "")
	defer m.Close()

	session, err := m.StartControllerForPurpose(context.Background(), "target", protocol.P2PPurposeDesktopMedia)
	if err != nil {
		t.Fatal(err)
	}
	defer session.closeLocal()
	if got.Type != protocol.RDPControlConnectRequest || got.Purpose != protocol.P2PPurposeDesktopMedia || got.TargetID != "target" {
		t.Fatalf("control=%+v", got)
	}
	if session.Purpose != protocol.P2PPurposeDesktopMedia {
		t.Fatalf("purpose=%q", session.Purpose)
	}
}

func TestApplicationHandlerAttachesReadyDesktopPath(t *testing.T) {
	m := NewManager(context.Background(), func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}, "127.0.0.1:9", time.Minute, "")
	defer m.Close()

	session := m.newSession(
		7,
		protocol.P2PPurposeDesktopMedia,
		"controller",
		"target",
		[]byte("0123456789abcdef"),
		nil,
		0,
	)
	if session == nil {
		t.Fatal("session was not created")
	}
	path := newApplicationPath(session)
	session.mu.Lock()
	session.applicationPath = path
	session.remotePort = netip.MustParseAddrPort("127.0.0.1:45678")
	session.mu.Unlock()

	ready := make(chan *ApplicationPath, 1)
	m.SetApplicationHandler(protocol.P2PPurposeDesktopMedia, func(gotSession *Session, gotPath *ApplicationPath) {
		if gotSession != session {
			t.Errorf("session=%p want %p", gotSession, session)
		}
		ready <- gotPath
	})

	select {
	case got := <-ready:
		if got != path {
			t.Fatalf("path=%p want %p", got, path)
		}
	case <-time.After(time.Second):
		t.Fatal("application handler was not notified")
	}
}

func TestDesktopMediaSessionCannotDialRDPDirectTCP(t *testing.T) {
	m := NewManager(context.Background(), func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}, "127.0.0.1:9", time.Minute, "")
	defer m.Close()

	session := m.newSession(
		8,
		protocol.P2PPurposeDesktopMedia,
		"controller",
		"target",
		[]byte("0123456789abcdef"),
		nil,
		0,
	)
	if session == nil {
		t.Fatal("session was not created")
	}
	if _, err := session.DialTCP(context.Background()); err == nil {
		t.Fatal("desktop media lease unexpectedly opened an RDP TCP path")
	}
}

func TestDirectPathMetricsSmoothRTTAndJitter(t *testing.T) {
	session := &Session{}
	session.observeDirectPathRTT(20 * time.Millisecond)
	first := session.DirectPathMetrics()
	if first.Samples != 1 || first.RTTMs < 19.9 || first.RTTMs > 20.1 || first.JitterMs != 0 {
		t.Fatalf("first metrics=%+v", first)
	}

	session.observeDirectPathRTT(30 * time.Millisecond)
	second := session.DirectPathMetrics()
	if second.Samples != 2 || second.RTTMs < 21.9 || second.RTTMs > 22.1 {
		t.Fatalf("second metrics=%+v", second)
	}
	if second.JitterMs < 9.9 || second.JitterMs > 10.1 {
		t.Fatalf("second jitter=%v", second.JitterMs)
	}

	session.observeDirectPathRTT(22 * time.Millisecond)
	third := session.DirectPathMetrics()
	if third.Samples != 3 || third.RTTMs < 21.9 || third.RTTMs > 22.1 {
		t.Fatalf("third metrics=%+v", third)
	}
	if third.JitterMs < 9.5 || third.JitterMs > 9.7 {
		t.Fatalf("third jitter=%v", third.JitterMs)
	}
}
