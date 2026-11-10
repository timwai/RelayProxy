// Package netpath observes established transports without changing routing.
package netpath

import (
	"net"
	"net/netip"
	"strings"

	"relayproxy/internal/protocol"
)

type interfaceAddress struct {
	name   string
	flags  net.Flags
	prefix netip.Prefix
}

func Basic(path string) protocol.ConnectionPath {
	p := protocol.ConnectionPath{Path: path, Network: "unknown"}
	switch {
	case strings.HasSuffix(path, "quic"):
		p.Transport = "quic"
	case strings.HasSuffix(path, "tls"):
		p.Transport = "tls"
	case path == "tcp_p2p":
		p.Transport = "tcp"
	case path == "udp_p2p":
		p.Transport = "udp"
	}
	return p
}

func From(value any, fallback string) protocol.ConnectionPath {
	if source, ok := value.(protocol.ConnectionPathProvider); ok {
		if p := source.ConnectionPath(); p.Path != "" {
			return p
		}
	}
	return Basic(fallback)
}

// Revalidate avoids retaining a LAN/interface assertion after QUIC rebinding.
// It uses only socket metadata; classification happens again on the next dial.
func Revalidate(p protocol.ConnectionPath, local, remote net.Addr) protocol.ConnectionPath {
	if p.Path == "" {
		return p
	}
	l, r := addrPort(local), addrPort(remote)
	oldLocal, _ := netip.ParseAddrPort(p.LocalAddress)
	oldRemote, _ := netip.ParseAddrPort(p.RemoteAddress)
	localChanged := l.IsValid() && oldLocal.IsValid() && l != oldLocal &&
		!(l.Addr().IsUnspecified() && l.Port() == oldLocal.Port())
	remoteChanged := r.IsValid() && oldRemote.IsValid() && r != oldRemote
	if localChanged || remoteChanged {
		p.Network, p.Interface, p.CandidateType = "unknown", "", ""
		if local != nil {
			p.LocalAddress = local.String()
		}
		if remote != nil {
			p.RemoteAddress = remote.String()
		}
	}
	return p
}

// Describe is called once when a transport is accepted, not on traffic reads.
// For a wildcard UDP listener the kernel's current source selection supplies
// interface evidence. The temporary UDP socket sends no packets. Policy routes
// and unrecognized virtual adapters may remain unknown; this is not a topology
// or NAT-type detector.
func Describe(path string, local, remote net.Addr) protocol.ConnectionPath {
	p := Basic(path)
	if local != nil {
		p.LocalAddress = local.String()
	}
	if remote != nil {
		p.RemoteAddress = remote.String()
	}
	l, r := addrPort(local), addrPort(remote)
	if !r.IsValid() {
		return p
	}
	if !l.IsValid() || l.Addr().IsUnspecified() {
		if conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(r)); err == nil {
			selected := addrPort(conn.LocalAddr())
			_ = conn.Close()
			if l.IsValid() {
				selected = netip.AddrPortFrom(selected.Addr(), l.Port())
			}
			l = selected
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return p
	}
	var addresses []interfaceAddress
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			prefix, err := netip.ParsePrefix(a.String())
			if err == nil {
				addresses = append(addresses, interfaceAddress{iface.Name, iface.Flags, prefix})
			}
		}
	}
	p.Network, p.Interface = classify(l.Addr(), r.Addr(), addresses)
	if l.IsValid() {
		p.LocalAddress = l.String()
	}
	return p
}

func addrPort(addr net.Addr) netip.AddrPort {
	if addr == nil {
		return netip.AddrPort{}
	}
	p, _ := netip.ParseAddrPort(addr.String())
	if p.IsValid() {
		return netip.AddrPortFrom(p.Addr().Unmap(), p.Port())
	}
	return p
}

func classify(local, remote netip.Addr, addresses []interfaceAddress) (string, string) {
	local, remote = local.Unmap(), remote.Unmap()
	if !remote.IsValid() || remote.IsUnspecified() || remote.IsMulticast() {
		return "unknown", ""
	}
	if remote.IsLoopback() {
		return "loopback", ""
	}
	for _, a := range addresses {
		if a.prefix.Addr().Unmap() != local.WithZone("") {
			continue
		}
		if virtual(a.name, a.flags) {
			return "virtual", a.name
		}
		if a.prefix.Contains(remote.WithZone("")) {
			return "lan", a.name
		}
		if public(remote) {
			return "public", a.name
		}
		return "unknown", a.name
	}
	// Without interface evidence we cannot rule out an overlay/VPN route.
	return "unknown", ""
}

func public(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

func virtual(name string, flags net.Flags) bool {
	if flags&net.FlagPointToPoint != 0 {
		return true
	}
	name = strings.ToLower(name)
	for _, marker := range []string{"utun", "tun", "tap", "wg", "tailscale", "zerotier", "zt", "wireguard", "vpn", "wintun", "vethernet", "docker", "veth", "vmnet", "virbr"} {
		if strings.HasPrefix(name, marker) || strings.Contains(name, "vpn") {
			return true
		}
	}
	return false
}

// MatchCandidate describes the selected endpoint, not the candidate inventory.
func MatchCandidate(remote net.Addr, candidates []protocol.P2PCandidate) string {
	address := addrPort(remote)
	if !address.IsValid() {
		return ""
	}
	for _, c := range candidates {
		parsed, err := netip.ParseAddrPort(c.Address)
		if err != nil || netip.AddrPortFrom(parsed.Addr().Unmap(), parsed.Port()) != address {
			continue
		}
		switch c.Type {
		case "lan":
			return "host"
		case "reflexive":
			return "reflexive"
		}
	}
	return "peer_reflexive"
}
