package candidate

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestValidateCandidates(t *testing.T) {
	items, err := Validate([]protocol.RDPCandidate{
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:3389", Priority: 10},
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:3389", Priority: 5},
		{Protocol: "udp", Type: "lan", Address: "[2001:db8::10]:3389", Priority: 20},
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("candidate normalization failed: %v %#v", err, items)
	}
	if items[0].Address != "[2001:db8::10]:3389" {
		t.Fatalf("IPv6 candidate was not preserved or prioritized: %#v", items)
	}
	for _, invalid := range []protocol.RDPCandidate{
		{Protocol: "tcp", Type: "lan", Address: "example.com:3389"},
		{Protocol: "udp", Type: "lan", Address: "0.0.0.0:3389"},
		{Protocol: "udp", Type: "reflexive", Address: "127.0.0.1:3389"},
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:0"},
		{Protocol: "udp", Type: "lan", Address: "[fe80::1]:3389"},
	} {
		if _, err := Validate([]protocol.RDPCandidate{invalid}); err == nil {
			t.Fatalf("invalid candidate accepted: %#v", invalid)
		}
	}
}

func TestDiscoveryPriorityMatchesDirectPathPreference(t *testing.T) {
	privateIPv4 := netip.MustParseAddr("192.168.1.10")
	privateIPv6 := netip.MustParseAddr("fd00::10")
	publicIPv6 := netip.MustParseAddr("2001:db8::10")
	publicIPv4 := netip.MustParseAddr("198.51.100.10")

	if got := discoveryPriority(privateIPv4, "udp"); got != 1200 {
		t.Fatalf("private IPv4 UDP priority=%d, want 1200", got)
	}
	if got := discoveryPriority(privateIPv6, "udp"); got != 1200 {
		t.Fatalf("private IPv6 UDP priority=%d, want 1200", got)
	}
	if got := discoveryPriority(publicIPv6, "udp"); got != 1100 {
		t.Fatalf("public IPv6 UDP priority=%d, want 1100", got)
	}
	if got := discoveryPriority(publicIPv4, "udp"); got != 1000 {
		t.Fatalf("public IPv4 UDP priority=%d, want 1000", got)
	}
	if got := discoveryPriority(privateIPv4, "tcp"); got != 1100 {
		t.Fatalf("private IPv4 TCP priority=%d, want 1100", got)
	}
}

func TestDiscoverLimitRetainsSingleIPv4AmongManyIPv6Addresses(t *testing.T) {
	var sorted []protocol.P2PCandidate
	for i := 1; i <= 24; i++ {
		sorted = append(sorted, protocol.P2PCandidate{
			Protocol: "udp", Type: "lan",
			Address: fmt.Sprintf("[2001:db8::%x]:51000", i), Priority: 1100,
		})
	}
	sorted = append(sorted, protocol.P2PCandidate{
		Protocol: "udp", Type: "lan",
		Address: "198.51.100.10:51000", Priority: 1000,
	})
	limited := limitDiscoveredCandidates(sorted, 14)
	if len(limited) != 14 {
		t.Fatalf("limited candidates=%d, want 14", len(limited))
	}
	foundIPv4, foundIPv6 := false, false
	for _, item := range limited {
		addr, err := netip.ParseAddrPort(item.Address)
		if err != nil {
			t.Fatal(err)
		}
		foundIPv4 = foundIPv4 || addr.Addr().Is4()
		foundIPv6 = foundIPv6 || addr.Addr().Is6()
	}
	if !foundIPv4 || !foundIPv6 {
		t.Fatalf("candidate limit removed a usable address family: ipv4=%v ipv6=%v", foundIPv4, foundIPv6)
	}
}

func TestPrioritizeSameSubnetKeepsWANFallbackAndDoesNotModifyInput(t *testing.T) {
	localSubnets := []netip.Prefix{
		netip.MustParsePrefix("192.168.40.0/24"),
		netip.MustParsePrefix("fd42:abcd::/64"),
	}
	input := []protocol.P2PCandidate{
		{Protocol: "udp", Type: "lan", Address: "192.168.40.22:45001", Priority: 1200},
		{Protocol: "udp", Type: "lan", Address: "192.168.41.22:45001", Priority: 1200},
		{Protocol: "udp", Type: "lan", Address: "[fd42:abcd::22]:45001", Priority: 1200},
		{Protocol: "udp", Type: "reflexive", Address: "203.0.113.22:45001", Priority: 800},
		{Protocol: "udp", Type: "reflexive", Address: "192.168.40.33:45001", Priority: 800},
	}
	got := PrioritizeSameSubnet(input, localSubnets)
	want := []uint32{1600, 1200, 1600, 800, 800}
	if len(got) != len(want) {
		t.Fatalf("candidate count=%d, want %d", len(got), len(want))
	}
	for i, priority := range want {
		if got[i].Priority != priority || got[i].Address != input[i].Address {
			t.Fatalf("candidate %d = %+v, expected priority %d and address %q", i, got[i], priority, input[i].Address)
		}
	}
	if input[0].Priority != 1200 || input[2].Priority != 1200 {
		t.Fatal("peer-advertised candidate priorities were modified")
	}
}

func TestPrioritizeSameSubnetDoesNotTrustUnrelatedPrivateRanges(t *testing.T) {
	peers := []protocol.P2PCandidate{
		{Protocol: "udp", Type: "lan", Address: "10.20.1.10:9000", Priority: 1200},
		{Protocol: "udp", Type: "lan", Address: "10.21.1.10:9000", Priority: 1200},
	}
	got := PrioritizeSameSubnet(peers, []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")})
	if got[0].Priority <= got[1].Priority {
		t.Fatalf("same-subnet candidate was not preferred: %+v", got)
	}
	for _, item := range PrioritizeSameSubnet(peers, nil) {
		if item.Priority != 1200 {
			t.Fatal("empty local networks incorrectly boosted private peer address")
		}
	}
}

func TestProbeReflexiveAllWorksWithIPv4OnlySocket(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	go func() {
		var buffer [64]byte
		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, readErr := server.ReadFromUDPAddrPort(buffer[:])
		if readErr != nil || n != 16 {
			return
		}
		var response [32]byte
		binary.BigEndian.PutUint32(response[:4], ProbeMagic)
		response[4] = ProbeVersion
		binary.BigEndian.PutUint16(response[6:8], remote.Port())
		copy(response[8:16], buffer[8:16])
		ip := remote.Addr().As16()
		copy(response[16:32], ip[:])
		_, _ = server.WriteToUDPAddrPort(response[:], remote)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// localhost may advertise AAAA as well as A. An IPv4-only socket must
	// still get the usable IPv4 reflexive address in a mixed-family lookup.
	port := server.LocalAddr().(*net.UDPAddr).Port
	results, err := ProbeReflexiveAll(ctx, fmt.Sprintf("localhost:%d", port), client, "udp")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("reflexive candidates=%d, want 1: %#v", len(results), results)
	}
	addr, err := netip.ParseAddrPort(results[0].Address)
	if err != nil || !addr.Addr().Is4() {
		t.Fatalf("invalid IPv4 reflexive candidate: %#v (%v)", results[0], err)
	}
}
