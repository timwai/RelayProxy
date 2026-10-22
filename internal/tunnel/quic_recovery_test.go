package tunnel

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

func waitQUICRecovery(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for QUIC recovery state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAutoRecoversQUICAfterTLSFallbackWithoutDroppingTLSOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	certificate := generateSelfSignedCert(t)
	// Reserve the real QUIC port, but initially leave UDP unanswered.
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptedTLS := make(chan *TLSSession, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		peer, err := ServerTLS(tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}}), nil)
		if err != nil {
			_ = conn.Close()
			return
		}
		acceptedTLS <- peer
	}()
	var connected, reconnecting atomic.Int32
	m := NewTunnelManager(ManagerConfig{
		ServerAddress: "127.0.0.1", Mode: ModeAuto,
		QUICPort:  udp.LocalAddr().(*net.UDPAddr).Port,
		TCPPort:   listener.Addr().(*net.TCPAddr).Port,
		TLSConfig: &tls.Config{InsecureSkipVerify: true}, ConnectTimeout: 500 * time.Millisecond,
		AutoFallbackDelay: 10 * time.Millisecond, AutoQUICRetryInterval: 20 * time.Millisecond,
	}, func(_, state State, _ TunnelSession) {
		switch state {
		case StateConnected:
			connected.Add(1)
		case StateReconnecting:
			reconnecting.Add(1)
		}
	})
	defer m.Close()
	initial, err := m.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Transport() != TransportTLS {
		t.Fatalf("initial transport=%s, want TLS fallback", initial.Transport())
	}
	var tlsPeer *TLSSession
	select {
	case tlsPeer = <-acceptedTLS:
		defer tlsPeer.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	local, remote := streamPair(t, initial, tlsPeer)
	defer local.Close()
	defer remote.Close()
	m.StartAutoReconnect()
	waitQUICRecovery(t, func() bool { return m.ConnectionMetrics().QUICRecoveryError != "" })
	if m.Session() != initial || m.State() != StateConnected || reconnecting.Load() != 0 {
		t.Fatal("failed QUIC probe disturbed the working TLS session")
	}
	// Verify an already-open stream still transfers after the failed probe.
	_ = local.SetDeadline(time.Now().Add(time.Second))
	_ = remote.SetDeadline(time.Now().Add(time.Second))
	go func() { _, _ = remote.Write([]byte("still TLS")) }()
	if _, err := io.ReadFull(local, make([]byte, len("still TLS"))); err != nil {
		t.Fatalf("failed probe interrupted TLS traffic: %v", err)
	}

	// Restore UDP on exactly the configured port, with no configuration change
	// or explicit disconnection of the healthy TLS session.
	quicListener, err := quic.Listen(udp, &tls.Config{
		Certificates: []tls.Certificate{certificate}, NextProtos: []string{"relayproxy-quic"},
	}, DefaultQUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer quicListener.Close()
	acceptedQUIC := make(chan *quic.Conn, 1)
	go func() { peer, _ := quicListener.Accept(ctx); acceptedQUIC <- peer }()
	waitQUICRecovery(t, func() bool {
		current := m.Session()
		return current != nil && current != initial && current.Transport() == TransportQUIC && connected.Load() == 2
	})
	select {
	case <-initial.Done():
	default:
		t.Fatal("replaced TLS transport was not closed")
	}
	var quicPeer *QUICSession
	select {
	case peer := <-acceptedQUIC:
		if peer == nil {
			t.Fatal("QUIC listener did not accept the recovery connection")
		}
		quicPeer = NewQUICSession(peer)
		defer quicPeer.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	recovered := m.Session()
	stream, peerStream := streamPair(t, recovered, quicPeer)
	defer stream.Close()
	defer peerStream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Second))
	go func() { _, _ = peerStream.Write([]byte("QUIC")) }()
	if _, err := io.ReadFull(stream, make([]byte, 4)); err != nil {
		t.Fatalf("recovered transport did not carry traffic: %v", err)
	}
	metrics := m.ConnectionMetrics()
	if reconnecting.Load() != 1 || metrics.QUICRecoveries != 1 || metrics.QUICRecoveryAttempts < 2 ||
		metrics.Transport != TransportQUIC || metrics.FallbackReason != "" || metrics.QUICRecoveryError != "" {
		t.Fatalf("incomplete recovery: reconnecting=%d metrics=%+v", reconnecting.Load(), metrics)
	}
	// Late cleanup from the old application session cannot undo the upgrade.
	m.MarkSessionLost(initial)
	if m.Session() != recovered {
		t.Fatal("old TLS cleanup cleared recovered QUIC")
	}
}

func TestQUICRecoveryCancelsOnCloseAndNetworkLoss(t *testing.T) {
	for _, event := range []string{"close", "offline"} {
		t.Run(event, func(t *testing.T) {
			udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			initial, _ := sessionPair(t, "tls")
			m := NewTunnelManager(ManagerConfig{
				ServerAddress: "127.0.0.1", QUICPort: udp.LocalAddr().(*net.UDPAddr).Port,
				Mode: ModeAuto, ConnectTimeout: 5 * time.Second, AutoQUICRetryInterval: 10 * time.Millisecond,
			}, nil)
			defer m.Close()
			m.setState(StateConnected, initial)
			m.StartAutoReconnect()
			// Receiving a probe proves that the dial really is blocked in UDP I/O.
			_ = udp.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, err := udp.ReadFrom(make([]byte, 1500)); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if event == "offline" {
				m.NetworkChanged(false)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("shutdown waited for the QUIC probe timeout")
			}
			if m.State() != StateClosed || m.Session() != nil || m.ConnectionMetrics().QUICRecoveries != 0 {
				t.Fatal("stale probe resurrected a session")
			}
		})
	}
}

func TestQUICRecoveryOnlyRunsForAutoTLS(t *testing.T) {
	for _, tc := range []struct {
		name, transport string
		mode            Mode
		plain           bool
	}{
		{"explicit TCP", "tls", ModeTCPOnly, false},
		{"plaintext auto", "tls", ModeAuto, true},
		{"connected QUIC", "quic", ModeAuto, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial, _ := sessionPair(t, tc.transport)
			m := NewTunnelManager(ManagerConfig{Mode: tc.mode, PlainTCP: tc.plain, AutoQUICRetryInterval: time.Millisecond}, nil)
			defer m.Close()
			m.setState(StateConnected, initial)
			m.StartAutoReconnect()
			time.Sleep(30 * time.Millisecond)
			if m.Session() != initial || m.ConnectionMetrics().QUICRecoveryAttempts != 0 {
				t.Fatal("QUIC recovery ran outside encrypted auto TLS")
			}
		})
	}
}

func TestQUICPromotionRejectsStaleSessions(t *testing.T) {
	for _, event := range []string{"replaced", "closed", "canceled", "offline", "candidate closed", "network generation changed", "network changed during callback"} {
		t.Run(event, func(t *testing.T) {
			initial, _ := sessionPair(t, "tls")
			candidate, _ := sessionPair(t, "quic")
			var m *TunnelManager
			m = NewTunnelManager(ManagerConfig{Mode: ModeAuto}, func(_, state State, _ TunnelSession) {
				if state == StateReconnecting && event == "network changed during callback" {
					m.NetworkChanged(false)
					m.NetworkChanged(true)
				}
			})
			defer m.Close()
			m.setState(StateConnected, initial)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch event {
			case "replaced":
				current, _ := sessionPair(t, "tls")
				m.setState(StateConnected, current)
			case "closed":
				_ = m.Close()
			case "canceled":
				cancel()
			case "offline":
				m.NetworkChanged(false)
			case "candidate closed":
				_ = candidate.Close()
			case "network generation changed":
				m.mu.Lock()
				m.metrics.NetworkChanges++
				m.mu.Unlock()
			}
			if m.promoteQUIC(ctx, initial, candidate, time.Millisecond, 0) {
				t.Fatal("stale QUIC candidate was promoted")
			}
			if m.Session() == candidate || m.ConnectionMetrics().QUICRecoveries != 0 {
				t.Fatal("stale promotion changed the session or success metrics")
			}
		})
	}
}
