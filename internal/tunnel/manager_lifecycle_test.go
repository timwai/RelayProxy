package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

func TestManagerAutoRetainsTLSFallbackBudget(t *testing.T) {
	blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cert := generateSelfSignedCert(t)
	accepted := make(chan *TLSSession, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		s, err := ServerTLS(tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}}), nil)
		if err != nil {
			_ = c.Close()
			return
		}
		accepted <- s
	}()
	m := NewTunnelManager(ManagerConfig{ServerAddress: "127.0.0.1", QUICPort: blackhole.LocalAddr().(*net.UDPAddr).Port, TCPPort: l.Addr().(*net.TCPAddr).Port, Mode: ModeAuto, TLSConfig: &tls.Config{InsecureSkipVerify: true}, ConnectTimeout: 800 * time.Millisecond}, nil)
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	s, err := m.Connect(ctx)
	if err != nil {
		t.Fatalf("fallback lost its budget: %v", err)
	}
	if s.Transport() != TransportTLS {
		t.Fatalf("transport=%s", s.Transport())
	}
	select {
	case peer := <-accepted:
		defer peer.Close()
	case <-time.After(time.Second):
		t.Fatal("TLS accept did not finish")
	}
}

func TestManagerCloseCancelsInFlightConnect(t *testing.T) {
	blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	m := NewTunnelManager(ManagerConfig{ServerAddress: "127.0.0.1", QUICPort: blackhole.LocalAddr().(*net.UDPAddr).Port, Mode: ModeQUICOnly, TLSConfig: &tls.Config{InsecureSkipVerify: true}}, nil)
	done := make(chan error, 1)
	go func() { _, err := m.Connect(context.Background()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for m.State() != StateConnecting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed manager connected")
		}
	case <-time.After(time.Second):
		t.Fatal("Connect did not stop")
	}
	m.StartAutoReconnect()
	m.StartAutoReconnect()
	if m.State() != StateClosed || m.Session() != nil {
		t.Fatal("closed manager resurrected")
	}
	if _, err := m.Connect(context.Background()); err == nil {
		t.Fatal("Connect after Close succeeded")
	}
}

func TestManagerLateLossDoesNotReplaceNewSession(t *testing.T) {
	old, _ := sessionPair(t, "tls")
	current, _ := sessionPair(t, "tls")
	m := NewTunnelManager(ManagerConfig{}, nil)
	defer m.Close()
	m.setState(StateConnected, current)
	m.MarkSessionLost(old)
	if m.Session() != current || m.State() != StateConnected {
		t.Fatal("old loss cleared new session")
	}
	if m.cfg.TLSConfig.InsecureSkipVerify || m.cfg.TLSConfig.MinVersion < tls.VersionTLS13 {
		t.Fatal("insecure TLS defaults")
	}
	m.MarkSessionLost(current)
	if m.Session() != nil || m.State() != StateDisconnected {
		t.Fatal("current loss was ignored")
	}
	select {
	case <-current.Done():
	default:
		t.Fatal("lost session was not closed")
	}
}

func TestNativeUDPRequiresPeerCapability(t *testing.T) {
	client, _ := sessionPair(t, "quic")
	SetPeerCapabilities(client, nil)
	if !SupportsDatagrams(client) {
		t.Fatal("QUIC transport capability lost")
	}
	if _, err := OpenDatagramChannel(client, 1); !errors.Is(err, ErrDatagramsUnsupported) {
		t.Fatalf("unadvertised peer accepted native UDP: %v", err)
	}
}
