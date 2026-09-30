package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
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

type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
}

func (w *bufferedResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func serveSharedHTTPConnection(ctx context.Context, conn net.Conn, handler HTTPHandler) error {
	if handler == nil {
		return fmt.Errorf("public HTTP handler is not configured")
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReaderSize(conn, 32<<10)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	defer req.Body.Close()
	req.RemoteAddr = conn.RemoteAddr().String()
	req = req.WithContext(ctx)
	if tlsConn := underlyingTLSConn(conn); tlsConn != nil {
		state := tlsConn.ConnectionState()
		req.TLS = &state
	}

	writer := newBufferedResponseWriter()
	req.Body = http.MaxBytesReader(writer, req.Body, 64<<10)
	handler.ServeHTTP(writer, req)

	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	header := writer.header.Clone()
	header.Set("Connection", "close")
	response := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(writer.body.Bytes())),
		ContentLength: int64(writer.body.Len()),
		Close:         true,
		Request:       req,
	}
	return response.Write(conn)
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
