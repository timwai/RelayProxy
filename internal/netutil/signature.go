package netutil

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// CurrentNetworkSignature returns a stable, non-reversible signature of the
// currently usable non-loopback interface addresses. It is intended only for
// detecting local network changes; raw addresses never leave the process.
func CurrentNetworkSignature() string {
	interfaces, _ := net.Interfaces()
	values := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, raw := range addrs {
			var ip netip.Addr
			switch value := raw.(type) {
			case *net.IPNet:
				if parsed, ok := netip.AddrFromSlice(value.IP); ok {
					ip = parsed.Unmap()
				}
			case *net.IPAddr:
				if parsed, ok := netip.AddrFromSlice(value.IP); ok {
					ip = parsed.Unmap()
				}
			}
			if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
				continue
			}
			values = append(values, iface.Name+"="+ip.String())
		}
	}
	sort.Strings(values)
	sum := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(sum[:16])
}
