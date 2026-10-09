package exit

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// DialViaUpstreamTCP exposes the existing, tested upstream handshakes to Agent
// routing without sending a local custom exit through Relay or the shared
// exit handler. Domain targets are passed unchanged to the upstream.
func DialViaUpstreamTCP(ctx context.Context, upstream UpstreamConfig, host string, port uint16) (net.Conn, error) {
	if err := ValidateUpstreamConfig(upstream); err != nil {
		return nil, err
	}
	if upstream.Mode == UpstreamDirect {
		return nil, fmt.Errorf("custom exit cannot use DIRECT")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target := net.JoinHostPort(host, strconv.Itoa(int(port)))
	switch upstream.Mode {
	case UpstreamSOCKS5:
		return dialSOCKS5(ctx, upstream, 0x01, target)
	case UpstreamHTTP, UpstreamHTTPS:
		return dialHTTPConnect(ctx, upstream, target)
	default:
		return nil, fmt.Errorf("unsupported upstream mode %q", upstream.Mode)
	}
}

// DialViaUpstreamUDP requires a literal IP target. Refusing hostname targets
// is preferable to accidentally resolving them via the local DNS resolver.
func DialViaUpstreamUDP(ctx context.Context, upstream UpstreamConfig, host string, port uint16) (net.PacketConn, error) {
	if err := ValidateUpstreamConfig(upstream); err != nil {
		return nil, err
	}
	if upstream.Mode != UpstreamSOCKS5 {
		return nil, fmt.Errorf("custom %s proxy does not support UDP", upstream.Mode)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 UDP requires an IP target (no local DNS fallback): %s", host)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := dialSOCKS5UDP(ctx, upstream, netip.AddrPortFrom(addr.Unmap(), port))
	if err != nil {
		return nil, err
	}
	return &packetConnAdapter{Conn: conn}, nil
}

type packetConnAdapter struct{ net.Conn }

func (c *packetConnAdapter) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(p)
	return n, c.Conn.RemoteAddr(), err
}

func (c *packetConnAdapter) WriteTo(p []byte, addr net.Addr) (int, error) {
	if addr != nil && addr.String() != c.Conn.RemoteAddr().String() {
		return 0, fmt.Errorf("SOCKS5 UDP connection is bound to %s", c.Conn.RemoteAddr())
	}
	return c.Conn.Write(p)
}
