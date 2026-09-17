package client

import (
	"net"
	"relayproxy/internal/tunnel"
)

type udpTunnelConn = tunnel.UDPStreamConn

func newUDPTunnelConn(stream tunnel.DeadlineStream, remote net.Addr) *udpTunnelConn {
	return tunnel.NewUDPStreamConn(stream, remote)
}
