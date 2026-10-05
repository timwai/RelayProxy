// Package candidate validates and discovers the bounded set of endpoints used
// by RelayProxy P2P direct paths. Candidates are hints only; authorization is
// still performed by the coordinator and by the per-session punch MAC.
package candidate

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"time"

	"relayproxy/internal/protocol"
)

// Rendezvous probe wire format (network byte order):
//
// Request (16 bytes)
//   [0:4]   magic
//   [4]     version
//   [5:8]   reserved
//   [8:16]  nonce
//
// Response (32 bytes)
//   [0:4]   magic
//   [4]     version
//   [5]     reserved
//   [6:8]   observed source port
//   [8:16]  echoed nonce
//   [16:32] observed source IP as 16 bytes
const (
	MaxCandidates = 16
	ProbeMagic     uint32 = 0x52505633 // "RPV3"
	ProbeVersion   byte   = 1
	ProbeRequestSize      = 16
	ProbeResponseSize     = 32
)

var (
	ErrInvalidCandidate = errors.New("invalid P2P network candidate")
	ErrProbeUnavailable = errors.New("P2P reflexive candidate probe unavailable")
)

// Validate normalizes and de-duplicates candidates.  Hostnames, unspecified,
// loopback, multicast and link-local addresses are deliberately excluded from
// the advertised public path; LAN discovery advertises only usable interface
// addresses.
func Validate(input []protocol.P2PCandidate) ([]protocol.P2PCandidate, error) {
	if len(input) > MaxCandidates {
		return nil, fmt.Errorf("at most %d P2P candidates are allowed", MaxCandidates)
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]protocol.P2PCandidate, 0, len(input))
	for _, item := range input {
		if item.Protocol != "tcp" && item.Protocol != "udp" {
			return nil, fmt.Errorf("%w: unsupported protocol %q", ErrInvalidCandidate, item.Protocol)
		}
		if item.Type != "lan" && item.Type != "reflexive" {
			return nil, fmt.Errorf("%w: unsupported candidate type %q", ErrInvalidCandidate, item.Type)
		}
		addr, err := netip.ParseAddrPort(item.Address)
		if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() || addr.Addr().IsLoopback() || addr.Addr().IsMulticast() || addr.Addr().IsLinkLocalUnicast() {
			return nil, fmt.Errorf("%w: address %q", ErrInvalidCandidate, item.Address)
		}
		addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		item.Address = addr.String()
		key := item.Protocol + ":" + item.Address
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		return result[i].Address < result[j].Address
	})
	return result, nil
}

// Discover returns interface addresses for the local TCP and UDP listeners.
// The caller may pass the actual bound ports (including different ports) so
// the candidate is never an arbitrary forwarding destination.
func Discover(udpPort, tcpPort int) []protocol.P2PCandidate {
	result := make([]protocol.P2PCandidate, 0, MaxCandidates)
	seen := make(map[string]struct{})
	interfaces, _ := net.Interfaces()
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
			appendCandidate := func(protocolName string, port int) {
				if port <= 0 || port > 65535 {
					return
				}
				address := netip.AddrPortFrom(ip, uint16(port)).String()
				key := protocolName + ":" + address
				if _, ok := seen[key]; ok {
					return
				}
				seen[key] = struct{}{}
				result = append(result, protocol.P2PCandidate{
					Protocol: protocolName, Type: "lan", Address: address,
					Priority: discoveryPriority(ip, protocolName),
				})
			}
			appendCandidate("udp", udpPort)
			appendCandidate("tcp", tcpPort)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		return result[i].Address < result[j].Address
	})
	if len(result) > MaxCandidates {
		result = result[:MaxCandidates]
	}
	return result
}

func discoveryPriority(ip netip.Addr, protocolName string) uint32 {
	priority := uint32(1000)
	switch {
	case ip.IsPrivate():
		// Private IPv4 and IPv6 ULA candidates are the best same-LAN hints.
		priority = 1200
	case ip.Is6():
		// Native global IPv6 avoids NAT while remaining below a reachable LAN path.
		priority = 1100
	}
	if protocolName == "tcp" && priority >= 100 {
		priority -= 100
	}
	return priority
}

func EncodeProbeRequest(nonce uint64) [ProbeRequestSize]byte {
	var request [ProbeRequestSize]byte
	binary.BigEndian.PutUint32(request[0:4], ProbeMagic)
	request[4] = ProbeVersion
	binary.BigEndian.PutUint64(request[8:16], nonce)
	return request
}

func DecodeProbeRequest(raw []byte) (uint64, bool) {
	if len(raw) != ProbeRequestSize || binary.BigEndian.Uint32(raw[0:4]) != ProbeMagic || raw[4] != ProbeVersion {
		return 0, false
	}
	return binary.BigEndian.Uint64(raw[8:16]), true
}

func EncodeProbeResponse(nonce uint64, observed netip.AddrPort) ([ProbeResponseSize]byte, bool) {
	var response [ProbeResponseSize]byte
	if !observed.IsValid() || observed.Port() == 0 {
		return response, false
	}
	ip := observed.Addr().Unmap()
	if !ip.IsValid() || ip.IsUnspecified() {
		return response, false
	}
	binary.BigEndian.PutUint32(response[0:4], ProbeMagic)
	response[4] = ProbeVersion
	binary.BigEndian.PutUint16(response[6:8], observed.Port())
	binary.BigEndian.PutUint64(response[8:16], nonce)
	encodedIP := ip.As16()
	copy(response[16:32], encodedIP[:])
	return response, true
}

func DecodeProbeResponse(raw []byte, expectedNonce uint64) (netip.AddrPort, bool) {
	if len(raw) != ProbeResponseSize || binary.BigEndian.Uint32(raw[0:4]) != ProbeMagic || raw[4] != ProbeVersion ||
		binary.BigEndian.Uint64(raw[8:16]) != expectedNonce {
		return netip.AddrPort{}, false
	}
	port := binary.BigEndian.Uint16(raw[6:8])
	ip, ok := netip.AddrFromSlice(raw[16:32])
	if !ok || port == 0 {
		return netip.AddrPort{}, false
	}
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip, port), true
}

// ProbeReflexive asks the server's UDP rendezvous socket to report the source
// address it observed.  A short deadline and a nonce prevent stale responses
// from being mistaken for the current endpoint.
func ProbeReflexive(ctx context.Context, rendezvous string, conn *net.UDPConn, protocolName string) (protocol.P2PCandidate, error) {
	if conn == nil || rendezvous == "" || (protocolName != "udp" && protocolName != "tcp") {
		return protocol.P2PCandidate{}, ErrProbeUnavailable
	}
	remote, err := net.ResolveUDPAddr("udp", rendezvous)
	if err != nil {
		return protocol.P2PCandidate{}, err
	}
	var nonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
		return protocol.P2PCandidate{}, err
	}
	request := EncodeProbeRequest(nonce)
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	defer conn.SetReadDeadline(time.Time{})
	_ = conn.SetReadDeadline(deadline)
	if _, err := conn.WriteToUDP(request[:], remote); err != nil {
		return protocol.P2PCandidate{}, err
	}
	buffer := make([]byte, 64)
	for {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return protocol.P2PCandidate{}, err
		}
		observed, ok := DecodeProbeResponse(buffer[:n], nonce)
		if !ok {
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		return protocol.P2PCandidate{Protocol: protocolName, Type: "reflexive", Address: observed.String(), Priority: 800}, nil
	}
}
