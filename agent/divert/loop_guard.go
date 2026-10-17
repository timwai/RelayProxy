package divert

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// prepareLoopGuard snapshots local addresses and resolves the relay before any
// interception rules are installed. Both Windows and Linux use the result to
// keep the agent's own tunnel and local-only traffic outside the proxy path.
func prepareLoopGuard(s *Server) error {
	// Rebuild each snapshot from configuration; a reconnect may use a different
	// address than the previous DNS answer or than a fresh resolver lookup.
	guard := s.opts.Guard
	if s.opts.RelayIPs != nil {
		for _, raw := range s.opts.RelayIPs() {
			if ip, err := netip.ParseAddr(raw); err == nil && !ip.IsUnspecified() {
				guard.RelayIPs = appendUnique(guard.RelayIPs, ip.Unmap().String())
			}
		}
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return fmt.Errorf("读取本机网络地址失败: %w", err)
	}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil {
			guard.LocalIPs = appendUnique(guard.LocalIPs, prefix.Addr().Unmap().String())
		}
	}
	if guard.RelayHost != "" {
		ctx, cancel := context.WithTimeout(s.ctx, s.opts.DialTimeout)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", strings.TrimSuffix(guard.RelayHost, "."))
		cancel()
		if err != nil && len(guard.RelayIPs) == 0 {
			return fmt.Errorf("启用透明代理前解析中继服务器失败: %w", err)
		}
		for _, ip := range ips {
			guard.RelayIPs = appendUnique(guard.RelayIPs, ip.Unmap().String())
		}
	}
	s.guardMu.Lock()
	s.guard = guard
	s.guardMu.Unlock()
	return nil
}
