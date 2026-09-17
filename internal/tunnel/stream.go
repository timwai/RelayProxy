package tunnel

import (
	"io"
	"net"
	"time"
)

// TunnelStream represents an individual multiplexed stream within a TunnelSession.
// It implements io.Reader, io.Writer, and io.Closer, and supports half-close via CloseWrite.
type TunnelStream interface {
	io.Reader
	io.Writer
	io.Closer

	// CloseWrite shuts down the writing side of the stream (sending FIN/EOF)
	CloseWrite() error

	// SetDeadline sets the read and write deadlines
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// NetConnAdapter wraps a TunnelStream and addresses to fulfill the standard net.Conn interface
type NetConnAdapter struct {
	Stream        TunnelStream
	LocalAddrVal  net.Addr
	RemoteAddrVal net.Addr
}

func NewNetConnAdapter(s TunnelStream, local, remote net.Addr) net.Conn {
	return &NetConnAdapter{
		Stream:        s,
		LocalAddrVal:  local,
		RemoteAddrVal: remote,
	}
}

func (c *NetConnAdapter) Read(b []byte) (n int, err error) {
	return c.Stream.Read(b)
}

func (c *NetConnAdapter) Write(b []byte) (n int, err error) {
	return c.Stream.Write(b)
}

func (c *NetConnAdapter) Close() error {
	return c.Stream.Close()
}

// CloseWrite forwards half-close to the underlying TunnelStream (P2-1).
func (c *NetConnAdapter) CloseWrite() error {
	return c.Stream.CloseWrite()
}

func (c *NetConnAdapter) LocalAddr() net.Addr {
	if c.LocalAddrVal != nil {
		return c.LocalAddrVal
	}
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (c *NetConnAdapter) RemoteAddr() net.Addr {
	if c.RemoteAddrVal != nil {
		return c.RemoteAddrVal
	}
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (c *NetConnAdapter) SetDeadline(t time.Time) error {
	return c.Stream.SetDeadline(t)
}

func (c *NetConnAdapter) SetReadDeadline(t time.Time) error {
	return c.Stream.SetReadDeadline(t)
}

func (c *NetConnAdapter) SetWriteDeadline(t time.Time) error {
	return c.Stream.SetWriteDeadline(t)
}
