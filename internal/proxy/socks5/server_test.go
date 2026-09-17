package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type testDialer func(context.Context, string, string, uint16) (net.Conn, error)

func (f testDialer) DialTCP(ctx context.Context, exitID, host string, port uint16) (net.Conn, error) {
	return f(ctx, exitID, host, port)
}

func (f testDialer) DialUDP(context.Context, string, string, uint16) (net.PacketConn, error) {
	return nil, errors.New("UDP is not used by these tests")
}

func startTestServer(t *testing.T, dialer testDialer) *Server {
	t.Helper()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: dialer})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestServer(t, s) })
	return s
}

func closeTestServer(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Close did not finish after cancelling connections")
	}
}

func proxyClient(t *testing.T, s *Server) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}

func negotiate(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{Version5, 1, AuthMethodNone}); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{Version5, AuthMethodNone} {
		t.Fatalf("negotiation reply %v, error %v", reply, err)
	}
}

func requestTarget(t *testing.T, conn net.Conn, target *net.TCPAddr) {
	t.Helper()
	request := []byte{Version5, CmdConnect, 0, AtypIPv4}
	request = append(request, target.IP.To4()...)
	request = binary.BigEndian.AppendUint16(request, uint16(target.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
}

func assertDisconnected(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [64]byte
	for {
		// A reply queued before shutdown may still be buffered by TCP. Drain
		// those bytes and require the transport to close, rather than time out.
		_, err := conn.Read(b[:])
		if err == nil {
			continue
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("connection remained open after Close")
		}
		return
	}
}

func TestCloseStopsDirectConnectionAndWaitsForHandlers(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := upstream.Accept()
		accepted <- conn
	}()
	direct := testDialer(func(ctx context.Context, _ string, host string, port uint16) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	})
	s := startTestServer(t, direct)
	client := proxyClient(t, s)
	negotiate(t, client)
	requestTarget(t, client, upstream.Addr().(*net.TCPAddr))
	var reply [10]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] != RepSuccess {
		t.Fatalf("CONNECT reply %v, error %v", reply, err)
	}
	var target net.Conn
	select {
	case target = <-accepted:
		if target == nil {
			t.Fatal("upstream accept failed")
		}
	case <-time.After(time.Second):
		t.Fatal("upstream was not connected")
	}
	defer target.Close()
	_ = target.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var payload [4]byte
	if _, err := io.ReadFull(target, payload[:]); err != nil || string(payload[:]) != "ping" {
		t.Fatalf("direct forwarding failed: %q, %v", payload, err)
	}
	if _, err := target.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, payload[:]); err != nil || string(payload[:]) != "pong" {
		t.Fatalf("direct reply failed: %q, %v", payload, err)
	}
	closeTestServer(t, s)
	assertDisconnected(t, client)
	assertDisconnected(t, target)
	s.mu.Lock()
	remaining := len(s.conns)
	s.mu.Unlock()
	if remaining != 0 || !errors.Is(s.ctx.Err(), context.Canceled) {
		t.Fatalf("Close returned with %d tracked connections, context=%v", remaining, s.ctx.Err())
	}
}

func TestCloseInterruptsPartialHandshake(t *testing.T) {
	for _, stage := range []string{"negotiation", "request"} {
		t.Run(stage, func(t *testing.T) {
			s := startTestServer(t, nil)
			client := proxyClient(t, s)
			if stage == "request" {
				negotiate(t, client)
			}
			if _, err := client.Write([]byte{Version5}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				s.mu.Lock()
				accepted := len(s.conns) > 0
				s.mu.Unlock()
				if accepted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("partial handshake was never accepted")
				}
				time.Sleep(time.Millisecond)
			}
			closeTestServer(t, s)
			assertDisconnected(t, client)
		})
	}
}

func TestCloseCancelsPendingDialAndClosesLateTarget(t *testing.T) {
	for _, lateSuccess := range []bool{false, true} {
		t.Run(strconv.FormatBool(lateSuccess), func(t *testing.T) {
			target, peer := net.Pipe()
			defer target.Close()
			defer peer.Close()
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			allowReturn := func() { releaseOnce.Do(func() { close(release) }) }
			defer allowReturn()
			dialer := testDialer(func(ctx context.Context, _, _ string, _ uint16) (net.Conn, error) {
				started <- ctx
				<-ctx.Done()
				<-release
				if lateSuccess {
					return target, nil
				}
				return nil, ctx.Err()
			})
			s := startTestServer(t, dialer)
			client := proxyClient(t, s)
			negotiate(t, client)
			requestTarget(t, client, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443})
			var dialCtx context.Context
			select {
			case dialCtx = <-started:
			case <-time.After(time.Second):
				t.Fatal("dial was not started")
			}
			closed := make(chan error, 2)
			go func() { closed <- s.Close() }()
			go func() { closed <- s.Close() }()
			select {
			case <-dialCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel the pending dial context")
			}
			select {
			case err := <-closed:
				t.Fatalf("Close returned before the handler completed: %v", err)
			default:
			}
			allowReturn()
			for i := 0; i < 2; i++ {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("concurrent Close did not wait for the same shutdown")
				}
			}
			assertDisconnected(t, client)
			if lateSuccess {
				assertDisconnected(t, peer)
			}
		})
	}
}

func TestStartAndCloseLifecycle(t *testing.T) {
	t.Run("closed before start", func(t *testing.T) {
		s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0"})
		closeTestServer(t, s)
		closeTestServer(t, s)
		if err := s.Start(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Start after Close = %v", err)
		}
	})
	t.Run("duplicate start", func(t *testing.T) {
		s := startTestServer(t, nil)
		addr := s.Addr().String()
		if err := s.Start(); err == nil || s.Addr().String() != addr {
			t.Fatal("second Start replaced or leaked the listener")
		}
	})
	t.Run("failed listen can retry", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		s := NewServer(ServerConfig{ListenAddr: occupied.Addr().String()})
		t.Cleanup(func() { closeTestServer(t, s) })
		if err := s.Start(); err == nil || s.Addr() != nil {
			t.Fatal("conflicting listen should fail without installing a listener")
		}
		_ = occupied.Close()
		if err := s.Start(); err != nil {
			t.Fatalf("retry after listen failure: %v", err)
		}
	})
	t.Run("close after listen failure", func(t *testing.T) {
		s := NewServer(ServerConfig{ListenAddr: "invalid-listen-address"})
		if err := s.Start(); err == nil {
			t.Fatal("invalid listen unexpectedly succeeded")
		}
		closeTestServer(t, s)
		closeTestServer(t, s)
	})
	t.Run("start races close", func(t *testing.T) {
		s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0"})
		start := make(chan struct{})
		result := make(chan error, 1)
		go func() { <-start; result <- s.Start() }()
		close(start)
		closeTestServer(t, s)
		if err := <-result; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		if addr := s.Addr(); addr != nil {
			conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
			if err == nil {
				_ = conn.Close()
				t.Fatal("Start/Close race leaked a listener")
			}
		}
	})
}
