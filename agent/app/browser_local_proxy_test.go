package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/proxy"
)

type browserEchoDialer struct{}

func (browserEchoDialer) DialBrowserNetworkTCP(ctx context.Context, host string, port uint16) (net.Conn, error) {
	if host == "" || port == 0 {
		return nil, fmt.Errorf("invalid destination")
	}
	c, other := net.Pipe()
	go func() { defer other.Close(); _, _ = io.Copy(other, other) }()
	return c, nil
}
func newStartedBrowserLocalProxy(t *testing.T, d browserLocalDialer) *browserLocalProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	p := newBrowserLocalProxy(fmt.Sprintf("127.0.0.1:%d", port), d)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func TestBrowserDestinationAllowsAnyPortDomainAndIP(t *testing.T) {
	for _, tc := range []struct {
		authority   string
		defaultPort uint16
		host        string
		port        uint16
	}{
		{"login.example.com:443", 0, "login.example.com", 443},
		{"printer.local:8443", 0, "printer.local", 8443},
		{"internal:8080", 0, "internal", 8080},
		{"10.0.0.12:80", 0, "10.0.0.12", 80},
		{"127.0.0.1:5555", 0, "127.0.0.1", 5555},
		{"[::1]:9443", 0, "::1", 9443},
		{"example.com", 80, "example.com", 80},
		{"[::1]", 80, "::1", 80},
	} {
		host, port, err := browserDestination(tc.authority, tc.defaultPort)
		if err != nil || host != tc.host || port != tc.port {
			t.Fatalf("%q => %q:%d %v; want %q:%d", tc.authority, host, port, err, tc.host, tc.port)
		}
	}
	for _, authority := range []string{"", "example.com:0", "example.com:65536", "example.com:-2",
		"example.com:abc", "user:password@example.com:443", "example.com:80/path"} {
		if _, _, err := browserDestination(authority, 0); err == nil {
			t.Fatalf("malformed authority accepted: %q", authority)
		}
	}
}
func TestBrowserLocalProxyConnectAnyHostPortWithoutAuth(t *testing.T) {
	p := newStartedBrowserLocalProxy(t, browserEchoDialer{})
	for _, dst := range []string{"other.example.com:8443", "127.0.0.1:443", "printer.local:80",
		"10.0.0.10:8080", "[::1]:9443", "internal:18080"} {
		c, err := net.Dial("tcp", p.listen)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", dst, dst)
		reader := bufio.NewReader(c)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(line, "200") {
			t.Fatalf("%s CONNECT: %q %v", dst, line, err)
		}
		for {
			line, err = reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = c.Write([]byte("hello"))
		b := make([]byte, 5)
		if _, err = io.ReadFull(reader, b); err != nil || string(b) != "hello" {
			t.Fatalf("%s tunnel: %q %v", dst, b, err)
		}
		_ = c.Close()
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

type browserHTTPDialer struct{ request chan *http.Request }

func (d browserHTTPDialer) DialBrowserNetworkTCP(ctx context.Context, host string, port uint16) (net.Conn, error) {
	if host != "printer.local" || port != 8080 {
		return nil, fmt.Errorf("wrong HTTP target %s:%d", host, port)
	}
	c, other := net.Pipe()
	go func() {
		defer other.Close()
		req, err := http.ReadRequest(bufio.NewReader(other))
		if err == nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
			select {
			case d.request <- req:
			default:
			}
		}
		_, _ = io.WriteString(other, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nworks")
	}()
	return c, nil
}
func TestBrowserLocalProxyForwardsPlainHTTPWithoutAuth(t *testing.T) {
	requests := make(chan *http.Request, 1)
	p := newStartedBrowserLocalProxy(t, browserHTTPDialer{request: requests})
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: p.listen})}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "http://printer.local:8080/path?x=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie := "session=" + strings.Repeat("x", 16*1024)
	request.Header.Set("Cookie", cookie)
	resp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(body) != "works" {
		t.Fatalf("HTTP proxy response %d %q %v", resp.StatusCode, body, err)
	}
	select {
	case req := <-requests:
		if req.URL.Path != "/path" || req.Host != "printer.local:8080" {
			t.Fatalf("HTTP request modified: %s %s", req.URL, req.Host)
		}
		if req.Header.Get("Cookie") != cookie {
			t.Fatal("large browser Cookie header was lost or truncated")
		}
	case <-time.After(time.Second):
		t.Fatal("native dialer did not receive HTTP request")
	}
}

type browserLocalRecorder struct {
	h    http.Header
	code int
}

func (w *browserLocalRecorder) Header() http.Header         { return w.h }
func (w *browserLocalRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (w *browserLocalRecorder) WriteHeader(code int)        { w.code = code }
func TestBrowserLocalProxyRejectsMalformedRequestsOnly(t *testing.T) {
	p := newBrowserLocalProxy("127.0.0.1:21291", browserEchoDialer{})
	for _, tc := range []struct {
		method, host, rawURL string
		want                 int
	}{
		{"GET", "example.com:80", "https://127.0.0.1/", 400},
		{"CONNECT", "example.com:0", "http://127.0.0.1/", 400},
		{"CONNECT", "example.com:65536", "http://127.0.0.1/", 400},
		{"CONNECT", "example.com:443", "http://127.0.0.1/", 500},
	} {
		r, _ := http.NewRequest(tc.method, tc.rawURL, nil)
		r.Host = tc.host
		if tc.method == http.MethodConnect {
			r.URL.Host = tc.host
		}
		w := &browserLocalRecorder{h: http.Header{}}
		p.ServeHTTP(w, r)
		if w.code != tc.want {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.host, w.code, tc.want)
		}
	}
}

type browserClientMetadataDialer struct {
	info chan proxy.ClientInfo
}

func (d browserClientMetadataDialer) DialBrowserNetworkTCP(ctx context.Context, host string, port uint16) (net.Conn, error) {
	select {
	case d.info <- proxy.ClientFromContext(ctx):
	default:
	}
	return browserEchoDialer{}.DialBrowserNetworkTCP(ctx, host, port)
}

// Process-based rules require the original Chrome->Agent socket tuple.
func TestBrowserLocalProxyPreservesChromeSourceTuple(t *testing.T) {
	observed := make(chan proxy.ClientInfo, 1)
	p := newStartedBrowserLocalProxy(t, browserClientMetadataDialer{info: observed})
	c, err := net.Dial("tcp", p.listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprint(c, "CONNECT login.example.com:443 HTTP/1.1\r\nHost: login.example.com:443\r\n\r\n")
	select {
	case info := <-observed:
		if info.Entry != "browser_chrome" || !info.Source.IsValid() || !info.Local.IsValid() {
			t.Fatalf("missing Chrome process lookup metadata: %+v", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("browser proxy never attempted native Agent routing")
	}
}
