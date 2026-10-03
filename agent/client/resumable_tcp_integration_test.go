package client

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	exitpkg "relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/traffic"
	"relayproxy/internal/tunnel"
)

type handlerTunnelStream struct {
	net.Conn
}

func (s handlerTunnelStream) CloseWrite() error { return nil }

type handlerSession struct {
	handler *exitpkg.Handler

	mu      sync.Mutex
	active  net.Conn
	streams []net.Conn
	opens   atomic.Int32
	done    chan struct{}
	once    sync.Once
}

func newHandlerSession(handler *exitpkg.Handler) *handlerSession {
	return &handlerSession{handler: handler, done: make(chan struct{})}
}

func (s *handlerSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	clientEnd, exitEnd := net.Pipe()
	s.opens.Add(1)
	s.mu.Lock()
	s.active = exitEnd
	s.streams = append(s.streams, exitEnd)
	s.mu.Unlock()

	go s.handler.HandleStream(context.Background(), handlerTunnelStream{Conn: exitEnd})
	return handlerTunnelStream{Conn: clientEnd}, nil
}

func (s *handlerSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, net.ErrClosed
}

func (s *handlerSession) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *handlerSession) PeerSupportsStreamResume() bool  { return true }
func (s *handlerSession) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 443}
}
func (s *handlerSession) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 40000}
}
func (s *handlerSession) Done() <-chan struct{} { return s.done }

func (s *handlerSession) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		streams := append([]net.Conn(nil), s.streams...)
		s.streams = nil
		s.active = nil
		s.mu.Unlock()
		for _, stream := range streams {
			_ = stream.Close()
		}
	})
	return nil
}

func (s *handlerSession) closeActive(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if active != nil {
			if err := active.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("direct session never exposed an active stream")
		}
		time.Sleep(time.Millisecond)
	}
}

func startTCPEchoTarget(t *testing.T) (host string, port uint16, accepts *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(portValue), &accepted
}

func TestResumableTCPDirectLossRecoversThroughRelayWithoutRedialingTarget(t *testing.T) {
	host, port, targetAccepts := startTCPEchoTarget(t)
	handler := exitpkg.NewHandler(exitpkg.HandlerConfig{
		ResumeEnabled:     true,
		ResumeGrace:       2 * time.Second,
		ResumeReplayLimit: 256 << 10,
	})
	t.Cleanup(func() { _ = handler.Close() })

	direct := newHandlerSession(handler)
	relay := newHandlerSession(handler)
	t.Cleanup(func() {
		_ = direct.Close()
		_ = relay.Close()
	})

	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(exitID string) (tunnel.TunnelSession, bool) {
		if exitID == "exit" {
			return direct, true
		}
		return nil, false
	}, nil)
	dialer.ConfigureDirectPolicy("auto", true)
	dialer.ConfigureStreamResume(true, 256<<10)
	var fallbacks, directFailures atomic.Int32
	dialer.ConfigureDirectMetrics(func(string) { fallbacks.Add(1) })
	dialer.ConfigureDirectFailure(func(string, string) { directFailures.Add(1) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialer.DialTCP(ctx, "exit", host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requireTCPPath(t, conn, protocol.P2PPathDirectQUIC)
	registry := traffic.NewRegistry(2, 2)
	conn = traffic.WrapConn(conn, registry.Start(traffic.Metadata{Protocol: "tcp", Action: "PROXY", ExitID: "exit"}))
	defer conn.Close()
	if got := registry.Snapshot().Connections[0].Path; got != protocol.P2PPathDirectQUIC {
		t.Fatalf("initial recorded path = %q", got)
	}

	first := []byte("before-direct-loss")
	if _, err := conn.Write(first); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(first))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(first) {
		t.Fatalf("first echo=%q want=%q", got, first)
	}
	if targetAccepts.Load() != 1 {
		t.Fatalf("target accepts=%d before migration, want 1", targetAccepts.Load())
	}

	direct.closeActive(t)

	second := []byte("after-relay-rebind")
	writeDone := make(chan error, 1)
	go func() {
		_, err := conn.Write(second)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write after direct loss: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("write did not recover through Relay")
	}

	got = make([]byte, len(second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second) {
		t.Fatalf("second echo=%q want=%q", got, second)
	}
	if relay.opens.Load() == 0 {
		t.Fatal("Relay was not used for recovery")
	}
	pathDeadline := time.Now().Add(time.Second)
	for registry.Snapshot().Connections[0].Path != protocol.P2PPathRelayQUIC {
		if time.Now().After(pathDeadline) {
			t.Fatal("connection record did not follow the successful Relay rebind")
		}
		time.Sleep(time.Millisecond)
	}
	if targetAccepts.Load() != 1 {
		t.Fatalf("target was redialed during migration: accepts=%d", targetAccepts.Load())
	}
	if fallbacks.Load() != 1 || directFailures.Load() != 1 {
		t.Fatalf("after P2P loss fallbacks=%d directFailures=%d, want 1/1", fallbacks.Load(), directFailures.Load())
	}

	// A later Relay stream loss should rebind the same logical stream again,
	// without redialing the target or counting a second P2P fallback.
	relay.closeActive(t)
	third := []byte("after-relay-rebind-again")
	writeDone = make(chan error, 1)
	go func() {
		_, err := conn.Write(third)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write after Relay loss: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("write did not recover after Relay stream loss")
	}
	got = make([]byte, len(third))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(third) {
		t.Fatalf("third echo=%q want=%q", got, third)
	}
	if targetAccepts.Load() != 1 {
		t.Fatalf("target was redialed during repeated recovery: accepts=%d", targetAccepts.Load())
	}
	if relay.opens.Load() < 2 {
		t.Fatalf("Relay rebind opens=%d, want at least 2", relay.opens.Load())
	}
	if fallbacks.Load() != 1 || directFailures.Load() != 1 {
		t.Fatalf("Relay recovery inflated P2P metrics: fallbacks=%d directFailures=%d", fallbacks.Load(), directFailures.Load())
	}
}
