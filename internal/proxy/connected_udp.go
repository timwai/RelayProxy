package proxy

import (
	"fmt"
	"net"
	"net/netip"
)

// WrapConnectedUDP exposes an already connected UDP socket as a PacketConn.
// WriteTo accepts its fixed peer (or nil) and rejects a different destination.
// The caller retains ownership if validation fails; otherwise Close closes conn.
func WrapConnectedUDP(conn *net.UDPConn) (net.PacketConn, error) {
	if conn == nil {
		return nil, fmt.Errorf("connected UDP socket is required")
	}
	peer, ok := conn.RemoteAddr().(*net.UDPAddr)
	if !ok || peer == nil || peer.Port < 1 || peer.Port > 65535 || !peer.AddrPort().IsValid() {
		return nil, fmt.Errorf("UDP socket must be connected to a valid destination")
	}
	return &connectedUDPConn{UDPConn: conn, peer: normalizedUDPAddr(peer)}, nil
}

type connectedUDPConn struct {
	*net.UDPConn
	peer netip.AddrPort
}

func (c *connectedUDPConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	if addr != nil {
		target, ok := addr.(*net.UDPAddr)
		if !ok || target == nil || target.Port < 1 || target.Port > 65535 || target.Zone != c.peer.Addr().Zone() || normalizedUDPAddr(target) != c.peer {
			return 0, &net.OpError{Op: "write", Net: "udp", Source: c.LocalAddr(), Addr: addr,
				Err: net.InvalidAddrError("destination differs from the connected UDP peer")}
		}
	}
	// A connected UDP socket rejects WriteTo even for its original peer. Write
	// preserves that kernel-selected destination without a second DNS lookup.
	return c.UDPConn.Write(payload)
}

func normalizedUDPAddr(addr *net.UDPAddr) netip.AddrPort {
	p := addr.AddrPort()
	return netip.AddrPortFrom(p.Addr().Unmap(), p.Port())
}
