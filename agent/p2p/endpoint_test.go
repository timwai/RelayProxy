package p2p

import (
	"encoding/hex"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/p2p/candidate"
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
