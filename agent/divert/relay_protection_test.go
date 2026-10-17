package divert

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopGuardIncludesActualRelayPeerAcrossDNSChanges(t *testing.T) {
	peer := "203.0.113.17"
	s := newTestServer(t, Options{
		Guard:    LoopGuard{RelayHost: "localhost", RelayPorts: []int{35820}},
		RelayIPs: func() []string { return []string{peer} },
	})
	for _, next := range []string{"203.0.113.17", "2001:db8::17"} {
		previous := peer
		peer = next
		if err := prepareLoopGuard(s); err != nil {
			t.Fatal(err)
		}
		if !s.guard.MustDirect("", peer, 35820) {
			t.Fatalf("actual peer %s was excluded from the DNS-based guard: %+v", peer, s.guard)
		}
		if peer != previous && slices.Contains(s.guard.RelayIPs, previous) {
			t.Fatalf("reconnect retained a stale peer: %v", s.guard.RelayIPs)
		}
	}
}

func TestRelayPacketsBypassAttributionAndSaturatedQueues(t *testing.T) {
	for _, proto := range []Protocol{ProtoTCP, ProtoUDP} {
		t.Run(string(proto), func(t *testing.T) {
			i, device := newTestInterceptor(t, Options{
				Config:        Config{DefaultAction: ActionReject},
				Guard:         LoopGuard{RelayIPs: []string{"203.0.113.17"}, RelayPorts: []int{35820}},
				FakeIPEnabled: func() bool { return true },
			})
			i.device = &deferredTestDevice{device}
			i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
				t.Fatal("relay transport entered process lookup")
				return packetProcess{}, errors.New("unavailable")
			}
			for _, queue := range i.udpClassifyQueues {
				for len(queue) < cap(queue) {
					queue <- interceptedUDP{}
				}
			}
			data, _ := packetTestFixture(false, proto, []byte("relay transport"), false)
			packet, err := parseIPPacket(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := rewriteIPPacket(data, packet.Source, netip.MustParseAddrPort("203.0.113.17:35820")); err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(data)
			if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
				t.Fatal(err)
			}
			got := expectInterceptedPacket(t, device)
			if !got.meta.outbound || !bytes.Equal(got.data, original) {
				t.Fatal("relay transport was redirected or modified")
			}
		})
	}
}

func TestPauseReleasesCaptureAndAllowsRearm(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	s := newTestServer(t, Options{ProxyReady: ready.Load})
	start := func(s *Server) (systemInterceptor, error) {
		i := newPacketInterceptor(s, newTestPacketDevice(), nil, nil)
		i.start()
		return i, nil
	}
	for range 2 {
		if err := s.start(start); err != nil {
			t.Fatal(err)
		}
		if !s.Running() {
			t.Fatal("capture did not start")
		}
		route, err := s.ClassifyFlow(testFlow(ProtoUDP, nil))
		if err != nil {
			t.Fatal(err)
		}
		ready.Store(false)
		s.Pause()
		if s.Running() || s.ctx.Err() != nil {
			t.Fatal("pause did not release capture while preserving server")
		}
		if route.udp.ctx.Err() == nil {
			t.Fatal("pause retained an old UDP association")
		}
		if err := s.start(start); err == nil {
			t.Fatal("capture armed without an authenticated relay")
		}
		ready.Store(true)
	}
	if err := s.start(func(s *Server) (systemInterceptor, error) {
		ready.Store(false)
		return start(s)
	}); err == nil || s.Running() {
		t.Fatal("capture remained armed after readiness was lost during startup")
	}
}

type failingFinalizeDevice struct{ *testPacketDevice }

func (*failingFinalizeDevice) Finalize(packetMetadata) error {
	return errors.New("broken completion pipe")
}

func TestFinalizeFailureReleasesCapture(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "packet", true: "empty"}[empty], func(t *testing.T) {
			s := newTestServer(t, Options{Config: Config{DefaultAction: ActionDirect}})
			device := newTestPacketDevice()
			if err := s.start(func(s *Server) (systemInterceptor, error) {
				i := newPacketInterceptor(s, &failingFinalizeDevice{device}, nil, nil)
				i.start()
				return i, nil
			}); err != nil {
				t.Fatal(err)
			}
			var data []byte
			if !empty {
				data, _ = packetTestFixture(false, ProtoUDP, nil, false)
			}
			device.incoming <- capturedTestPacket{data: data}
			select {
			case <-device.done:
			case <-time.After(2 * time.Second):
				t.Fatal("completion failure kept host capture active")
			}
			s.Pause()
			if s.Diagnostics().LastError == "" {
				t.Fatal("capture failure diagnostics were lost during release")
			}
			if s.ctx.Err() != nil {
				t.Fatal("completion failure destroyed restart state")
			}
		})
	}
}
