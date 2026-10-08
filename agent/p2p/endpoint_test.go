package p2p

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/p2p/candidate"
	p2pupnp "relayproxy/internal/p2p/upnp"
)

func TestEndpointUsesSameSocketForReflexiveDiscovery(t *testing.T) {
	rendezvous, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer rendezvous.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buffer := make([]byte, 64)
		_ = rendezvous.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, readErr := rendezvous.ReadFromUDPAddrPort(buffer)
		if readErr != nil || n != 16 || binary.BigEndian.Uint32(buffer[:4]) != candidate.ProbeMagic {
			return
		}
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		var response [32]byte
		binary.BigEndian.PutUint32(response[0:4], candidate.ProbeMagic)
		response[4] = candidate.ProbeVersion
		binary.BigEndian.PutUint16(response[6:8], remote.Port())
		copy(response[8:16], buffer[8:16])
		ip := netip.MustParseAddr("198.51.100.10").As16()
		copy(response[16:32], ip[:])
		_, _ = rendezvous.WriteToUDPAddrPort(response[:], remote)
	}()

	endpoint := NewEndpoint(rendezvous.LocalAddr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := endpoint.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	<-serverDone

	candidates, fingerprint, err := endpoint.Description()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" {
		t.Fatal("missing ephemeral fingerprint")
	}
	conn, err := endpoint.UDPConn()
	if err != nil {
		t.Fatal(err)
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	foundReflexive := false
	for _, item := range candidates {
		if item.Type != "reflexive" {
			continue
		}
		address, parseErr := netip.ParseAddrPort(item.Address)
		if parseErr == nil && int(address.Port()) == localPort {
			foundReflexive = true
		}
	}
	if !foundReflexive {
		t.Fatalf("reflexive candidate did not preserve endpoint socket port %d: %#v", localPort, candidates)
	}
}

func TestCurrentNetworkSignatureIsOpaque(t *testing.T) {
	signature := CurrentNetworkSignature()
	if len(signature) != 32 {
		t.Fatalf("network signature length=%d, want 32", len(signature))
	}
	if _, err := hex.DecodeString(signature); err != nil {
		t.Fatalf("network signature is not hex: %q", signature)
	}
}

func TestEndpointUsesConfiguredUDPPortRange(t *testing.T) {
	probe, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	endpoint := NewEndpointWithPortRange("", port, port)
	if err := endpoint.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	conn, err := endpoint.UDPConn()
	if err != nil {
		t.Fatal(err)
	}
	if got := conn.LocalAddr().(*net.UDPAddr).Port; got != port {
		t.Fatalf("P2P endpoint port=%d, want configured port %d", got, port)
	}
}

func TestEndpointRejectsExhaustedUDPPortRange(t *testing.T) {
	occupied, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.LocalAddr().(*net.UDPAddr).Port

	endpoint := NewEndpointWithPortRange("", port, port)
	if err := endpoint.Start(context.Background()); err == nil {
		_ = endpoint.Close()
		t.Fatalf("P2P endpoint unexpectedly escaped exhausted configured port %d", port)
	}
}

func TestEndpointRejectsInvalidUDPPortRange(t *testing.T) {
	endpoint := NewEndpointWithPortRange("", 40001, 40000)
	if err := endpoint.Start(context.Background()); err == nil {
		_ = endpoint.Close()
		t.Fatal("invalid P2P UDP port range was accepted")
	}
}

func TestEndpointPublishesUPnPCandidate(t *testing.T) {
	previous := mapUPnPUDP
	defer func() { mapUPnPUDP = previous }()
	mapUPnPUDP = func(ctx context.Context, internalPort, portStart, portEnd int) (*p2pupnp.Mapping, netip.AddrPort, error) {
		if internalPort == 0 {
			t.Fatal("UPnP mapper received zero internal port")
		}
		return nil, netip.MustParseAddrPort("8.8.8.8:45678"), nil
	}

	endpoint := NewEndpointWithPortRangeAndUPnP("", 0, 0, true)
	if err := endpoint.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()

	candidates, _, err := endpoint.Description()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range candidates {
		if item.Protocol == "udp" && item.Type == "reflexive" &&
			item.Address == "8.8.8.8:45678" && item.Priority == 900 {
			return
		}
	}
	t.Fatalf("UPnP candidate missing from %#v", candidates)
}

func TestEndpointUPnPFailureIsNonFatal(t *testing.T) {
	previous := mapUPnPUDP
	defer func() { mapUPnPUDP = previous }()
	mapUPnPUDP = func(context.Context, int, int, int) (*p2pupnp.Mapping, netip.AddrPort, error) {
		return nil, netip.AddrPort{}, errors.New("router does not support UPnP")
	}

	endpoint := NewEndpointWithPortRangeAndUPnP("", 0, 0, true)
	if err := endpoint.Start(context.Background()); err != nil {
		t.Fatalf("UPnP failure unexpectedly failed P2P endpoint: %v", err)
	}
	defer endpoint.Close()
	candidates, _, err := endpoint.Description()
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("UPnP failure removed all local P2P candidates")
	}
}

func TestEndpointUPnPRenewalUpdatesAndWithdrawsCandidates(t *testing.T) {
	previous := mapUPnPUDP
	defer func() { mapUPnPUDP = previous }()
	oldAddress := netip.MustParseAddrPort("8.8.8.8:45678")
	newAddress := netip.MustParseAddrPort("8.8.8.8:45679")
	mapUPnPUDP = func(context.Context, int, int, int) (*p2pupnp.Mapping, netip.AddrPort, error) {
		return nil, oldAddress, nil
	}
	endpoint := NewEndpointWithPortRangeAndUPnP("", 0, 0, true)
	if err := endpoint.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	if state, _ := endpoint.UPnPStatus(); state != "MAPPED" {
		t.Fatalf("initial UPnP state=%q", state)
	}
	updates := endpoint.CandidateChanges()
	endpoint.applyUPnPUpdate(p2pupnp.MappingUpdate{Healthy: false, Reason: "router rebooted"})
	select {
	case items := <-updates:
		for _, item := range items {
			if item.Address == oldAddress.String() {
				t.Fatalf("expired UPnP candidate was retained: %#v", items)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("UPnP lease loss did not notify P2P sessions")
	}
	if state, reason := endpoint.UPnPStatus(); state != "DEGRADED" || reason != "router rebooted" {
		t.Fatalf("unexpected UPnP failure state: %s %q", state, reason)
	}
	endpoint.applyUPnPUpdate(p2pupnp.MappingUpdate{Healthy: true, Address: newAddress})
	select {
	case items := <-updates:
		found := false
		for _, item := range items {
			if item.Address == oldAddress.String() {
				t.Fatalf("stale UPnP candidate reappeared: %#v", items)
			}
			found = found || item.Address == newAddress.String()
		}
		if !found {
			t.Fatalf("new external mapping was not published: %#v", items)
		}
	case <-time.After(time.Second):
		t.Fatal("UPnP recovery did not notify P2P sessions")
	}
	if state, reason := endpoint.UPnPStatus(); state != "MAPPED" || reason != "" {
		t.Fatalf("unexpected recovered state: %s %q", state, reason)
	}
}

func TestPrivateWANRouterMappingNeverBecomesPublicP2PCandidate(t *testing.T) {
	previous := mapUPnPUDP
	defer func() { mapUPnPUDP = previous }()
	privateWAN := netip.MustParseAddrPort("100.64.10.7:20900")
	publicWAN := netip.MustParseAddrPort("8.8.8.8:20900")
	mapUPnPUDP = func(context.Context, int, int, int) (*p2pupnp.Mapping, netip.AddrPort, error) {
		return nil, privateWAN, nil
	}
	endpoint := NewEndpointWithPortRangeAndUPnP("", 0, 0, true)
	if err := endpoint.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	assertNotPublished := func() {
		t.Helper()
		candidates, _, err := endpoint.Description()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range candidates {
			if c.Address == privateWAN.String() {
				t.Fatal("CGNAT WAN was advertised as public P2P candidate")
			}
		}
	}
	assertNotPublished()
	if state, reason := endpoint.UPnPStatus(); state != "CGNAT" || reason == "" {
		t.Fatalf("CGNAT mapping status=%q reason=%q", state, reason)
	}
	if address := endpoint.UPnPAddress(); address != privateWAN.String() {
		t.Fatalf("CGNAT mapping not visible in diagnostics: %q", address)
	}
	endpoint.applyUPnPUpdate(p2pupnp.MappingUpdate{Address: publicWAN, Healthy: true})
	if state, _ := endpoint.UPnPStatus(); state != "MAPPED" {
		t.Fatalf("public WAN recovery state=%q", state)
	}
	candidates, _, err := endpoint.Description()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range candidates {
		found = found || c.Address == publicWAN.String()
	}
	if !found {
		t.Fatal("restored public mapping was not advertised")
	}
	endpoint.applyUPnPUpdate(p2pupnp.MappingUpdate{Address: privateWAN, Healthy: true})
	assertNotPublished()
	if state, _ := endpoint.UPnPStatus(); state != "CGNAT" {
		t.Fatalf("mapping returning to CGNAT state=%q", state)
	}
}
