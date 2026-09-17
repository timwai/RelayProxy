package divert

import (
	"net"
	"strconv"
	"strings"
)

// LoopGuard returns true if the destination must be DIRECT to avoid proxy loops.
type LoopGuard struct {
	SelfNames  []string // process basenames always excluded
	SelfPID    uint32
	RelayHost  string
	RelayIPs   []string // resolved by a verified adapter before interception
	RelayPorts []int
	LocalProxy []string // "127.0.0.1:1080" style listen addrs
	LocalIPs   []string // local interface addresses for wildcard listeners
}

func (g LoopGuard) MustDirectFlow(flow Flow) bool {
	if g.SelfPID != 0 && flow.ProcessID == g.SelfPID {
		return true
	}
	return g.MustDirect(flow.Process, flow.IP, flow.Port) ||
		(flow.Host != "" && g.MustDirect(flow.Process, flow.Host, flow.Port))
}

// MustDirect reports whether dst must bypass divert PROXY.
func (g LoopGuard) MustDirect(process, dstHost string, dstPort uint16) bool {
	for _, n := range g.SelfNames {
		if matchProcess(n, process) {
			return true
		}
	}
	host := strings.TrimSpace(dstHost)
	relay := g.RelayHost != "" && sameHost(host, g.RelayHost)
	for _, ip := range g.RelayIPs {
		relay = relay || sameHost(host, ip)
	}
	if relay {
		for _, p := range g.RelayPorts {
			if int(dstPort) == p {
				return true
			}
		}
	}
	dst := net.JoinHostPort(host, strconv.Itoa(int(dstPort)))
	for _, lp := range g.LocalProxy {
		if strings.EqualFold(strings.TrimSpace(lp), dst) {
			return true
		}
		// Also compare host:port when lp is already host:port
		if h, p, err := net.SplitHostPort(lp); err == nil {
			matches := sameHost(h, host)
			if listenIP := net.ParseIP(h); h == "" || (listenIP != nil && listenIP.IsUnspecified()) || strings.EqualFold(h, "localhost") {
				destination := net.ParseIP(host)
				matches = destination != nil && destination.IsLoopback()
				if !strings.EqualFold(h, "localhost") {
					for _, localIP := range g.LocalIPs {
						matches = matches || sameHost(localIP, host)
					}
				}
			}
			if matches {
				if pi, err2 := strconv.Atoi(p); err2 == nil && pi == int(dstPort) {
					return true
				}
			}
		}
	}
	return false
}

func sameHost(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if ai, bi := net.ParseIP(a), net.ParseIP(b); ai != nil && bi != nil {
		return ai.Equal(bi)
	}
	return strings.EqualFold(a, b)
}
