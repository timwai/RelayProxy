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
	"strconv"
	"time"

	"relayproxy/internal/protocol"
)

const (
	MaxCandidates        = 16
	ProbeMagic    uint32 = 0x52505633 // "RPV3"
	ProbeVersion  byte   = 1
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
		result = limitDiscoveredCandidates(result)
	}
	return result
}

// Reserve at least one discovered endpoint for each available protocol/address
// family combination before applying the global candidate limit. VPN and
// virtual NICs must not crowd out the only reachable IPv4 or IPv6 path.
func limitDiscoveredCandidates(sorted []protocol.P2PCandidate) []protocol.P2PCandidate {
	if len(sorted) <= MaxCandidates {
		return sorted
	}
	selected := make([]protocol.P2PCandidate, 0, MaxCandidates)
	used := make(map[string]bool, MaxCandidates)
	for _, protocolName := range []string{"udp", "tcp"} {
		for _, wantIPv6 := range []bool{false, true} {
			for _, item := range sorted {
				addr, err := netip.ParseAddrPort(item.Address)
				if err != nil || item.Protocol != protocolName || addr.Addr().Is6() != wantIPv6 {
					continue
				}
				if !used[item.Protocol+":"+item.Address] {
					selected = append(selected, item)
					used[item.Protocol+":"+item.Address] = true
				}
				break
			}
		}
	}
	for _, item := range sorted {
		if len(selected) >= MaxCandidates {
			break
		}
		key := item.Protocol + ":" + item.Address
		if !used[key] {
			selected = append(selected, item)
			used[key] = true
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Priority != selected[j].Priority {
			return selected[i].Priority > selected[j].Priority
		}
		return selected[i].Address < selected[j].Address
	})
	return selected
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

// ProbeReflexive preserves the single-candidate API for existing callers.
// Use ProbeReflexiveAll to advertise both discovered address families.
func ProbeReflexive(ctx context.Context, rendezvous string, conn *net.UDPConn, protocolName string) (protocol.P2PCandidate, error) {
	all, err := ProbeReflexiveAll(ctx, rendezvous, conn, protocolName)
	if err != nil {
		return protocol.P2PCandidate{}, err
	}
	return all[0], nil
}

// ProbeReflexiveAll probes the IPv4 and IPv6 rendezvous addresses from the
// SAME UDP socket used by punching and QUIC. The probes use distinct nonces,
// are verified against their responding server, and do not depend on DNS
// answer ordering. Successful families survive failures of the other family.
func ProbeReflexiveAll(ctx context.Context, rendezvous string, conn *net.UDPConn, protocolName string) ([]protocol.P2PCandidate, error) {
	if conn == nil || rendezvous == "" || (protocolName != "udp" && protocolName != "tcp") {
		return nil, ErrProbeUnavailable
	}
	host, rawPort, err := net.SplitHostPort(rendezvous)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return nil, ErrProbeUnavailable
	}

	var ips []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = append(ips, literal.Unmap())
	} else {
		resolved, resolveErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if resolveErr != nil {
			return nil, resolveErr
		}
		for _, value := range resolved {
			ips = append(ips, value.Unmap())
		}
	}
	// Probe only one address per family; returning the first successful
	// reflexive candidate of each family keeps signaling bounded.
	var destinations []netip.AddrPort
	for _, wantIPv6 := range []bool{false, true} {
		for _, ip := range ips {
			if !ip.IsValid() || ip.Is6() != wantIPv6 || ip.IsUnspecified() {
				continue
			}
			destinations = append(destinations, netip.AddrPortFrom(ip, uint16(port)))
			break
		}
	}
	if len(destinations) == 0 {
		return nil, ErrProbeUnavailable
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	defer conn.SetReadDeadline(time.Time{})
	pending := make(map[uint64]netip.AddrPort, len(destinations))
	var lastErr error
	for _, remote := range destinations {
		var nonce uint64
		if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
			return nil, err
		}
		var request [16]byte
		binary.BigEndian.PutUint32(request[0:4], ProbeMagic)
		request[4] = ProbeVersion
		binary.BigEndian.PutUint64(request[8:16], nonce)
		if _, err := conn.WriteToUDPAddrPort(request[:], remote); err != nil {
			lastErr = err
			continue
		}
		pending[nonce] = remote
	}
	if len(pending) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrProbeUnavailable
	}

	var results []protocol.P2PCandidate
	var firstResponse time.Time
	buffer := make([]byte, 64)
	for len(pending) > 0 {
		readUntil := deadline
		if !firstResponse.IsZero() {
			// Do not add a two-second delay to IPv4-only peers when an
			// advertised IPv6 route is unavailable.
			if grace := firstResponse.Add(250 * time.Millisecond); grace.Before(readUntil) {
				readUntil = grace
			}
		}
		if !time.Now().Before(readUntil) {
			break
		}
		_ = conn.SetReadDeadline(readUntil)
		n, sender, readErr := conn.ReadFromUDPAddrPort(buffer)
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				break
			}
			if len(results) > 0 {
				break
			}
			return nil, readErr
		}
		if n != 32 || binary.BigEndian.Uint32(buffer[0:4]) != ProbeMagic || buffer[4] != ProbeVersion {
			continue
		}
		nonce := binary.BigEndian.Uint64(buffer[8:16])
		expected, ok := pending[nonce]
		if !ok || netip.AddrPortFrom(sender.Addr().Unmap(), sender.Port()) != expected {
			continue
		}
		delete(pending, nonce)
		ip, ok := netip.AddrFromSlice(buffer[16:32])
		if !ok {
			continue
		}
		ip = ip.Unmap()
		reflexivePort := binary.BigEndian.Uint16(buffer[6:8])
		if !ip.IsValid() || ip.IsUnspecified() || reflexivePort == 0 {
			continue
		}
		results = append(results, protocol.P2PCandidate{
			Protocol: protocolName, Type: "reflexive",
			Address: netip.AddrPortFrom(ip, reflexivePort).String(), Priority: 800,
		})
		if firstResponse.IsZero() {
			firstResponse = time.Now()
		}
	}
	if len(results) > 0 {
		return results, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrProbeUnavailable
}
