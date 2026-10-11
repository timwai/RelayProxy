package app

import (
	"bufio"
	"context"
	"encoding/base64"
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
	if host != "login.example.com" || port != 443 {
		return nil, fmt.Errorf("wrong target")
	}
	c, other := net.Pipe()
	go func() { defer other.Close(); _, _ = io.Copy(other, other) }()
	return c, nil
}
func TestBrowserLocalProxyGuardsAndTunnel(t *testing.T) {
	token := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGH"
	p := newBrowserLocalProxy("127.0.0.1:0", token, []string{"login.example.com"}, browserEchoDialer{})
	encoded := base64.StdEncoding.EncodeToString([]byte("relay-browser:" + token))
	for _, tt := range []struct {
		method, host, auth string
		want               int
	}{
		{"GET", "login.example.com:443", encoded, 405},
		{"CONNECT", "login.example.com:443", "", 407},
		{"CONNECT", "login.example.com:443", base64.StdEncoding.EncodeToString([]byte("relay-browser:wrong")), 407},
		{"CONNECT", "127.0.0.1:443", encoded, 403},
		{"CONNECT", "other.example.com:443", encoded, 403},
	} {
		r, _ := http.NewRequest(tt.method, "http://127.0.0.1/", nil)
		r.Host = tt.host
		if tt.auth != "" {
			r.Header.Set("Proxy-Authorization", "Basic "+tt.auth)
		}
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
	p = newBrowserLocalProxy(fmt.Sprintf("127.0.0.1:%d", port), token, []string{"login.example.com"}, browserEchoDialer{})
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
	_, _ = fmt.Fprintf(c, "CONNECT login.example.com:443 HTTP/1.1\r\nHost: login.example.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", encoded)
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
		t.Fatalf("native relay failure %q %v", b, err)
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
