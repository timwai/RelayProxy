package proxy

import (
	"context"
	"net"
	"net/netip"
)

type ClientInfo struct {
	Entry          string
	Source         netip.AddrPort
	Local          netip.AddrPort
	Process        string
	ProcessAliases []string
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

// WithClientProcess attaches process identity supplied by a trusted local
// transport, such as the authenticated Android VPN SOCKS entry.
func WithClientProcess(ctx context.Context, process string, aliases []string) context.Context {
	info := ClientFromContext(ctx)
	info.Process = process
	info.ProcessAliases = append([]string(nil), aliases...)
	return context.WithValue(ctx, clientInfoKey{}, info)
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
