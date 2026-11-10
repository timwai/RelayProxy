package p2p

import (
	"context"
	"net"
	"testing"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/protocol"
)

func TestDialUDPUsesUpdatedTargetPort(t *testing.T) {
	s, _ := newDirectUDPPair(t)
	s.mu.Lock()
	live := append([]protocol.RDPCandidate(nil), s.candidates...)
	s.mu.Unlock()
	// candidate.Validate excludes loopback. Use the host's actual LAN address.
	ap, err := net.ResolveUDPAddr("udp", live[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	live = candidate.Discover(ap.Port, 0)
	if len(live) == 0 {
		t.Skip("no LAN interface")
	}
	stale, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	s.mu.Lock()
	s.candidates = []protocol.RDPCandidate{{Protocol: "udp", Type: "lan", Address: stale.LocalAddr().String()}}
	s.mu.Unlock()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	s.manager.rendezvous = probe.LocalAddr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		pc, err := s.DialUDP(ctx)
		if pc != nil {
			pc.Close()
		}
		done <- err
	}()
	probe.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, remote, err := probe.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	// A real candidate_update arrives after punching has started with stale hints.
	s.manager.HandleControl(protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate, SessionID: s.ID, SessionToken: s.Token, Candidates: live})
	nonce, ok := candidate.DecodeProbeRequest(buf[:n])
	if !ok {
		t.Fatal("bad probe")
	}
	reply, ok := candidate.EncodeProbeResponse(nonce, remote.AddrPort())
	if !ok {
		t.Fatal("bad reply")
	}
	probe.WriteToUDP(reply[:], remote)
	if err := <-done; err != nil {
		t.Fatalf("reachable new target port ignored after candidate_update: %v", err)
	}
}

func TestLANDoesNotWaitForDeadRendezvous(t *testing.T) {
	s, _ := newDirectUDPPair(t)
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	s.manager.rendezvous = probe.LocalAddr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	pc, err := s.DialUDP(ctx)
	if pc != nil {
		info := pc.(protocol.ConnectionPathProvider).ConnectionPath()
		if info.Path != "udp_p2p" || info.RemoteAddress == "" || info.Transport != "udp" {
			t.Fatalf("missing UDP path: %+v", info)
		}
		pc.Close()
	}
	if err != nil {
		t.Fatalf("reachable LAN path not attempted within 600ms (Relay starts at 300ms): %v", err)
	}
}

func TestTargetPublishesLANBeforeRendezvousCompletes(t *testing.T) {
	if len(candidate.Discover(1, 0)) == 0 {
		t.Skip("no LAN interface")
	}
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	updates := make(chan protocol.RDPControlMessage, 4)
	m := NewManager(context.Background(), func(_ context.Context, msg protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		updates <- msg
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}, "127.0.0.1:9", time.Minute, probe.LocalAddr().String())
	defer m.Close()
	s := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
	started := make(chan error, 1)
	go func() { started <- m.startTargetUDP(s) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("target waited for a silent rendezvous before starting LAN reader")
	}
	select {
	case msg := <-updates:
		s.mu.Lock()
		port := s.udp.LocalAddr().(*net.UDPAddr).Port
		s.mu.Unlock()
		for _, c := range msg.Candidates {
			if c.Protocol != "udp" {
				continue
			}
			addr, err := net.ResolveUDPAddr("udp", c.Address)
			if err != nil || addr.Port != port {
				t.Fatalf("published stale candidate: %v", c)
			}
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("target did not publish LAN before rendezvous response")
	}
}
