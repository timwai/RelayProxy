package proxy

import (
	"context"
	"net"
	"net/netip"
)

type ClientInfo struct {
	Entry  string
	Source netip.AddrPort
	Local  netip.AddrPort
}
type clientInfoKey struct{}

// WithClientConn records the accepted local socket tuple, not the requested
// Internet destination. OS owner tables identify the process using this tuple.
func WithClientConn(ctx context.Context, entry string, conn net.Conn) context.Context {
	info := ClientInfo{Entry: entry}
	if conn != nil {
		info.Source = addrPort(conn.RemoteAddr())
		info.Local = addrPort(conn.LocalAddr())
	}
	return context.WithValue(ctx, clientInfoKey{}, info)
}

func ClientFromContext(ctx context.Context) ClientInfo {
	info, _ := ctx.Value(clientInfoKey{}).(ClientInfo)
	return info
}

func addrPort(addr net.Addr) netip.AddrPort {
	if addr == nil {
		return netip.AddrPort{}
	}
	p, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(p.Addr().Unmap().WithZone(""), p.Port())
}
