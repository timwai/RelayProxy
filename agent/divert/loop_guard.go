package divert

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// prepareLoopGuard snapshots local addresses and resolves the relay before any
// interception rules are installed. Both Windows and Linux use the result to
// keep the agent's own tunnel and local-only traffic outside the proxy path.
func prepareLoopGuard(s *Server) error {
	guard := s.guard
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
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", guard.RelayHost)
		cancel()
		if err != nil {
			return fmt.Errorf("启用透明代理前解析中继服务器失败: %w", err)
		}
		for _, ip := range ips {
			guard.RelayIPs = appendUnique(guard.RelayIPs, ip.Unmap().String())
		}
	}
	s.mu.Lock()
	s.guard = guard
	s.mu.Unlock()
	return nil
}
