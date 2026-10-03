package client

import (
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// tcpPathConn preserves the stream adapter's half-close support while exposing
// the session actually chosen for this connection, not current P2P readiness.
type tcpPathConn struct {
	*tunnel.NetConnAdapter
	path string
}

func (c *tcpPathConn) ProxyPath() string { return c.path }

func tcpSessionPath(session tunnel.TunnelSession, direct bool) string {
	if direct {
		return protocol.P2PPathDirectQUIC
	}
	switch session.Transport() {
	case tunnel.TransportQUIC:
		return protocol.P2PPathRelayQUIC
	case tunnel.TransportTLS:
		return protocol.P2PPathRelayTLS
	default:
		return ""
	}
}
