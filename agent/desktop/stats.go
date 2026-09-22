package desktop

import (
	"sync"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const maxTrackedMissingPackets = 8192

type sessionStatsTracker struct {
	mu      sync.Mutex
	started time.Time

	recvBytes   uint64
	recvPackets uint64
	recvFrames  uint64
	dropped     uint64

	haveSequence  bool
	lastSequence  uint32
	missing       map[uint32]struct{}
	lostBase      uint64
	lossDetected  uint64
	lossRecovered uint64

	abrPackets       uint64
	abrLossDetected  uint64
	abrLossRecovered uint64

	diagnosticAt            time.Time
	diagnosticRecvBytes     uint64
	diagnosticRecvPackets   uint64
	diagnosticRecvFrames    uint64
	diagnosticLossDetected  uint64
	diagnosticLossRecovered uint64

	probeSequence uint64
	pendingProbes map[uint64]time.Time
	rttMs         float64
	jitterMs      float64
	lastRTTMs     float64

	remote protocol.DesktopSessionStats
	viewer protocol.DesktopSessionStats
	path   string

	pathStarted           time.Time
	pathBasePackets       uint64
	pathBaseLossDetected  uint64
	pathBaseLossRecovered uint64
	pathBaseDropped       uint64
}

func newSessionStatsTracker(path string) *sessionStatsTracker {
	now := time.Now()
	return &sessionStatsTracker{
		started:       now,
		diagnosticAt:  now,
		missing:       make(map[uint32]struct{}),
		pendingProbes: make(map[uint64]time.Time),
		path:          path,
		pathStarted:   now,
	}
}

func (s *sessionStatsTracker) ObservePacket(header desktopmedia.MediaHeader, bytes int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recvPackets++
	if bytes > 0 {
		s.recvBytes += uint64(bytes)
	}
	sequence := header.Sequence
	if !s.haveSequence {
		s.haveSequence = true
		s.lastSequence = sequence
		return
	}
	if sequence == s.lastSequence {
		return
	}
	if sequence > s.lastSequence {
		gap := uint64(sequence - s.lastSequence - 1)
		if gap > 0 {
			s.lossDetected += gap
			if gap <= maxTrackedMissingPackets {
				for missing := s.lastSequence + 1; missing != sequence; missing++ {
					s.missing[missing] = struct{}{}
				}
			} else {
				s.lostBase += gap
				clear(s.missing)
			}
		}
		s.lastSequence = sequence
		return
	}
	if _, ok := s.missing[sequence]; ok {
		delete(s.missing, sequence)
		s.lossRecovered++
	}
}

func (s *sessionStatsTracker) ObserveFrame() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.recvFrames++
	s.mu.Unlock()
}

func (s *sessionStatsTracker) ObserveDroppedFrame() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dropped++
	s.mu.Unlock()
}

func (s *sessionStatsTracker) SetPath(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == path {
		return
	}

	// A path transition is also a packet-order boundary. Outstanding packets
	// from the previous path are now final loss; carrying its last sequence into
	// the next path would manufacture a large cross-path sequence gap.
	if len(s.missing) > 0 {
		s.lostBase += uint64(len(s.missing))
		clear(s.missing)
	}
	s.haveSequence = false
	s.path = path
	s.pathStarted = time.Now()
	s.pathBasePackets = s.recvPackets
	s.pathBaseLossDetected = s.lossDetected
	s.pathBaseLossRecovered = s.lossRecovered
	s.pathBaseDropped = s.dropped
}

// PathQuality returns media-path-local quality since the most recent path
// transition. RTT/Jitter are intentionally left unset for now because the
// existing ping/pong runs on the reliable Relay stream and is not a direct-path
// probe. Loss and host send-queue delay are safe inputs for the current policy.
func (s *sessionStatsTracker) PathQuality(now time.Time, relay bool) PathQuality {
	if s == nil {
		return PathQuality{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	packetDelta := s.recvPackets - s.pathBasePackets
	detectedDelta := s.lossDetected - s.pathBaseLossDetected
	recoveredDelta := s.lossRecovered - s.pathBaseLossRecovered
	outstanding := uint64(0)
	if detectedDelta > recoveredDelta {
		outstanding = detectedDelta - recoveredDelta
	}
	total := packetDelta + outstanding
	lossPercent := 0.0
	if total > 0 {
		lossPercent = float64(outstanding) * 100 / float64(total)
	}

	return PathQuality{
		Available:    packetDelta > 0,
		Relay:        relay,
		LossPercent:  lossPercent,
		QueueDelayMs: s.remote.SendQueueDelayMs,
	}
}

func (s *sessionStatsTracker) MergeRemote(stats protocol.DesktopSessionStats) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.remote = stats
	s.mu.Unlock()
}

func (s *sessionStatsTracker) MergeViewer(stats protocol.DesktopSessionStats) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.viewer = stats
	s.mu.Unlock()
}

func (s *sessionStatsTracker) NewProbe(now time.Time) protocol.DesktopSessionProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probeSequence++
	sequence := s.probeSequence
	s.pendingProbes[sequence] = now
	for id := range s.pendingProbes {
		if id+32 < sequence {
			delete(s.pendingProbes, id)
		}
	}
	return protocol.DesktopSessionProbe{
		Sequence: sequence,
		SentAtUS: now.UnixMicro(),
	}
}

func (s *sessionStatsTracker) ObservePong(probe protocol.DesktopSessionProbe, now time.Time) {
	if s == nil || probe.Sequence == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sent, ok := s.pendingProbes[probe.Sequence]
	if !ok {
		return
	}
	delete(s.pendingProbes, probe.Sequence)
	rtt := float64(now.Sub(sent).Microseconds()) / 1000
	if rtt < 0 {
		return
	}
	if s.rttMs == 0 {
		s.rttMs = rtt
	} else {
		s.rttMs = s.rttMs*0.8 + rtt*0.2
	}
	if s.lastRTTMs != 0 {
		delta := rtt - s.lastRTTMs
		if delta < 0 {
			delta = -delta
		}
		if s.jitterMs == 0 {
			s.jitterMs = delta
		} else {
			s.jitterMs = s.jitterMs*0.8 + delta*0.2
		}
	}
	s.lastRTTMs = rtt
}

func (s *sessionStatsTracker) AdaptationSnapshot(now time.Time) protocol.DesktopSessionStats {
	if s == nil {
		return protocol.DesktopSessionStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	packetDelta := s.recvPackets - s.abrPackets
	detectedDelta := s.lossDetected - s.abrLossDetected
	recoveredDelta := s.lossRecovered - s.abrLossRecovered
	outstanding := uint64(0)
	if detectedDelta > recoveredDelta {
		outstanding = detectedDelta - recoveredDelta
	}
	total := packetDelta + outstanding
	lossPercent := 0.0
	if total > 0 {
		lossPercent = float64(outstanding) * 100 / float64(total)
	}

	stats := s.remote
	stats.RTTMs = s.rttMs
	stats.JitterMs = s.jitterMs
	stats.LossPercent = lossPercent
	stats.DroppedFrames += s.dropped
	if s.path != "" {
		stats.Path = s.path
	}

	s.abrPackets = s.recvPackets
	s.abrLossDetected = s.lossDetected
	s.abrLossRecovered = s.lossRecovered
	return stats
}

func (s *sessionStatsTracker) DiagnosticsSnapshot(now time.Time) protocol.DesktopSessionStats {
	if s == nil {
		return protocol.DesktopSessionStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	elapsed := now.Sub(s.diagnosticAt).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}

	bytesDelta := s.recvBytes - s.diagnosticRecvBytes
	packetDelta := s.recvPackets - s.diagnosticRecvPackets
	frameDelta := s.recvFrames - s.diagnosticRecvFrames
	detectedDelta := s.lossDetected - s.diagnosticLossDetected
	recoveredDelta := s.lossRecovered - s.diagnosticLossRecovered
	outstanding := uint64(0)
	if detectedDelta > recoveredDelta {
		outstanding = detectedDelta - recoveredDelta
	}
	total := packetDelta + outstanding
	lossPercent := 0.0
	if total > 0 {
		lossPercent = float64(outstanding) * 100 / float64(total)
	}

	stats := s.remote
	stats.ReceiveFPS = float64(frameDelta) / elapsed
	stats.DecodeFPS = s.viewer.DecodeFPS
	stats.RenderFPS = s.viewer.RenderFPS
	stats.DecodeMs = s.viewer.DecodeMs
	stats.RenderMs = s.viewer.RenderMs
	stats.DecoderBackend = s.viewer.DecoderBackend
	stats.DecoderHardware = s.viewer.DecoderHardware
	stats.ActualBitrate = int64(float64(bytesDelta*8) / elapsed)
	stats.DeliveryRate = stats.ActualBitrate
	stats.RTTMs = s.rttMs
	stats.JitterMs = s.jitterMs
	stats.LossPercent = lossPercent
	stats.DroppedFrames += s.dropped
	if s.path != "" {
		stats.Path = s.path
	}

	s.diagnosticAt = now
	s.diagnosticRecvBytes = s.recvBytes
	s.diagnosticRecvPackets = s.recvPackets
	s.diagnosticRecvFrames = s.recvFrames
	s.diagnosticLossDetected = s.lossDetected
	s.diagnosticLossRecovered = s.lossRecovered
	return stats
}

func (s *sessionStatsTracker) Snapshot(now time.Time) protocol.DesktopSessionStats {
	if s == nil {
		return protocol.DesktopSessionStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := now.Sub(s.started).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	lost := s.lostBase + uint64(len(s.missing))
	total := s.recvPackets + lost
	lossPercent := 0.0
	if total > 0 {
		lossPercent = float64(lost) * 100 / float64(total)
	}
	stats := s.remote
	stats.ReceiveFPS = float64(s.recvFrames) / elapsed
	stats.DecodeFPS = s.viewer.DecodeFPS
	stats.RenderFPS = s.viewer.RenderFPS
	stats.DecodeMs = s.viewer.DecodeMs
	stats.RenderMs = s.viewer.RenderMs
	stats.DecoderBackend = s.viewer.DecoderBackend
	stats.DecoderHardware = s.viewer.DecoderHardware
	stats.ActualBitrate = int64(float64(s.recvBytes*8) / elapsed)
	stats.DeliveryRate = stats.ActualBitrate
	stats.RTTMs = s.rttMs
	stats.JitterMs = s.jitterMs
	stats.LossPercent = lossPercent
	stats.DroppedFrames += s.dropped
	if s.path != "" {
		stats.Path = s.path
	}
	return stats
}
