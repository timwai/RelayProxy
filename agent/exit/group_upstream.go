package exit

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"relayproxy/internal/proxy"
	"strconv"
	"time"
)

// A connected PacketConn adapter keeps the final destination fixed for the
// existing UDP stream pump, including its deadline and teardown semantics.
type upstreamPacketConn struct {
	net.PacketConn
	target netip.AddrPort
}

func (c *upstreamPacketConn) Read(b []byte) (int, error) { n, _, e := c.ReadFrom(b); return n, e }
func (c *upstreamPacketConn) Write(b []byte) (int, error) {
	return c.WriteTo(b, net.UDPAddrFromAddrPort(c.target))
}
func (c *upstreamPacketConn) RemoteAddr() net.Addr          { return net.UDPAddrFromAddrPort(c.target) }
func (c *upstreamPacketConn) SetDeadline(t time.Time) error { return c.PacketConn.SetDeadline(t) }

// groupTCP keeps the same multi-address fallback as the SOCKS5/HTTP upstream
// modes: one unreachable permitted address must not fail the whole request.
func groupTCP(ctx context.Context, d proxy.TunnelDialer, id string, ips []net.IP, port uint16) (net.Conn, string, error) {
	if len(ips) == 0 {
		return nil, "", errors.New("no permitted target address")
	}
	c, ip, e := dialTCPIPsWith(ctx, ips, func(attemptCtx context.Context, ip net.IP) (net.Conn, error) {
		return d.DialTCP(attemptCtx, id, ip.String(), port)
	})
	if e != nil {
		return nil, "", e
	}
	return c, net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), nil
}
