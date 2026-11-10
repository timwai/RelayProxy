package protocol

import (
	"net/netip"
	"strings"
)

// ConnectionPath describes one authenticated data transport. Path retains the
// legacy wire value; Network is an observation, never an authorization signal.
// Empty/unknown fields are intentional when the platform cannot identify them.
type ConnectionPath struct {
	Path          string `json:"path"`
	Network       string `json:"network"` // lan, public, virtual, loopback, unknown
	Transport     string `json:"transport,omitempty"`
	LocalAddress  string `json:"localAddress,omitempty"`
	RemoteAddress string `json:"remoteAddress,omitempty"`
	Interface     string `json:"interface,omitempty"`
	CandidateType string `json:"candidateType,omitempty"` // host, reflexive, peer_reflexive
}

// ConnectionPathProvider must return a value snapshot without blocking on I/O.
type ConnectionPathProvider interface {
	ConnectionPath() ConnectionPath
}

// ValidFor bounds optional peer telemetry before it is retained or displayed.
func (p ConnectionPath) ValidFor(path string) bool {
	if path == "" || p.Path != path {
		return false
	}
	switch p.Network {
	case "lan", "public", "virtual", "loopback", "unknown":
	default:
		return false
	}
	switch p.CandidateType {
	case "", "host", "reflexive", "peer_reflexive":
	default:
		return false
	}
	if (strings.HasSuffix(path, "quic") && p.Transport != "quic") || (strings.HasSuffix(path, "tls") && p.Transport != "tls") {
		return false
	}
	if len(p.Interface) > 128 || strings.IndexFunc(p.Interface, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return false
	}
	for _, address := range []string{p.LocalAddress, p.RemoteAddress} {
		if address == "" {
			continue
		}
		if len(address) > 128 || strings.IndexFunc(address, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return false
		}
		if _, err := netip.ParseAddrPort(address); err != nil {
			return false
		}
	}
	return true
}
