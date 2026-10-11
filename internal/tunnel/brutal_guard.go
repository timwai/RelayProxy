package tunnel

import "time"

// brutalRTTGuard is local to one QUIC sending direction. It looks for
// sustained loaded RTT inflation; transient spikes and idle paths cannot
// trigger. This is a last-resort safety fuse, not capacity estimation,
// guaranteed latency protection or global BBR fairness.
type brutalRTTGuard struct {
	lastBytes  uint64
	consecutive int
	startedAt time.Time
}

const (
	brutalGuardMinimumLifetime = 5 * time.Second
	brutalGuardMinimumSendBytes = 64 * 1024 // meaningful load per 1s sample
	brutalGuardAbsoluteRTTInflation = 100 * time.Millisecond
	brutalGuardRTTMultiplier = 3
	brutalGuardConsecutiveSamples = 3
)

func (g *brutalRTTGuard) sample(now time.Time, minimumRTT, smoothedRTT time.Duration, sentBytes uint64) bool {
	if g.startedAt.IsZero() {
		g.startedAt = now
		g.lastBytes = sentBytes
		return false
	}
	var byteDelta uint64
	if sentBytes >= g.lastBytes {
		byteDelta = sentBytes - g.lastBytes
	}
	g.lastBytes = sentBytes

	if now.Sub(g.startedAt) < brutalGuardMinimumLifetime ||
		minimumRTT <= 0 || smoothedRTT <= 0 ||
		byteDelta < brutalGuardMinimumSendBytes ||
		smoothedRTT < minimumRTT ||
		smoothedRTT-minimumRTT < brutalGuardAbsoluteRTTInflation ||
		smoothedRTT < brutalGuardRTTMultiplier*minimumRTT {
		g.consecutive = 0
		return false
	}
	g.consecutive++
	return g.consecutive >= brutalGuardConsecutiveSamples
}
