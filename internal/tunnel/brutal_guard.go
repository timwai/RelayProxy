package tunnel

import "time"

// brutalRTTGuard is local to one QUIC sending direction. It looks for
// sustained loaded RTT inflation; transient spikes and idle paths cannot
// trigger. This is a last-resort safety fuse, not capacity estimation,
// guaranteed latency protection or global BBR fairness.
type brutalRTTGuard struct {
	lastBytes   uint64
	consecutive int
	startedAt   time.Time
}

const (
	brutalGuardMinimumLifetime      = 5 * time.Second
	brutalGuardMinimumSendBytes     = 64 * 1024 // meaningful load per 1s sample
	brutalGuardAbsoluteRTTInflation = 100 * time.Millisecond
	brutalGuardRTTMultiplier        = 3
	brutalGuardConsecutiveSamples   = 3
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


// brutalLossGuard observes *newly sent* packets and newly declared losses,
// rather than the connection's cumulative counters. quic-go may retract a
// spurious loss, so a decreasing lost counter resets the sample. Persistent
// severe loss at meaningful send volume is another safety fallback signal.
type brutalLossGuard struct {
	startedAt   time.Time
	lastSent    uint64
	lastLost    uint64
	consecutive int
}

const (
	brutalGuardMinPacketsPerSample = 100
	brutalGuardSevereLossFraction  = 0.20
)

func (g *brutalLossGuard) sample(now time.Time, packetsSent, packetsLost uint64) bool {
	if g.startedAt.IsZero() {
		g.startedAt = now
		g.lastSent = packetsSent
		g.lastLost = packetsLost
		return false
	}
	var newSent, newLost uint64
	if packetsSent >= g.lastSent {
		newSent = packetsSent - g.lastSent
	}
	if packetsLost >= g.lastLost {
		newLost = packetsLost - g.lastLost
	}
	g.lastSent, g.lastLost = packetsSent, packetsLost
	if now.Sub(g.startedAt) < brutalGuardMinimumLifetime ||
		newSent < brutalGuardMinPacketsPerSample ||
		float64(newLost)/float64(newSent) < brutalGuardSevereLossFraction {
		g.consecutive = 0
		return false
	}
	g.consecutive++
	return g.consecutive >= brutalGuardConsecutiveSamples
}
