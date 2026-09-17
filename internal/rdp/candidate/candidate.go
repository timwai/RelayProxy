// Package candidate validates and discovers the bounded set of endpoints used
// by the RDP direct path.  Candidates are hints only; authorization is still
// performed by the RelayProxy coordinator and by the per-session punch MAC.
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

const (
	MaxCandidates        = 16
	ProbeMagic    uint32 = 0x52505633 // "RPV3"
	ProbeVersion  byte   = 1
)

var (
	ErrInvalidCandidate = errors.New("invalid RDP network candidate")
	ErrProbeUnavailable = errors.New("RDP reflexive candidate probe unavailable")
)

// Validate normalizes and de-duplicates candidates.  Hostnames, unspecified,
// loopback, multicast and link-local addresses are deliberately excluded from
// the advertised public path; LAN discovery advertises only usable interface
// addresses.
func Validate(input []protocol.RDPCandidate) ([]protocol.RDPCandidate, error) {
	if len(input) > MaxCandidates {
		return nil, fmt.Errorf("at most %d RDP candidates are allowed", MaxCandidates)
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]protocol.RDPCandidate, 0, len(input))
	for _, item := range input {
		if item.Protocol != "tcp" && item.Protocol != "udp" {
			return nil, fmt.Errorf("%w: unsupported protocol %q", ErrInvalidCandidate, item.Protocol)
		}
		if item.Type != "lan" && item.Type != "reflexive" {
			return nil, fmt.Errorf("%w: unsupported candidate type %q", ErrInvalidCandidate, item.Type)
		}
		addr, err := netip.ParseAddrPort(item.Address)
		if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() || addr.Addr().IsMulticast() || addr.Addr().IsLinkLocalUnicast() {
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
func Discover(udpPort, tcpPort int) []protocol.RDPCandidate {
	result := make([]protocol.RDPCandidate, 0, MaxCandidates)
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
			if !ip.IsValid() || !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if udpPort > 0 {
				result = append(result, protocol.RDPCandidate{Protocol: "udp", Type: "lan", Address: netip.AddrPortFrom(ip, uint16(udpPort)).String(), Priority: 1000})
			}
			if tcpPort > 0 {
				result = append(result, protocol.RDPCandidate{Protocol: "tcp", Type: "lan", Address: netip.AddrPortFrom(ip, uint16(tcpPort)).String(), Priority: 900})
			}
			if len(result) >= MaxCandidates {
				return result[:MaxCandidates]
			}
		}
	}
	return result
}

// ProbeReflexive asks the server's UDP rendezvous socket to report the source
// address it observed.  A short deadline and a nonce prevent stale responses
// from being mistaken for the current endpoint.
func ProbeReflexive(ctx context.Context, rendezvous string, conn *net.UDPConn, protocolName string) (protocol.RDPCandidate, error) {
	if conn == nil || rendezvous == "" || (protocolName != "udp" && protocolName != "tcp") {
		return protocol.RDPCandidate{}, ErrProbeUnavailable
	}
	remote, err := net.ResolveUDPAddr("udp", rendezvous)
	if err != nil {
		return protocol.RDPCandidate{}, err
	}
	var nonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
		return protocol.RDPCandidate{}, err
	}
	request := make([]byte, 16)
	binary.BigEndian.PutUint32(request[0:4], ProbeMagic)
	request[4] = ProbeVersion
	binary.BigEndian.PutUint64(request[8:16], nonce)
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	defer conn.SetReadDeadline(time.Time{})
	_ = conn.SetReadDeadline(deadline)
	if _, err := conn.WriteToUDP(request, remote); err != nil {
		return protocol.RDPCandidate{}, err
	}
	buffer := make([]byte, 64)
	for {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return protocol.RDPCandidate{}, err
		}
		if n != 32 || binary.BigEndian.Uint32(buffer[0:4]) != ProbeMagic || buffer[4] != ProbeVersion || binary.BigEndian.Uint64(buffer[8:16]) != nonce {
			continue
		}
		ip, ok := netip.AddrFromSlice(buffer[16:32])
		ip = ip.Unmap()
		if !ok || !ip.IsValid() || ip.IsUnspecified() {
			return protocol.RDPCandidate{}, ErrProbeUnavailable
		}
		// The source port is returned in the lower two bytes of the response.
		port := binary.BigEndian.Uint16(buffer[6:8])
		if port == 0 {
			return protocol.RDPCandidate{}, ErrProbeUnavailable
		}
		_ = conn.SetReadDeadline(time.Time{})
		return protocol.RDPCandidate{Protocol: protocolName, Type: "reflexive", Address: netip.AddrPortFrom(ip, port).String(), Priority: 800}, nil
	}
}
