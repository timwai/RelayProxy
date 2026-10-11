package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dedicated 127.0.0.1 Chrome entry, independent of ordinary SOCKS/HTTP and
// transparent interception. CONNECT is the Chrome-facing adapter only; the
// target is dialed by RelayProxy's native routing engine and QUIC/P2P paths.
type browserLocalDialer interface {
	DialBrowserNetworkTCP(context.Context, string, uint16) (net.Conn, error)
}
type browserLocalProxy struct {
	listen string
	secret [32]byte
	hosts  map[string]bool
	dialer browserLocalDialer
	mu     sync.Mutex
	closed bool
	server *http.Server
	active map[net.Conn]struct{}
	slots  chan struct{}
}

var localBrowserDNS = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dedicated 127.0.0.1 Chrome entry, independent of ordinary SOCKS/HTTP and
)

func localBrowserAllowedHost(host string) bool {
	if len(host) > 253 || host != strings.ToLower(host) || !localBrowserDNS.MatchString(host) || net.ParseIP(host) != nil {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) > 63 {
			return false
		}
	}
	for _, end := range []string{".local", ".internal", ".localhost", ".arpa"} {
		if strings.HasSuffix(host, end) {
			return false
		}
	}
	return true
}
func newBrowserLocalProxy(listen, token string, allowed []string, dialer browserLocalDialer) *browserLocalProxy {
	hosts := make(map[string]bool, len(allowed))
	for _, host := range allowed {
		hosts[host] = true
	}
	return &browserLocalProxy{listen: listen, secret: sha256.Sum256([]byte(token)), hosts: hosts,
		dialer: dialer, active: make(map[net.Conn]struct{}), slots: make(chan struct{}, 64)}
}
func (p *browserLocalProxy) Start() error {
	host, port, err := net.SplitHostPort(p.listen)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("browser bridge must bind 127.0.0.1")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 {
		return fmt.Errorf("browser bridge port invalid")
	}
	if len(p.hosts) == 0 || p.dialer == nil {
		return fmt.Errorf("browser bridge requires allowed hosts and native routing dialer")
	}
	ln, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 8 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	srv := p.server
	p.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}
func (p *browserLocalProxy) validAuth(v string) bool {
	if !strings.HasPrefix(v, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v[6:]))
	if err != nil || len(raw) > 192 {
		return false
	}
	user, token, ok := strings.Cut(string(raw), ":")
	if !ok || user != "relay-browser" {
		return false
	}
	hash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(hash[:], p.secret[:]) == 1
}
func (p *browserLocalProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.active[c] = struct{}{}
	return true
}
func (p *browserLocalProxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.active, c)
	p.mu.Unlock()
	_ = c.Close()
}
func (p *browserLocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodConnect || r.ProtoMajor != 1 {
		http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
		return
	}
	if !p.validAuth(r.Header.Get("Proxy-Authorization")) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="RelayProxy Browser Sync local Agent"`)
		http.Error(w, "proxy authorization required", http.StatusProxyAuthRequired)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" || !localBrowserAllowedHost(host) || !p.hosts[host] ||
		r.URL == nil || (r.URL.Host != "" && r.URL.Host != r.Host) {
		http.Error(w, "target forbidden", http.StatusForbidden)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "too many streams", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	out, err := p.dialer.DialBrowserNetworkTCP(ctx, host, 443)
	cancel()
	if err != nil || out == nil {
		if out != nil {
			_ = out.Close()
		}
		http.Error(w, "native Agent route unavailable", http.StatusBadGateway)
		return
	}
	defer out.Close()
	if !p.track(out) {
		http.Error(w, "bridge closed", http.StatusServiceUnavailable)
		return
	}
	defer p.untrack(out)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if !p.track(client) {
		return
	}
	defer p.untrack(client)
	_ = buffer.Flush()
	if _, err = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Now().Add(30 * time.Minute))
	_ = out.SetDeadline(time.Now().Add(30 * time.Minute))
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, buffer); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, out); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = out.Close()
	<-done
}
func (p *browserLocalProxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	srv := p.server
	active := make([]net.Conn, 0, len(p.active))
	for c := range p.active {
		active = append(active, c)
	}
	p.mu.Unlock()
	for _, c := range active {
		_ = c.Close()
	}
	if srv != nil {
		return srv.Close()
	}
	return nil
}
