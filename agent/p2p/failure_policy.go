package p2p

import (
	"errors"
	"time"

	"relayproxy/internal/protocol"
)

type classifiedFailure struct {
	kind string
	err  error
}

func (e *classifiedFailure) Error() string { return e.err.Error() }
func (e *classifiedFailure) Unwrap() error { return e.err }

func failureKind(err error) string {
	var classified *classifiedFailure
	if errors.As(err, &classified) {
		return classified.kind
	}
	var relay *protocol.RelayError
	if errors.As(err, &relay) {
		switch relay.Code {
		case protocol.ErrCodeExitOffline, "EXIT_NOTIFY_FAILED":
			return "peer_offline"
		case protocol.ErrCodeAccessDenied, protocol.ErrCodeAuthFailed, protocol.ErrCodeInvalidRequest:
			return "policy"
		case protocol.ErrCodeRateLimited, protocol.ErrCodeConnectionLimit:
			return "rate_limit"
		}
	}
	return "transient"
}

func failureDelay(kind string, count int) time.Duration {
	delays := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	delay := delays[min(max(count-1, 0), len(delays)-1)]
	switch kind {
	case "transient":
		return min(delay, 30*time.Second)
	case "peer_offline", "rate_limit":
		return min(delay, time.Minute)
	default:
		return delay // UDP reachability and policy failures need fewer probes.
	}
}

func (m *Manager) recordFailureError(exitID string, err error) {
	if m == nil || exitID == "" || err == nil || m.closed.Load() {
		return
	}
	kind := failureKind(err)
	m.mu.Lock()
	s := m.cooldowns[exitID]
	if s.kind != kind {
		s.count = 0
	}
	s.count++
	s.kind, s.reason = kind, err.Error()
	s.until = time.Now().Add(failureDelay(kind, s.count))
	m.cooldowns[exitID] = s
	m.mu.Unlock()
}

// ObserveExits uses authenticated inventory transitions, not repeated identical
// heartbeat snapshots, to release a cooldown when a peer comes back online.
func (m *Manager) ObserveExits(exits []protocol.ProxyExit) {
	if m == nil || m.closed.Load() {
		return
	}
	m.mu.Lock()
	next := make(map[string]bool, len(exits))
	var retry []string
	for _, exit := range exits {
		next[exit.DeviceID] = exit.Online
		wasOnline, known := m.peerOnline[exit.DeviceID]
		failure, cooling := m.cooldowns[exit.DeviceID]
		if cooling && failure.kind != "policy" && failure.kind != "rate_limit" && exit.Online && ((!wasOnline && known) || (!known && failure.kind == "peer_offline")) {
			delete(m.cooldowns, exit.DeviceID)
			retry = append(retry, exit.DeviceID)
		}
	}
	m.peerOnline = next
	m.mu.Unlock()
	if m.send != nil && (m.local != nil || m.endpointFactory != nil) {
		for _, exitID := range retry {
			m.PrewarmClient(exitID)
		}
	}
}
