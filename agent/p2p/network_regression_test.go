package p2p

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"relayproxy/internal/protocol"
)

func TestPathReportKeepsWireSummaryCountOnly(t *testing.T) {
	reports := make(chan protocol.P2PControlMessage, 1)
	m := NewManager(context.Background(), func(_ context.Context, msg protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		reports <- msg
		return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck}, nil
	}, nil, time.Minute)
	defer m.Close()
	endpoint := NewEndpoint("")
	s := m.newSessionWithEndpoint(42, "client", "exit", []byte("0123456789abcdef"), 0, endpoint)
	if !strings.HasSuffix(s.Snapshot().CandidateSummary, " upnp=disabled") {
		t.Fatal("local UPnP status was lost")
	}
	s.reportPath(protocol.P2PPathDirectQUIC, "")
	select {
	case msg := <-reports:
		if msg.CandidateSummary != "local:lan=0,reflexive=0;peer:lan=0,reflexive=0" {
			t.Fatalf("wire summary: %q", msg.CandidateSummary)
		}
	case <-time.After(time.Second):
		t.Fatal("missing report")
	}
}

func TestCooldownObservesNetworkChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var sig atomic.Value
		sig.Store("wan")
		m := NewQUICManagerWithOptions(context.Background(), nil, "", time.Minute, QUICManagerOptions{NetworkCheckInterval: time.Second, NetworkSignature: func() string { return sig.Load().(string) }})
		defer m.Close()
		for i := 0; i < 4; i++ {
			m.recordFailureError("exit", &classifiedFailure{kind: "udp_unreachable", err: fmt.Errorf("UDP unavailable")})
		}
		sig.Store("lan")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		m.mu.Lock()
		_, cooling := m.cooldowns["exit"]
		got := m.networkSig
		m.mu.Unlock()
		if cooling {
			t.Fatalf("network changed WAN -> LAN but cooldown survives; signature=%q", got)
		}
	})
}

func TestClientReadyQueueDoesNotAccumulateClosedSessions(t *testing.T) {
	m := NewManager(context.Background(), nil, nil, time.Minute)
	defer m.Close()
	for i := uint64(1); i <= uint64(cap(m.ready)+1); i++ {
		s := m.newSession(i, "client", "exit", []byte("0123456789abcdef"), 0)
		s.mu.Lock()
		s.clientRole = true
		s.state = StateReady
		s.mu.Unlock()
		if !s.enqueueReady(nil) {
			t.Fatalf("CLIENT-only manager permanently rejects ready session %d; %d closed notifications remain", i, len(m.ready))
		}
		m.remove(i)
	}
	if len(m.ready) != 0 {
		t.Fatal("Client notifications accumulated")
	}
}

func TestExpiredCooldownStopsNetworkPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var checks atomic.Int64
		m := NewQUICManagerWithOptions(context.Background(), nil, "", time.Minute, QUICManagerOptions{
			NetworkCheckInterval: time.Second,
			NetworkSignature:     func() string { checks.Add(1); return "unchanged" },
		})
		defer m.Close()
		m.recordFailure("exit", "temporary UDP failure")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if checks.Load() <= 1 {
			t.Fatal("cooldown did not wake network watcher")
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		before := checks.Load()
		time.Sleep(time.Minute)
		if checks.Load() != before {
			t.Fatal("expired cooldown left polling armed")
		}
	})
}
