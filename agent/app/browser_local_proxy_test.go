package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type browserEchoDialer struct{}

func (browserEchoDialer) DialBrowserNetworkTCP(ctx context.Context, host string, port uint16) (net.Conn, error) {
	if (host != "login.example.com" && host != "other.example.com") || port != 443 {
		return nil, fmt.Errorf("wrong target")
	}
	c, other := net.Pipe()
	go func() { defer other.Close(); _, _ = io.Copy(other, other) }()
	return c, nil
}
func TestBrowserLocalProxyGuardsAndTunnel(t *testing.T) {
	p := newBrowserLocalProxy("127.0.0.1:0", browserEchoDialer{})
	for _, tt := range []struct {
		method, host string
		want         int
	}{
		{"GET", "login.example.com:443", 405},
		{"CONNECT", "127.0.0.1:443", 403},
		{"CONNECT", "internal.local:443", 403},
		{"CONNECT", "login.example.com:80", 403},
		{"CONNECT", "login.example.com:8443", 403},
	} {
		r, _ := http.NewRequest(tt.method, "http://127.0.0.1/", nil)
		r.Host = tt.host
		w := &browserLocalRecorder{h: http.Header{}}
		p.ServeHTTP(w, r)
		if w.code != tt.want {
			t.Fatalf("%s %s: got %d want %d", tt.method, tt.host, w.code, tt.want)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	p = newBrowserLocalProxy(fmt.Sprintf("127.0.0.1:%d", port), browserEchoDialer{})
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprint(c, "CONNECT other.example.com:443 HTTP/1.1\r\nHost: other.example.com:443\r\n\r\n")
	reader := bufio.NewReader(c)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("CONNECT %q %v", line, err)
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
	if _, err := io.ReadFull(reader, b); err != nil || string(b) != "hello" {
		t.Fatalf("native relay to non-whitelisted DNS host failed: %q %v", b, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = reader.ReadByte(); err == nil {
		t.Fatal("proxy stream remained open after shutdown")
	}
}

type browserLocalRecorder struct {
	h    http.Header
	code int
}

func (w *browserLocalRecorder) Header() http.Header         { return w.h }
func (w *browserLocalRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (w *browserLocalRecorder) WriteHeader(code int)        { w.code = code }
