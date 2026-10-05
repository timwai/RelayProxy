package client

import (
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// tcpPathConn preserves the stream adapter's half-close support while exposing
// the concrete path chosen for this connection.
type tcpPathConn struct {
	*tunnel.NetConnAdapter
	path string
}

func (c *tcpPathConn) ProxyPath() string { return c.path }

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

// tcpSessionPath is kept for package-level compatibility with existing tests
// and helpers. New path-aware code should pass the selected ProxyPath directly.
func tcpSessionPath(session tunnel.TunnelSession, direct bool) string {
	if direct {
		return protocol.ProxyPathP2PQUIC.String()
	}
	return relaySessionPath(session).String()
}
