package client

import (
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// tcpPathConn preserves the stream adapter's half-close support while exposing
// the concrete path selected for this connection.
type tcpPathConn struct {
	*tunnel.NetConnAdapter
	path protocol.ProxyPath
}

func (c *tcpPathConn) ProxyPath() string { return c.path.String() }

func relaySessionPath(session tunnel.TunnelSession) protocol.ProxyPath {
	if session == nil {
		return ""
	}
	switch session.Transport() {
	case tunnel.TransportQUIC:
		return protocol.ProxyPathRelayQUIC
	case tunnel.TransportTLS:
		return protocol.ProxyPathRelayTLS
	default:
		return ""
	}
}
