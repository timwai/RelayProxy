package proxy

import (
	"io"
	"net"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxConnections   = 256
	DefaultIdleTimeout      = 5 * time.Minute
	DefaultHandshakeTimeout = 15 * time.Second
	DialTimeout             = 15 * time.Second
)

// ReaderConn preserves bytes prefetched by a protocol parser and the original
// connection's deadlines and half-close behavior.
type ReaderConn struct {
	net.Conn
	Reader io.Reader
}

func (c *ReaderConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }
func (c *ReaderConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// IdleConn protects HTTP request-body forwarding before the bidirectional
// tunnel pumps take over. Enable IdleTimeout only after parsing the headers.
type IdleConn struct {
	net.Conn
	IdleTimeout time.Duration
	nextRefresh atomic.Int64
}

func (c *IdleConn) refresh() {
	if c.IdleTimeout <= 0 {
		return
	}
	now := time.Now()
	next := c.nextRefresh.Load()
	if now.UnixNano() < next {
		return
	}
	interval := min(time.Second, c.IdleTimeout/4)
	if c.nextRefresh.CompareAndSwap(next, now.Add(interval).UnixNano()) {
		_ = c.Conn.SetDeadline(now.Add(c.IdleTimeout))
	}
}

func (c *IdleConn) Read(p []byte) (int, error) {
	c.refresh()
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.refresh()
	}
	return n, err
}

func (c *IdleConn) Write(p []byte) (int, error) {
	c.refresh()
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.refresh()
	}
	return n, err
}

// Preserve streaming writes when http.Request.Write's bufio.Writer delegates
// body copying. Without ReaderFrom it can buffer a small body chunk while the
// next chunk is still being uploaded by the client.
func (c *IdleConn) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{c}, r)
}
