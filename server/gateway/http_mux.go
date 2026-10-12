package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"relayproxy/internal/tunnel"
)

func sharedTCPConfig(base *tls.Config, enableHTTP bool) *tls.Config {
	if base == nil {
		return nil
	}
	config := base.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	if !enableHTTP {
		return config
	}
	config.NextProtos = appendUniqueProtocol(config.NextProtos, tunnel.TCPALPN)
	config.NextProtos = appendUniqueProtocol(config.NextProtos, "http/1.1")
	return config
}

func appendUniqueProtocol(protocols []string, protocol string) []string {
	for _, current := range protocols {
		if current == protocol {
			return protocols
		}
	}
	return append(protocols, protocol)
}

// HTTPHandler is intentionally the small net/http surface the gateway needs.
// It keeps the public push API independent from the Admin Web listener.
type HTTPHandler interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func classifySharedTCP(conn net.Conn, negotiatedProtocol string, timeout time.Duration) (net.Conn, bool, error) {
	switch negotiatedProtocol {
	case tunnel.TCPALPN:
		return conn, false, nil
	case "http/1.1":
		return conn, true, nil
	}

	// Older RelayProxy agents do not advertise ALPN. yamux starts with a binary
	// frame header (version byte 0), whereas ordinary HTTP methods start with an
	// upper-case ASCII letter. Peeking one decrypted byte therefore preserves
	// backwards compatibility without consuming tunnel data.
	if timeout <= 0 || timeout > 3*time.Second {
		timeout = 3 * time.Second
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return conn, false, err
	}
	reader := bufio.NewReaderSize(conn, 4096)
	first, err := reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return conn, false, err
	}
	wrapped := &bufferedConn{Conn: conn, reader: reader}
	isHTTP := first[0] >= 'A' && first[0] <= 'Z'
	return wrapped, isHTTP, nil
}

// One already-negotiated TLS connection is passed into net/http. Unlike the
// previous buffered single-response path, this supports WebSocket Upgrade,
// authenticated CONNECT hijacking and continuous streams without exposing an
// independent HTTP listener.
type sharedHTTPConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *sharedHTTPConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

type singleHTTPListener struct {
	conn net.Conn
	once sync.Once
}

func (l *singleHTTPListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn == nil {
		return nil, net.ErrClosed
	}
	return conn, nil
}
func (l *singleHTTPListener) Close() error   { return nil }
func (l *singleHTTPListener) Addr() net.Addr { return l.conn.LocalAddr() }

// Restore the TLS state (when enabled) after protocol sniffing. The wrapper
// is not a *tls.Conn; never trust user-supplied X-Forwarded-Proto headers.
// Only the old public push API may be served over plaintext relay.
func serveSharedHTTPConnection(ctx context.Context, conn net.Conn, handler HTTPHandler) error {
	if handler == nil {
		return fmt.Errorf("public HTTP handler is not configured")
	}
	tlsConn := underlyingTLSConn(conn)
	var state *tls.ConnectionState
	if tlsConn != nil {
		verified := tlsConn.ConnectionState()
		state = &verified
	}
	observed := &sharedHTTPConn{Conn: conn, done: make(chan struct{})}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Old plaintext relay installations may still receive public
			// push requests. Browser Sync and CONNECT themselves require
			// TLS and reject r.TLS == nil at their own authorization gates.
			r.TLS = state
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	// net/http owns the HTTP framing and the ResponseWriter.Hijack
	// semantics. This Accept-once listener is not reachable outside the
	// gateway's authenticated TLS socket.
	err := server.Serve(&singleHTTPListener{conn: observed})
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		_ = observed.Close()
		return err
	}
	select {
	case <-observed.done:
	case <-ctx.Done():
		_ = observed.Close()
	}
	return nil
}

func underlyingTLSConn(conn net.Conn) *tls.Conn {
	switch value := conn.(type) {
	case *tls.Conn:
		return value
	case *bufferedConn:
		if tlsConn, ok := value.Conn.(*tls.Conn); ok {
			return tlsConn
		}
	}
	return nil
}
