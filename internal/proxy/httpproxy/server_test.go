package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

const lifecycleTimeout = 3 * time.Second

type lifecycleDialer struct {
	dial func(context.Context, string, string, uint16) (net.Conn, error)
}

func (d lifecycleDialer) DialTCP(ctx context.Context, exitID, host string, port uint16) (net.Conn, error) {
	if d.dial != nil {
		return d.dial(ctx, exitID, host, port)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
}

func (lifecycleDialer) DialUDP(context.Context, string, string, uint16) (net.PacketConn, error) {
	return nil, errors.New("unexpected UDP dial")
}

func newLifecycleServer(t *testing.T, dialer lifecycleDialer) *Server {
	t.Helper()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: dialer})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeLifecycleServer(t, s) })
	return s
}

func closeLifecycleServer(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(lifecycleTimeout):
		t.Fatal("Close did not finish after canceling active I/O")
	}
	s.mu.Lock()
	n := len(s.conns)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("Close returned with %d registered connections", n)
	}
}

func connectLifecycleClient(t *testing.T, s *Server) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.Addr().String(), lifecycleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(lifecycleTimeout))
	return conn
}

func listenLifecycleTarget(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener.(*net.TCPListener)
}

func acceptLifecycleTarget(t *testing.T, listener *net.TCPListener) net.Conn {
	t.Helper()
	_ = listener.SetDeadline(time.Now().Add(lifecycleTimeout))
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(lifecycleTimeout))
	return conn
}

func requireClosedRead(t *testing.T, conn net.Conn, reader io.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(lifecycleTimeout))
	// Bytes sent just before Close may still be queued at the peer.
	if n, err := io.CopyN(io.Discard, reader, 1<<20); err == nil {
		t.Fatalf("connection produced %d bytes without closing", n)
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("connection was left open after Close: %v", err)
	}
}

func waitLifecycleSignal(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(lifecycleTimeout):
		t.Fatal(message)
	}
}

func waitLifecycleAccepted(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.NewTimer(lifecycleTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		accepted := len(s.conns) != 0
		s.mu.Unlock()
		if accepted {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("server did not accept client")
		}
	}
}

func TestCloseStopsDirectConnections(t *testing.T) {
	for _, method := range []string{http.MethodConnect, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			upstream := listenLifecycleTarget(t)
			s := newLifecycleServer(t, lifecycleDialer{})
			client := connectLifecycleClient(t, s)
			address := upstream.Addr().String()
			requestTarget := address
			if method == http.MethodGet {
				requestTarget = "http://" + address + "/stream"
			}
			if _, err := fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\n", method, requestTarget, address); err != nil {
				t.Fatal(err)
			}
			target := acceptLifecycleTarget(t, upstream)
			clientReader := bufio.NewReader(client)
			if method == http.MethodGet {
				req, err := http.ReadRequest(bufio.NewReader(target))
				if err != nil {
					t.Fatal(err)
				}
				if req.URL.Path != "/stream" {
					t.Fatalf("forwarded path = %q", req.URL.Path)
				}
				if _, err := io.WriteString(target, "HTTP/1.1 200 OK\r\nContent-Length: 1000000\r\n\r\nhello"); err != nil {
					t.Fatal(err)
				}
			}
			response, err := http.ReadResponse(clientReader, &http.Request{Method: method})
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("proxy response = %s", response.Status)
			}
			var stream io.Reader = response.Body
			if method == http.MethodConnect {
				stream = clientReader
				if _, err := io.WriteString(client, "hello"); err != nil {
					t.Fatal(err)
				}
				var request [5]byte
				if _, err := io.ReadFull(target, request[:]); err != nil {
					t.Fatal(err)
				}
				if string(request[:]) != "hello" {
					t.Fatalf("upstream received %q", request)
				}
				if _, err := target.Write(request[:]); err != nil {
					t.Fatal(err)
				}
			}
			var payload [5]byte
			if _, err := io.ReadFull(stream, payload[:]); err != nil {
				t.Fatal(err)
			}
			if string(payload[:]) != "hello" {
				t.Fatalf("client received %q", payload)
			}

			closeLifecycleServer(t, s)
			requireClosedRead(t, client, stream)
			requireClosedRead(t, target, target)
		})
	}
}

func TestConnectForwardsBufferedTunnelData(t *testing.T) {
	upstream := listenLifecycleTarget(t)
	s := newLifecycleServer(t, lifecycleDialer{})
	client := connectLifecycleClient(t, s)
	address := upstream.Addr().String()
	const early = "early tunnel bytes"
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", address, address, early)
	// One write lets the request parser prefetch tunnel data with the headers.
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	target := acceptLifecycleTarget(t, upstream)
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response = %s", response.Status)
	}
	const later = " followed by more data"
	if _, err := io.WriteString(client, later); err != nil {
		t.Fatal(err)
	}
	want := early + later
	payload := make([]byte, len(want))
	if _, err := io.ReadFull(target, payload); err != nil {
		t.Fatalf("read tunnel bytes sent with and after CONNECT: %v", err)
	}
	if string(payload) != want {
		t.Fatalf("upstream received %q, want %q", payload, want)
	}
	if _, err := target.Write(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != want {
		t.Fatalf("client received %q, want %q", payload, want)
	}
	closeLifecycleServer(t, s)
}

func TestCloseStopsPartialRequests(t *testing.T) {
	for _, request := range []string{
		"",
		"GET http://example.invalid/ HTTP/1.1\r\nHost: example.invalid",
		"CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\n",
	} {
		t.Run(fmt.Sprintf("prefix_%d_bytes", len(request)), func(t *testing.T) {
			s := newLifecycleServer(t, lifecycleDialer{dial: func(context.Context, string, string, uint16) (net.Conn, error) {
				t.Error("partial request unexpectedly reached DialTCP")
				return nil, errors.New("unexpected dial")
			}})
			client := connectLifecycleClient(t, s)
			if request != "" {
				if _, err := io.WriteString(client, request); err != nil {
					t.Fatal(err)
				}
			}
			waitLifecycleAccepted(t, s)
			closeLifecycleServer(t, s)
			requireClosedRead(t, client, client)
		})
	}
}

func TestCloseStopsIncompleteRequestBody(t *testing.T) {
	upstream := listenLifecycleTarget(t)
	s := newLifecycleServer(t, lifecycleDialer{})
	client := connectLifecycleClient(t, s)
	address := upstream.Addr().String()
	if _, err := fmt.Fprintf(client, "POST http://%s/upload HTTP/1.1\r\nHost: %s\r\nContent-Length: 1000000\r\n\r\npartial", address, address); err != nil {
		t.Fatal(err)
	}
	target := acceptLifecycleTarget(t, upstream)
	req, err := http.ReadRequest(bufio.NewReader(target))
	if err != nil {
		t.Fatal(err)
	}
	var partial [7]byte
	if _, err := io.ReadFull(req.Body, partial[:]); err != nil {
		t.Fatal(err)
	}
	if string(partial[:]) != "partial" {
		t.Fatalf("forwarded body = %q", partial)
	}

	closeLifecycleServer(t, s)
	requireClosedRead(t, client, client)
	requireClosedRead(t, target, req.Body)
}

func TestCloseCancelsAndWaitsForDial(t *testing.T) {
	for _, method := range []string{http.MethodConnect, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			s := newLifecycleServer(t, lifecycleDialer{dial: func(ctx context.Context, _, _ string, _ uint16) (net.Conn, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return nil, ctx.Err()
			}})
			t.Cleanup(unblock)
			client := connectLifecycleClient(t, s)
			target := "example.invalid:443"
			if method == http.MethodGet {
				target = "http://example.invalid/"
			}
			if _, err := fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: example.invalid\r\n\r\n", method, target); err != nil {
				t.Fatal(err)
			}
			waitLifecycleSignal(t, entered, "request did not reach DialTCP")

			closed := make(chan error, 2)
			go func() { closed <- s.Close() }()
			go func() { closed <- s.Close() }()
			waitLifecycleSignal(t, canceled, "Close did not cancel the dial context")
			select {
			case err := <-closed:
				t.Fatalf("Close returned before the active dial exited: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			unblock()
			for range 2 {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(lifecycleTimeout):
					t.Fatal("concurrent Close did not finish after dial returned")
				}
			}
			requireClosedRead(t, client, client)
		})
	}
}

func TestCloseRejectsLateDialSuccess(t *testing.T) {
	upstream := listenLifecycleTarget(t)
	entered := make(chan struct{})
	s := newLifecycleServer(t, lifecycleDialer{dial: func(ctx context.Context, _, host string, port uint16) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			return nil, err
		}
		close(entered)
		<-ctx.Done()
		// A dial may win its own cancellation race and still return a connection.
		return conn, nil
	}})
	client := connectLifecycleClient(t, s)
	if _, err := fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Addr(), upstream.Addr()); err != nil {
		t.Fatal(err)
	}
	target := acceptLifecycleTarget(t, upstream)
	waitLifecycleSignal(t, entered, "upstream dial did not complete")

	closeLifecycleServer(t, s)
	requireClosedRead(t, client, client)
	requireClosedRead(t, target, target)
}

func TestServerLifecycle(t *testing.T) {
	t.Run("close_before_start", func(t *testing.T) {
		s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0"})
		closeLifecycleServer(t, s)
		closeLifecycleServer(t, s)
		if err := s.Start(); err == nil {
			t.Fatal("Start succeeded after Close")
		}
		if s.Addr() != nil {
			t.Fatal("closed server acquired a listener")
		}
	})
	t.Run("duplicate_start", func(t *testing.T) {
		s := newLifecycleServer(t, lifecycleDialer{})
		addr := s.Addr().String()
		if err := s.Start(); err == nil {
			t.Fatal("second Start succeeded")
		}
		if s.Addr().String() != addr {
			t.Fatal("second Start replaced the live listener")
		}
		closeLifecycleServer(t, s)
		if err := s.Start(); err == nil {
			t.Fatal("Start succeeded after Close")
		}
	})
	t.Run("failed_start_can_retry", func(t *testing.T) {
		occupied := listenLifecycleTarget(t)
		s := NewServer(ServerConfig{ListenAddr: occupied.Addr().String()})
		t.Cleanup(func() { closeLifecycleServer(t, s) })
		if err := s.Start(); err == nil {
			t.Fatal("Start succeeded on an occupied port")
		}
		if s.Addr() != nil {
			t.Fatal("failed Start published a listener")
		}
		_ = occupied.Close()
		if err := s.Start(); err != nil {
			t.Fatalf("Start after failed bind: %v", err)
		}
		client := connectLifecycleClient(t, s)
		waitLifecycleAccepted(t, s)
		closeLifecycleServer(t, s)
		requireClosedRead(t, client, client)
	})
}

func TestConcurrentStartClose(t *testing.T) {
	for range 25 {
		s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0"})
		ready := make(chan struct{})
		started := make(chan error, 1)
		closed := make(chan error, 1)
		go func() {
			<-ready
			started <- s.Start()
		}()
		go func() {
			<-ready
			closed <- s.Close()
		}()
		close(ready)
		select {
		case <-started:
		case <-time.After(lifecycleTimeout):
			t.Fatal("concurrent Start did not finish")
		}
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(lifecycleTimeout):
			t.Fatal("concurrent Close did not finish")
		}
		if addr := s.Addr(); addr != nil {
			conn, err := net.DialTimeout("tcp", addr.String(), 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				t.Fatal("listener survived concurrent Start/Close")
			}
		}
	}
}
