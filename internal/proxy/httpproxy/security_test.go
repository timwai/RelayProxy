package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRejectInvalidAuthorityBeforeDial(t *testing.T) {
	for _, request := range []string{
		"CONNECT :22", "CONNECT example.com:0", "CONNECT example.com:99999",
		"CONNECT example.com:", "CONNECT [::1", "CONNECT bad/host:443",
		"GET http://example.com:99999/", "GET http://example.com:0/",
	} {
		t.Run(request, func(t *testing.T) {
			var dialed atomic.Bool
			s := newLifecycleServer(t, lifecycleDialer{dial: func(context.Context, string, string, uint16) (net.Conn, error) {
				dialed.Store(true)
				return nil, errors.New("must not dial")
			}})
			client := connectLifecycleClient(t, s)
			_, _ = fmt.Fprintf(client, "%s HTTP/1.1\r\nHost: example.com\r\n\r\n", request)
			resp, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest || dialed.Load() {
				t.Fatalf("status=%s dialed=%v", resp.Status, dialed.Load())
			}
		})
	}
}

func TestAuthorityCanonicalization(t *testing.T) {
	for _, tc := range []struct {
		authority, host string
		port            uint16
	}{
		{"example.com", "example.com", 443}, {"LOCALHOST:80", "localhost", 80},
		{"[2001:db8::1]", "2001:db8::1", 443}, {"[::1]:8443", "::1", 8443},
		{"2001:db8::1", "2001:db8::1", 443}, {"[::ffff:127.0.0.1]:22", "127.0.0.1", 22},
		{"foo_bar:8080", "foo_bar", 8080}, {"BÜCHER.example:443", "xn--bcher-kva.example", 443},
		{"例子。测试.", "xn--fsqu00a.xn--0zwm56d.", 443},
	} {
		t.Run(tc.authority, func(t *testing.T) {
			host, port, err := parseAuthority(tc.authority, 443)
			if err != nil || host != tc.host || port != tc.port {
				t.Fatalf("got %q:%d, %v", host, port, err)
			}
		})
	}
}

func TestCompatibleHostnamesReachDialerCanonicalized(t *testing.T) {
	for _, tc := range []struct{ authority, host string }{
		{"foo_bar:443", "foo_bar"}, {"bücher.example:443", "xn--bcher-kva.example"},
	} {
		t.Run(tc.authority, func(t *testing.T) {
			calls := make(chan string, 1)
			s := newLifecycleServer(t, lifecycleDialer{dial: func(_ context.Context, _ string, host string, _ uint16) (net.Conn, error) {
				calls <- host
				return nil, errors.New("test dial unavailable")
			}})
			client := connectLifecycleClient(t, s)
			_, _ = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", tc.authority, tc.authority)
			response, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusGatewayTimeout {
				t.Fatalf("compatible hostname rejected: %s", response.Status)
			}
			select {
			case host := <-calls:
				if host != tc.host {
					t.Fatalf("dial host=%q, want %q", host, tc.host)
				}
			default:
				t.Fatal("compatible hostname did not reach dialer")
			}
		})
	}
}

func TestRequestHeaderLimitDoesNotLimitBody(t *testing.T) {
	var dialed atomic.Bool
	s := newLifecycleServer(t, lifecycleDialer{dial: func(ctx context.Context, _ string, host string, port uint16) (net.Conn, error) {
		dialed.Store(true)
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	}})
	client := connectLifecycleClient(t, s)
	_, _ = fmt.Fprintf(client, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\nX-Large: %s\r\n\r\n", strings.Repeat("x", maxRequestHeaderBytes))
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge || dialed.Load() {
		t.Fatalf("status=%s dialed=%v", resp.Status, dialed.Load())
	}

	body := strings.Repeat("body", maxRequestHeaderBytes)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n != int64(len(body)) {
			t.Errorf("body copied %d bytes: %v", n, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	proxyURL, _ := url.Parse("http://" + s.Addr().String())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: lifecycleTimeout}
	response, err := httpClient.Post(upstream.URL, "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
}

func TestRequestBodyOutlivesHandshakeDeadline(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		b, err := io.ReadAll(r.Body)
		if err != nil || string(b) != "ab" {
			t.Errorf("body=%q error=%v", b, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: lifecycleDialer{}, HandshakeTimeout: 50 * time.Millisecond, IdleTimeout: time.Second})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := connectLifecycleClient(t, s)
	_, _ = fmt.Fprintf(client, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\n\r\na", upstream.URL)
	waitLifecycleSignal(t, entered, "upstream missed request headers")
	time.Sleep(150 * time.Millisecond)
	if _, err := io.WriteString(client, "b"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
}

func TestConnectDialOutlivesHandshakeAndIdleDeadlines(t *testing.T) {
	upstream, peer := net.Pipe()
	defer peer.Close()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", HandshakeTimeout: 50 * time.Millisecond, IdleTimeout: 50 * time.Millisecond,
		Dialer: lifecycleDialer{dial: func(context.Context, string, string, uint16) (net.Conn, error) {
			time.Sleep(150 * time.Millisecond)
			return upstream, nil
		}},
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := connectLifecycleClient(t, s)
	_, _ = io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT reply inherited an earlier phase's deadline: %v, %v", resp, err)
	}
}

func TestConnectionLimitReleasesAndIdleClosesHalfOpenTunnel(t *testing.T) {
	upstream := listenLifecycleTarget(t)
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: lifecycleDialer{}, MaxConnections: 1, IdleTimeout: 100 * time.Millisecond})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := connectLifecycleClient(t, s)
	_, _ = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: target\r\n\r\n", upstream.Addr())
	target := acceptLifecycleTarget(t, upstream)
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	extra := connectLifecycleClient(t, s)
	requireClosedRead(t, extra, extra)
	// EOF in one direction must not leave the reverse pump stuck forever.
	_ = client.(*net.TCPConn).CloseWrite()
	requireClosedRead(t, client, client)
	requireClosedRead(t, target, target)
	deadline := time.Now().Add(lifecycleTimeout)
	for len(s.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.slots) != 0 {
		t.Fatal("connection slot was not released")
	}
	second := connectLifecycleClient(t, s)
	_, _ = fmt.Fprintf(second, "CONNECT %s HTTP/1.1\r\nHost: target\r\n\r\n", upstream.Addr())
	_ = acceptLifecycleTarget(t, upstream)
	if resp, err := http.ReadResponse(bufio.NewReader(second), &http.Request{Method: http.MethodConnect}); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("slot was not reusable: %v %v", resp, err)
	}
}
