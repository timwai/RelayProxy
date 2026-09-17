package traffic

import (
	"errors"
	"net"
	"sync"
)

type conn struct {
	net.Conn
	record   *Record
	once     sync.Once
	closeErr error
}

func WrapConn(c net.Conn, record *Record) net.Conn {
	if record == nil {
		return c
	}
	record.Activate()
	return &conn{Conn: c, record: record}
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.record.AddDownload(n)
	return n, err
}
func (c *conn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.record.AddUpload(n)
	return n, err
}
func (c *conn) Close() error {
	c.once.Do(func() { c.closeErr = c.Conn.Close(); c.record.Finish("closed", nil) })
	return c.closeErr
}
func (c *conn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errors.New("connection does not support TCP half-close")
}
func (c *conn) CloseRead() error {
	if half, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return half.CloseRead()
	}
	return errors.New("connection does not support TCP half-close")
}

type packetConn struct {
	net.PacketConn
	record   *Record
	once     sync.Once
	closeErr error
}

func WrapPacketConn(pc net.PacketConn, record *Record) net.PacketConn {
	if record == nil {
		return pc
	}
	record.Activate()
	wrapped := &packetConn{PacketConn: pc, record: record}
	if mode, ok := pc.(interface{ UDPMode() string }); ok {
		return &modePacketConn{packetConn: wrapped, mode: mode}
	}
	return wrapped
}

type modePacketConn struct {
	*packetConn
	mode interface{ UDPMode() string }
}

func (c *modePacketConn) UDPMode() string { return c.mode.UDPMode() }
func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.record.AddDownload(n)
	return n, addr, err
}
func (c *packetConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	c.record.AddUpload(n)
	return n, err
}
func (c *packetConn) Close() error {
	c.once.Do(func() { c.closeErr = c.PacketConn.Close(); c.record.Finish("closed", nil) })
	return c.closeErr
}
