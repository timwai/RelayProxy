package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/internal/proxy"
)

// browserLocalProxy is a dedicated, opt-in loopback adapter for Chrome.
// It is not a separate routing policy: HTTP and CONNECT (all TCP ports,
// DNS names and IP literals) use the Agent's existing routing engine.
// No local proxy authentication or destination allowlist is applied.
type browserLocalDialer interface {
	DialBrowserNetworkTCP(context.Context, string, uint16) (net.Conn, error)
}

type browserLocalProxy struct {
	listen string
	dialer browserLocalDialer
	mu     sync.Mutex
	closed bool
	server *http.Server
	active map[net.Conn]struct{}
	slots  chan struct{}
}

func newBrowserLocalProxy(listen string, dialer browserLocalDialer) *browserLocalProxy {
	return &browserLocalProxy{
		listen: listen, dialer: dialer, active: make(map[net.Conn]struct{}),
		slots: make(chan struct{}, 64),
	}
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
	if p.dialer == nil {
		return fmt.Errorf("browser bridge requires native routing dialer")
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
	p.server = &http.Server{
		Handler: p, ReadHeaderTimeout: 8 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192,
		// Preserve the accepted socket tuple so native Agent process-based
		// routing can identify Chrome just as with SOCKS5/HTTP entry points.
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return proxy.WithClientConn(ctx, "browser_chrome", conn)
		},
	}
	srv := p.server
	p.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// A destination can be a DNS name (including local hostnames), IPv4 or IPv6.
// Do not impose a browser-specific policy on top of Agent routing/exit ACL.
func browserDestination(authority string, defaultPort uint16) (string, uint16, error) {
	if authority == "" || strings.ContainsAny(authority, " \t\r\n/@\\") {
		return "", 0, fmt.Errorf("invalid destination authority")
	}
	host, portString, err := net.SplitHostPort(authority)
	if err != nil {
		// URL.Host omits the default port for plain HTTP requests.
		if defaultPort == 0 {
			return "", 0, fmt.Errorf("destination requires a port")
		}
		if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
		} else if strings.Contains(authority, ":") && net.ParseIP(authority) == nil {
			return "", 0, fmt.Errorf("invalid destination port")
		} else {
			host = authority
		}
		return host, defaultPort, nil
	}
	if host == "" {
		return "", 0, fmt.Errorf("empty destination host")
	}
	port, err := strconv.ParseUint(portString, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("invalid destination port")
	}
	return host, uint16(port), nil
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

type browserTrackedConn struct {
	net.Conn
	proxy *browserLocalProxy
}

func (c *browserTrackedConn) Close() error {
	c.proxy.untrack(c.Conn)
	return nil
}
func (p *browserLocalProxy) serveForwardHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Scheme != "http" || r.URL.Host == "" ||
		r.URL.User != nil {
		http.Error(w, "absolute HTTP request required", http.StatusBadRequest)
		return
	}
	_, _, err := browserDestination(r.URL.Host, 80)
	if err != nil {
		http.Error(w, "invalid HTTP destination", http.StatusBadRequest)
		return
	}
	// ReverseProxy handles HTTP/1.1 bodies, redirects, chunking and WebSocket
	// Upgrade. Its transport delegates every new TCP dial to Agent routing.
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := browserDestination(address, 0)
			if err != nil {
				return nil, err
			}
			conn, err := p.dialer.DialBrowserNetworkTCP(ctx, host, port)
			if err != nil {
				return nil, err
			}
			if !p.track(conn) {
				_ = conn.Close()
				return nil, net.ErrClosed
			}
			return &browserTrackedConn{Conn: conn, proxy: p}, nil
		},
	}
	defer transport.CloseIdleConnections()
	rev := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = newURLCopy(pr.In.URL)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("Proxy-Connection")
			// Never add X-Forwarded-For: the browser's local address
			// is irrelevant to the website and must not be leaked.
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Agent route unavailable", http.StatusBadGateway)
		},
	}
	rev.ServeHTTP(w, r)
}

// ReverseProxy rewrites the output request, not the browser's original.
func newURLCopy(u *url.URL) *url.URL {
	copy := *u
	return &copy
}

func (p *browserLocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.ProtoMajor != 1 {
		http.Error(w, "unsupported HTTP version", http.StatusHTTPVersionNotSupported)
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "too many streams", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodConnect {
		p.serveForwardHTTP(w, r)
		return
	}
	host, port, err := browserDestination(r.Host, 0)
	if err != nil || r.URL == nil || (r.URL.Host != "" && r.URL.Host != r.Host) {
		http.Error(w, "invalid CONNECT destination", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	out, err := p.dialer.DialBrowserNetworkTCP(ctx, host, port)
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
