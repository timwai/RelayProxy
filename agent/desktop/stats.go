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

	probeSequence uint64
	pendingProbes map[uint64]time.Time
	rttMs         float64
	jitterMs      float64
	lastRTTMs     float64

	remote       protocol.DesktopSessionStats
	viewer       protocol.DesktopSessionStats
	path         string
	pathSwitches uint64
	idrRequests  uint64
}

func newSessionStatsTracker(path string) *sessionStatsTracker {
	return &sessionStatsTracker{
		started:       time.Now(),
		missing:       make(map[uint32]struct{}),
		pendingProbes: make(map[uint64]time.Time),
		path:          path,
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
	if s.path != "" && s.path != path {
		s.pathSwitches++
	}
	s.path = path
	s.mu.Unlock()
}

func (s *sessionStatsTracker) ObserveIDRRequest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.idrRequests++
	s.mu.Unlock()
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
	stats.IDRRequests = s.idrRequests
	stats.PathSwitches = s.pathSwitches
	if s.path != "" {
		stats.Path = s.path
	}

	s.abrPackets = s.recvPackets
	s.abrLossDetected = s.lossDetected
	s.abrLossRecovered = s.lossRecovered
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
	stats.ActualBitrate = int64(float64(s.recvBytes*8) / elapsed)
	stats.DeliveryRate = stats.ActualBitrate
	stats.RTTMs = s.rttMs
	stats.JitterMs = s.jitterMs
	stats.LossPercent = lossPercent
	stats.DroppedFrames += s.dropped
	stats.IDRRequests = s.idrRequests
	stats.PathSwitches = s.pathSwitches
	if s.path != "" {
		stats.Path = s.path
	}
	return stats
}
