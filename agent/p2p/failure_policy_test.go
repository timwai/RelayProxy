package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestFailurePolicySeparatesTransientOfflineAndUDP(t *testing.T) {
	m := NewManager(context.Background(), nil, nil, time.Minute)
	defer m.Close()
	cases := []struct {
		err  error
		kind string
		cap  time.Duration
	}{
		{context.DeadlineExceeded, "transient", 30 * time.Second},
		{protocol.NewRelayError(protocol.ErrCodeExitOffline, "offline"), "peer_offline", time.Minute},
		{protocol.NewRelayError(protocol.ErrCodeRateLimited, "busy"), "rate_limit", time.Minute},
		{&classifiedFailure{"udp_unreachable", errors.New("punch failed")}, "udp_unreachable", 10 * time.Minute},
		{protocol.NewRelayError(protocol.ErrCodeAccessDenied, "denied"), "policy", 10 * time.Minute},
	}
	for _, tc := range cases {
		m.resetFailure("exit")
		for range 6 {
			m.recordFailureError("exit", tc.err)
		}
		state := m.cooldowns["exit"]
		if state.kind != tc.kind || time.Until(state.until) > tc.cap || time.Until(state.until) < tc.cap-time.Second {
			t.Fatalf("wrong policy: %+v", state)
		}
		status, ok := m.PathStatus("exit")
		if !ok || status.FailureKind != tc.kind || status.RetryAfterMS <= 0 {
			t.Fatalf("missing cooldown diagnostics: %+v", status)
		}
	}
	// A different cause starts a fresh ladder instead of inheriting a long ban.
	m.recordFailureError("exit", context.DeadlineExceeded)
	if state := m.cooldowns["exit"]; state.count != 1 || time.Until(state.until) > 5*time.Second {
		t.Fatalf("cause change retained ban: %+v", state)
	}
}

func TestPeerOnlineTransitionReleasesCooldownOnlyOnce(t *testing.T) {
	m := NewManager(context.Background(), nil, nil, time.Minute)
	defer m.Close()
	m.ObserveExits([]protocol.ProxyExit{{DeviceID: "exit", Online: false}})
	m.recordFailureError("exit", protocol.NewRelayError(protocol.ErrCodeExitOffline, "offline"))
	m.ObserveExits([]protocol.ProxyExit{{DeviceID: "exit", Online: true}})
	if _, exists := m.cooldowns["exit"]; exists {
		t.Fatal("online peer retained offline cooldown")
	}
	m.recordFailureError("exit", &classifiedFailure{"udp_unreachable", errors.New("blocked")})
	m.ObserveExits([]protocol.ProxyExit{{DeviceID: "exit", Online: true}})
	if _, exists := m.cooldowns["exit"]; !exists {
		t.Fatal("identical heartbeat inventory reset UDP cooldown")
	}
	m.recordFailureError("exit", protocol.NewRelayError(protocol.ErrCodeAccessDenied, "denied"))
	m.ObserveExits([]protocol.ProxyExit{{DeviceID: "exit", Online: false}})
	m.ObserveExits([]protocol.ProxyExit{{DeviceID: "exit", Online: true}})
	if state := m.cooldowns["exit"]; state.kind != "policy" {
		t.Fatal("online transition bypassed policy cooldown")
	}
}

func TestCoordinatorErrorRetainsCodeForFailurePolicy(t *testing.T) {
	m := NewManager(context.Background(), func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		return protocol.P2PControlMessage{Type: protocol.P2PControlError, ErrorCode: protocol.ErrCodeExitOffline, ErrorMessage: "offline"}, nil
	}, testDescription("127.0.0.1:1", "fingerprint"), time.Minute)
	defer m.Close()
	_, err := m.StartClient(context.Background(), "exit")
	if failureKind(err) != "peer_offline" {
		t.Fatalf("coordinator code lost: %v", err)
	}
}
