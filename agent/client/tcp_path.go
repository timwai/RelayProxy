package client

import (
	"net"

	"relayproxy/internal/netpath"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// tcpPathConn preserves the stream adapter's half-close support while exposing
// the concrete path selected for this connection.
type tcpPathConn struct {
	*tunnel.NetConnAdapter
	path   protocol.ProxyPath
	info   protocol.ConnectionPath
	source protocol.ConnectionPathProvider
}

func (c *tcpPathConn) ProxyPath() string { return c.path.String() }

func (c *tcpPathConn) ConnectionPath() protocol.ConnectionPath {
	return currentPathInfo(c.source, c.info)
}

func currentPathInfo(source protocol.ConnectionPathProvider, info protocol.ConnectionPath) protocol.ConnectionPath {
	if source != nil {
		if live := source.ConnectionPath(); live.Path == info.Path {
			return live
		}
	}
	return info
}
func pathSource(session tunnel.TunnelSession) protocol.ConnectionPathProvider {
	source, _ := session.(protocol.ConnectionPathProvider)
	return source
}

func sessionPathInfo(session tunnel.TunnelSession, path protocol.ProxyPath) protocol.ConnectionPath {
	if source, ok := session.(protocol.ConnectionPathProvider); ok {
		if p := source.ConnectionPath(); p.Path == path.String() {
			return p
		}
	}
	info := netpath.Describe(path.String(), session.LocalAddr(), session.RemoteAddr())
	if s, ok := session.(interface{ SetConnectionPath(protocol.ConnectionPath) }); ok {
		s.SetConnectionPath(info)
	}
	return info
}

type packetPathConn struct {
	net.PacketConn
	info   protocol.ConnectionPath
	source protocol.ConnectionPathProvider
}

func (c *packetPathConn) ProxyPath() string { return c.info.Path }
func (c *packetPathConn) ConnectionPath() protocol.ConnectionPath {
	return currentPathInfo(c.source, c.info)
}
func (c *packetPathConn) UDPMode() string {
	if m, ok := c.PacketConn.(interface{ UDPMode() string }); ok {
		return m.UDPMode()
	}
	return ""
}

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
