package desktop

import (
	"context"
	"errors"
	"strings"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const (
	maxControllerAudioFrames             = 8
	maxControllerAudioConcealmentFrames  = 3
	controllerAudioPlayoutFrames         = 2
	defaultControllerAudioFramePeriod = 20 * time.Millisecond
	minControllerAudioPlayoutDelay    = 20 * time.Millisecond
	maxControllerAudioPlayoutDelay    = 80 * time.Millisecond
)

type audioRuntimeCounters struct {
	ReceivedFrames            uint64
	ReceivedBytes             uint64
	ConsumedFrames            uint64
	ConsumedBytes             uint64
	QueueDroppedFrames        uint64
	GenerationDiscardedFrames uint64
	RejectedFrames            uint64
	ReorderedFrames           uint64
	DuplicateFrames           uint64
	LateFrames                uint64
	PlayoutTimeoutFrames      uint64
	ConcealmentFrames         uint64
	GapSkippedFrames          uint64
	LastFrameID               uint32
	LastConsumedFrameID       uint32
	LastMediaTimestampUS      uint64
	LastReceivedAtUnixMs      int64
	LastConsumedAtUnixMs      int64
}

type DesktopAudioDiagnostics struct {
	Enabled                   bool                        `json:"enabled"`
	Config                    protocol.DesktopAudioConfig `json:"config"`
	QueueFrames               int                         `json:"queueFrames"`
	QueueCapacity             int                         `json:"queueCapacity"`
	ReceivedFrames            uint64                      `json:"receivedFrames"`
	ReceivedBytes             uint64                      `json:"receivedBytes"`
	ConsumedFrames            uint64                      `json:"consumedFrames"`
	ConsumedBytes             uint64                      `json:"consumedBytes"`
	QueueDroppedFrames        uint64                      `json:"queueDroppedFrames"`
	GenerationDiscardedFrames uint64                      `json:"generationDiscardedFrames"`
	RejectedFrames            uint64                      `json:"rejectedFrames"`
	ReorderedFrames           uint64                      `json:"reorderedFrames"`
	DuplicateFrames           uint64                      `json:"duplicateFrames"`
	LateFrames                uint64                      `json:"lateFrames"`
	PlayoutTimeoutFrames      uint64                      `json:"playoutTimeoutFrames"`
	ConcealmentFrames         uint64                      `json:"concealmentFrames"`
	GapSkippedFrames          uint64                      `json:"gapSkippedFrames"`
	LastFrameID               uint32                      `json:"lastFrameId,omitempty"`
	LastConsumedFrameID       uint32                      `json:"lastConsumedFrameId,omitempty"`
	LastMediaTimestampUS      uint64                      `json:"lastMediaTimestampUs,omitempty"`
	LastReceivedAtUnixMs      int64                       `json:"lastReceivedAtUnixMs,omitempty"`
	LastConsumedAtUnixMs      int64                       `json:"lastConsumedAtUnixMs,omitempty"`
}

type AudioFrameSnapshot struct {
	Generation uint32
	FrameID    uint32
	Timestamp  uint64
	Config      bool
	Concealment bool
	Data        []byte
	receivedAt  time.Time
}

func validDesktopAudioConfig(config protocol.DesktopAudioConfig) bool {
	if config.Generation == 0 || strings.TrimSpace(config.Codec) == "" {
		return false
	}
	if config.SampleRate < 8_000 || config.SampleRate > 384_000 {
		return false
	}
	if config.Channels < 1 || config.Channels > 32 {
		return false
	}
	if config.BitsPerSample < 0 || config.BitsPerSample > 64 {
		return false
	}
	if config.FrameDurationMs < 0 || config.FrameDurationMs > 1000 {
		return false
	}
	return config.TargetBitrate >= 0
}

func (s *ControllerSession) applyAudioConfig(config protocol.DesktopAudioConfig) bool {
	if s == nil || !validDesktopAudioConfig(config) {
		return false
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	current := s.audioConfig
	if current.Generation != 0 && config.Generation < current.Generation {
		return false
	}
	if current.Generation != 0 && current.Generation == config.Generation && current != config {
		return false
	}
	if current.Generation != config.Generation {
		s.audioStats.GenerationDiscardedFrames += uint64(len(s.audioQueue))
		clear(s.audioQueue)
		s.audioQueue = s.audioQueue[:0]
		s.audioStats.LastConsumedFrameID = 0
		s.audioConcealmentRun = 0
	}
	s.audioConfig = config
	s.signalAudioLocked()
	return true
}

func (s *ControllerSession) AudioEnabled() bool {
	if s == nil {
		return false
	}
	return s.options.Audio == nil || *s.options.Audio
}

func (s *ControllerSession) AudioConfigSnapshot() protocol.DesktopAudioConfig {
	if s == nil {
		return protocol.DesktopAudioConfig{}
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	return s.audioConfig
}

func (s *ControllerSession) AudioDiagnosticsSnapshot() DesktopAudioDiagnostics {
	if s == nil {
		return DesktopAudioDiagnostics{}
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	stats := s.audioStats
	return DesktopAudioDiagnostics{
		Enabled:                   s.options.Audio == nil || *s.options.Audio,
		Config:                    s.audioConfig,
		QueueFrames:               len(s.audioQueue),
		QueueCapacity:             maxControllerAudioFrames,
		ReceivedFrames:            stats.ReceivedFrames,
		ReceivedBytes:             stats.ReceivedBytes,
		ConsumedFrames:            stats.ConsumedFrames,
		ConsumedBytes:             stats.ConsumedBytes,
		QueueDroppedFrames:        stats.QueueDroppedFrames,
		GenerationDiscardedFrames: stats.GenerationDiscardedFrames,
		RejectedFrames:            stats.RejectedFrames,
		ReorderedFrames:           stats.ReorderedFrames,
		DuplicateFrames:           stats.DuplicateFrames,
		LateFrames:                stats.LateFrames,
		PlayoutTimeoutFrames:      stats.PlayoutTimeoutFrames,
		ConcealmentFrames:         stats.ConcealmentFrames,
		GapSkippedFrames:          stats.GapSkippedFrames,
		LastFrameID:               stats.LastFrameID,
		LastConsumedFrameID:       stats.LastConsumedFrameID,
		LastMediaTimestampUS:      stats.LastMediaTimestampUS,
		LastReceivedAtUnixMs:      stats.LastReceivedAtUnixMs,
		LastConsumedAtUnixMs:      stats.LastConsumedAtUnixMs,
	}
}

func audioFrameMatchesConfig(
	frame *desktopmedia.EncodedFrame,
	config protocol.DesktopAudioConfig,
) bool {
	return frame != nil &&
		frame.Type == desktopmedia.MediaPacketAudio &&
		frame.StreamID == desktopmedia.MediaStreamAudioID &&
		config.Generation != 0 &&
		frame.Generation == config.Generation
}

func audioSnapshotFromEncodedFrame(frame *desktopmedia.EncodedFrame) AudioFrameSnapshot {
	if frame == nil {
		return AudioFrameSnapshot{}
	}
	return AudioFrameSnapshot{
		Generation: frame.Generation,
		FrameID:    frame.FrameID,
		Timestamp:  frame.Timestamp,
		Config:     frame.Config,
		Data:       append([]byte(nil), frame.Data...),
		receivedAt: time.Now(),
	}
}

func (s *ControllerSession) enqueueAudioFrame(frame *desktopmedia.EncodedFrame) bool {
	if s == nil || frame == nil {
		return false
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	if !audioFrameMatchesConfig(frame, s.audioConfig) {
		s.audioStats.RejectedFrames++
		return false
	}
	if last := s.audioStats.LastConsumedFrameID; last != 0 && frame.FrameID <= last {
		s.audioStats.RejectedFrames++
		s.audioStats.LateFrames++
		return false
	}
	for i := range s.audioQueue {
		if s.audioQueue[i].FrameID == frame.FrameID {
			s.audioStats.RejectedFrames++
			s.audioStats.DuplicateFrames++
			return false
		}
	}

	snapshot := audioSnapshotFromEncodedFrame(frame)
	s.audioStats.ReceivedFrames++
	s.audioStats.ReceivedBytes += uint64(len(snapshot.Data))
	s.audioStats.LastFrameID = snapshot.FrameID
	s.audioStats.LastMediaTimestampUS = snapshot.Timestamp
	s.audioStats.LastReceivedAtUnixMs = snapshot.receivedAt.UnixMilli()

	insertAt := len(s.audioQueue)
	for insertAt > 0 && s.audioQueue[insertAt-1].FrameID > snapshot.FrameID {
		insertAt--
	}
	if insertAt < len(s.audioQueue) {
		s.audioStats.ReorderedFrames++
	}
	s.audioQueue = append(s.audioQueue, AudioFrameSnapshot{})
	copy(s.audioQueue[insertAt+1:], s.audioQueue[insertAt:])
	s.audioQueue[insertAt] = snapshot
	if len(s.audioQueue) > maxControllerAudioFrames {
		dropped := s.audioQueue[0]
		s.audioStats.QueueDroppedFrames++
		if dropped.FrameID > s.audioStats.LastConsumedFrameID {
			s.audioStats.LastConsumedFrameID = dropped.FrameID
		}
		s.audioConcealmentRun = 0
		copy(s.audioQueue, s.audioQueue[1:])
		s.audioQueue[len(s.audioQueue)-1] = AudioFrameSnapshot{}
		s.audioQueue = s.audioQueue[:len(s.audioQueue)-1]
	}
	s.signalAudioLocked()
	return true
}

func (s *ControllerSession) signalAudioLocked() {
	if s == nil || s.audioNotify == nil {
		return
	}
	select {
	case s.audioNotify <- struct{}{}:
	default:
	}
}

func audioPlayoutDelay(config protocol.DesktopAudioConfig) time.Duration {
	period := time.Duration(config.FrameDurationMs) * time.Millisecond
	if period <= 0 {
		period = defaultControllerAudioFramePeriod
	}
	delay := 2 * period
	if delay < minControllerAudioPlayoutDelay {
		delay = minControllerAudioPlayoutDelay
	}
	if delay > maxControllerAudioPlayoutDelay {
		delay = maxControllerAudioPlayoutDelay
	}
	return delay
}

func nextAudioFrameID(last uint32) uint32 {
	if last == 0 {
		return 1
	}
	return last + 1
}

func audioConcealmentTimestamp(head AudioFrameSnapshot, expected uint32, config protocol.DesktopAudioConfig) uint64 {
	periodUS := uint64(config.FrameDurationMs) * 1000
	if periodUS == 0 || head.FrameID <= expected {
		return head.Timestamp
	}
	delta := uint64(head.FrameID-expected) * periodUS
	if head.Timestamp < delta {
		return 0
	}
	return head.Timestamp - delta
}

func (s *ControllerSession) audioFrameReadyLocked(now time.Time) (ready bool, wait time.Duration, timeout bool) {
	if s == nil || len(s.audioQueue) == 0 {
		return false, 0, false
	}
	if len(s.audioQueue) >= controllerAudioPlayoutFrames {
		return true, 0, false
	}
	oldest := s.audioQueue[0]
	if oldest.receivedAt.IsZero() {
		return true, 0, false
	}
	remaining := audioPlayoutDelay(s.audioConfig) - now.Sub(oldest.receivedAt)
	if remaining <= 0 {
		return true, 0, true
	}
	return false, remaining, false
}

func (s *ControllerSession) NextAudioFrame(ctx context.Context) (AudioFrameSnapshot, protocol.DesktopAudioConfig, error) {
	if s == nil {
		return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.audioMu.Lock()
		config := s.audioConfig
		for len(s.audioQueue) > 0 && s.audioQueue[0].Generation != config.Generation {
			copy(s.audioQueue, s.audioQueue[1:])
			s.audioQueue[len(s.audioQueue)-1] = AudioFrameSnapshot{}
			s.audioQueue = s.audioQueue[:len(s.audioQueue)-1]
			s.audioStats.RejectedFrames++
		}
		ready, wait, timedOut := s.audioFrameReadyLocked(time.Now())
		if ready && len(s.audioQueue) > 0 {
			head := s.audioQueue[0]
			expected := nextAudioFrameID(s.audioStats.LastConsumedFrameID)
			if config.Codec == protocol.DesktopAudioCodecOpus && head.FrameID > expected {
				if s.audioConcealmentRun < maxControllerAudioConcealmentFrames {
					s.audioConcealmentRun++
					s.audioStats.ConcealmentFrames++
					s.audioStats.LastConsumedFrameID = expected
					s.audioStats.LastConsumedAtUnixMs = time.Now().UnixMilli()
					frame := AudioFrameSnapshot{
						Generation:  config.Generation,
						FrameID:     expected,
						Timestamp:   audioConcealmentTimestamp(head, expected, config),
						Concealment: true,
					}
					s.audioMu.Unlock()
					return frame, config, nil
				}
				s.audioStats.GapSkippedFrames += uint64(head.FrameID - expected)
				s.audioStats.LastConsumedFrameID = head.FrameID - 1
				s.audioConcealmentRun = 0
			}

			frame := s.audioQueue[0]
			copy(s.audioQueue, s.audioQueue[1:])
			s.audioQueue[len(s.audioQueue)-1] = AudioFrameSnapshot{}
			s.audioQueue = s.audioQueue[:len(s.audioQueue)-1]
			if timedOut {
				s.audioStats.PlayoutTimeoutFrames++
			}
			s.audioStats.ConsumedFrames++
			s.audioStats.ConsumedBytes += uint64(len(frame.Data))
			s.audioStats.LastConsumedFrameID = frame.FrameID
			s.audioStats.LastConsumedAtUnixMs = time.Now().UnixMilli()
			s.audioConcealmentRun = 0
			s.audioMu.Unlock()
			frame.Data = append([]byte(nil), frame.Data...)
			return frame, config, nil
		}
		notify := s.audioNotify
		done := s.done
		s.audioMu.Unlock()
		if notify == nil {
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop audio queue is unavailable")
		}
		if wait <= 0 {
			select {
			case <-ctx.Done():
				return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, ctx.Err()
			case <-done:
				return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is closed")
			case <-notify:
			}
			continue
		}

		timer := time.NewTimer(wait)
		stopTimer := func() {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		select {
		case <-ctx.Done():
			stopTimer()
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, ctx.Err()
		case <-done:
			stopTimer()
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is closed")
		case <-notify:
			stopTimer()
		case <-timer.C:
		}
	}
}
