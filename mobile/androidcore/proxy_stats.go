package androidcore

import (
	"context"
	"net"
	"sync"

	"relayproxy/internal/proxy"
)

type proxyStatsDialer struct {
	base  proxy.TunnelDialer
	owner *Client
}

func (d *proxyStatsDialer) DialTCP(ctx context.Context, exitID, host string, port uint16) (net.Conn, error) {
	conn, err := d.base.DialTCP(ctx, exitID, host, port)
	if err != nil {
		return nil, err
	}
	d.owner.proxyTCPFlows.Add(1)
	d.owner.proxyActiveTCP.Add(1)
	return &proxyStatsConn{Conn: conn, owner: d.owner}, nil
}

func (d *proxyStatsDialer) DialUDP(ctx context.Context, exitID, host string, port uint16) (net.PacketConn, error) {
	return d.dialUDP(ctx, exitID, host, port, proxy.UDPDialOptions{})
}

func (d *proxyStatsDialer) DialUDPWithOptions(ctx context.Context, exitID, host string, port uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	return d.dialUDP(ctx, exitID, host, port, options)
}

func (d *proxyStatsDialer) dialUDP(ctx context.Context, exitID, host string, port uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	var (
		conn net.PacketConn
		err  error
	)
	if optional, ok := d.base.(proxy.UDPOptionsDialer); ok {
		conn, err = optional.DialUDPWithOptions(ctx, exitID, host, port, options)
	} else {
		conn, err = d.base.DialUDP(ctx, exitID, host, port)
	}
	if err != nil {
		return nil, err
	}
	d.owner.proxyUDPFlows.Add(1)
	d.owner.proxyActiveUDP.Add(1)
	return &proxyStatsPacketConn{PacketConn: conn, owner: d.owner}, nil
}

type proxyStatsConn struct {
	net.Conn
	owner *Client
	once  sync.Once
}

func (c *proxyStatsConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.owner.proxyBytesDown.Add(uint64(n))
	return n, err
}

func (c *proxyStatsConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	c.owner.proxyBytesUp.Add(uint64(n))
	return n, err
}

func (c *proxyStatsConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.proxyActiveTCP.Add(-1) })
	return err
}

func (c *proxyStatsConn) CloseWrite() error {
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return c.Close()
}

type proxyStatsPacketConn struct {
	net.PacketConn
	owner *Client
	once  sync.Once
}

func (c *proxyStatsPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(buffer)
	c.owner.proxyBytesDown.Add(uint64(n))
	return n, addr, err
}

func (c *proxyStatsPacketConn) WriteTo(buffer []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(buffer, addr)
	c.owner.proxyBytesUp.Add(uint64(n))
	return n, err
}

func (c *proxyStatsPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() { c.owner.proxyActiveUDP.Add(-1) })
	return err
}
