package androidcore

import (
	"context"
	"net"
	"net/netip"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
)

var androidVPNFakeIPPrefix = netip.MustParsePrefix("198.18.0.0/15")

// vpnMappedDNSGuardDialer is used only by the Android VPN's private SOCKS
// listener. hev-socks5-tunnel normally translates mapped DNS addresses back to
// hostnames before they reach this layer. If its mapping was lost (for example
// after a TUN restart while an app still caches an old FakeIP), fail locally
// instead of forwarding RFC 2544 benchmark addresses to the selected exit.
type vpnMappedDNSGuardDialer struct {
	base proxy.TunnelDialer
}

func rejectAndroidVPNFakeIP(host string) error {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	if !androidVPNFakeIPPrefix.Contains(addr) {
		return nil
	}
	return protocol.NewRelayError(
		protocol.ErrCodeHostUnreach,
		"Android VPN mapped-DNS address has no live hostname mapping; re-resolve the destination",
	)
}

func (d *vpnMappedDNSGuardDialer) DialTCP(ctx context.Context, exitID, host string, port uint16) (net.Conn, error) {
	if err := rejectAndroidVPNFakeIP(host); err != nil {
		return nil, err
	}
	return d.base.DialTCP(ctx, exitID, host, port)
}

func (d *vpnMappedDNSGuardDialer) DialUDP(ctx context.Context, exitID, host string, port uint16) (net.PacketConn, error) {
	return d.dialUDP(ctx, exitID, host, port, proxy.UDPDialOptions{})
}

func (d *vpnMappedDNSGuardDialer) DialUDPWithOptions(ctx context.Context, exitID, host string, port uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	return d.dialUDP(ctx, exitID, host, port, options)
}

func (d *vpnMappedDNSGuardDialer) dialUDP(ctx context.Context, exitID, host string, port uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	if err := rejectAndroidVPNFakeIP(host); err != nil {
		return nil, err
	}
	// Android Cronet / Google services aggressively bootstrap HTTP/3 on
	// UDP/443. RelayProxy native UDP fragments at 1100 bytes, so a >=1200-byte
	// QUIC Initial would otherwise become multiple independently lossy outer
	// QUIC DATAGRAM frames. Prefer the reliable framed UDP stream for ordinary
	// Android VPN UDP/443. Explicit DatagramRequired routing still wins.
	if port == 443 && !options.DatagramRequired {
		options.PreferStream = true
	}
	if optional, ok := d.base.(proxy.UDPOptionsDialer); ok {
		return optional.DialUDPWithOptions(ctx, exitID, host, port, options)
	}
	return d.base.DialUDP(ctx, exitID, host, port)
}
