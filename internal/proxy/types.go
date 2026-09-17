package proxy

import (
	"context"
	"net"
)

// TunnelDialer defines the interface for creating connections through the Relay tunnel.
type TunnelDialer interface {
	DialTCP(ctx context.Context, exitNodeID string, host string, port uint16) (net.Conn, error)
	DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error)
}

// UDPDialOptions expresses requirements for the entire two-hop association.
type UDPDialOptions struct {
	DatagramRequired bool
}

type UDPOptionsDialer interface {
	DialUDPWithOptions(ctx context.Context, exitNodeID string, host string, port uint16, options UDPDialOptions) (net.PacketConn, error)
}
