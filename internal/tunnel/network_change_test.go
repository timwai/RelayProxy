package tunnel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestNetworkRecoveryWakesBackoffAndOfflineStopsAttempts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	peers := make(chan *TLSSession, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			peer, err := ServerTLS(c, nil)
			if err != nil {
				c.Close()
				continue
			}
			peers <- peer
		}
	}()
	m := NewTunnelManager(ManagerConfig{ServerAddress: "127.0.0.1", TCPPort: l.Addr().(*net.TCPAddr).Port, Mode: ModeTCPOnly, PlainTCP: true}, nil)
	defer m.Close()
	defer func() {
		for {
			select {
			case p := <-peers:
				p.Close()
			default:
				return
			}
		}
	}()
	m.NetworkChanged(false)
	m.StartAutoReconnect()
	time.Sleep(50 * time.Millisecond)
	if m.ConnectionMetrics().Attempts != 0 {
		t.Fatal("dialed while offline")
	}
	start := time.Now()
	m.NetworkChanged(true)
	deadline := time.Now().Add(time.Second)
	for m.Session() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	first := m.Session()
	if first == nil || time.Since(start) > 800*time.Millisecond {
		t.Fatal("network recovery did not bypass backoff")
	}
	// Exercise first control-byte / authenticated readiness metrics.
	peer := <-peers
	go func() {
		s, err := peer.AcceptStream(context.Background())
		if err == nil {
			defer s.Close()
			_, _ = s.Write([]byte{7})
		}
	}()
	s, err := first.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s = m.TrackControlStream(first, s)
	_ = s.SetDeadline(time.Now().Add(time.Second))
	if _, err := s.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(s, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	m.RecordControlReady(first)
	peer.Close()
	m.NetworkChanged(false)
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("old transport survived network loss")
	}
	before := m.ConnectionMetrics().Attempts
	time.Sleep(100 * time.Millisecond)
	if m.ConnectionMetrics().Attempts != before {
		t.Fatal("offline reconnect loop continued dialing")
	}
	m.NetworkChanged(true)
	deadline = time.Now().Add(time.Second)
	for (m.Session() == nil || m.Session() == first) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Session() == nil || m.Session() == first {
		t.Fatal("transport not recreated on network recovery")
	}
	// Connect publishes state just before its metrics defer completes.
	m.connectMu.Lock()
	m.connectMu.Unlock()
	metrics := m.ConnectionMetrics()
	if metrics.Reconnects != 1 || metrics.NetworkChanges != 4 || metrics.Dial == nil || metrics.LastRecoveryMS <= 0 {
		t.Fatalf("incomplete metrics: %+v", metrics)
	}
	metrics.Dial.TotalMS = -1
	if m.ConnectionMetrics().Dial.TotalMS < 0 {
		t.Fatal("metrics exposed mutable state")
	}
}

func TestNetworkChangeCancelsInFlightQUIC(t *testing.T) {
	blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	m := NewTunnelManager(ManagerConfig{ServerAddress: "127.0.0.1", QUICPort: blackhole.LocalAddr().(*net.UDPAddr).Port, Mode: ModeQUICOnly}, nil)
	defer m.Close()
	done := make(chan error, 1)
	go func() { _, err := m.Connect(context.Background()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for m.State() != StateConnecting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m.NetworkChanged(false)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old network dial succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("old network dial not canceled")
	}
}
