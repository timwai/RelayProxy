package desktop

import (
	"context"
	"errors"
	"strings"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const maxControllerAudioFrames = 8

type audioRuntimeCounters struct {
	ReceivedFrames            uint64
	ReceivedBytes             uint64
	ConsumedFrames            uint64
	ConsumedBytes             uint64
	QueueDroppedFrames        uint64
	GenerationDiscardedFrames uint64
	RejectedFrames            uint64
	LastFrameID               uint32
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
	LastFrameID               uint32                      `json:"lastFrameId,omitempty"`
	LastMediaTimestampUS      uint64                      `json:"lastMediaTimestampUs,omitempty"`
	LastReceivedAtUnixMs      int64                       `json:"lastReceivedAtUnixMs,omitempty"`
	LastConsumedAtUnixMs      int64                       `json:"lastConsumedAtUnixMs,omitempty"`
}

type AudioFrameSnapshot struct {
	Generation uint32
	FrameID    uint32
	Timestamp  uint64
	Config     bool
	Data       []byte
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
		LastFrameID:               stats.LastFrameID,
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
	snapshot := audioSnapshotFromEncodedFrame(frame)
	s.audioStats.ReceivedFrames++
	s.audioStats.ReceivedBytes += uint64(len(snapshot.Data))
	s.audioStats.LastFrameID = snapshot.FrameID
	s.audioStats.LastMediaTimestampUS = snapshot.Timestamp
	s.audioStats.LastReceivedAtUnixMs = time.Now().UnixMilli()
	if len(s.audioQueue) >= maxControllerAudioFrames {
		s.audioStats.QueueDroppedFrames++
		copy(s.audioQueue, s.audioQueue[1:])
		s.audioQueue[len(s.audioQueue)-1] = snapshot
	} else {
		s.audioQueue = append(s.audioQueue, snapshot)
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

func (s *ControllerSession) NextAudioFrame(ctx context.Context) (AudioFrameSnapshot, protocol.DesktopAudioConfig, error) {
	if s == nil {
		return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is unavailable")
	}
	for {
		s.audioMu.Lock()
		for len(s.audioQueue) > 0 {
			frame := s.audioQueue[0]
			copy(s.audioQueue, s.audioQueue[1:])
			s.audioQueue[len(s.audioQueue)-1] = AudioFrameSnapshot{}
			s.audioQueue = s.audioQueue[:len(s.audioQueue)-1]
			config := s.audioConfig
			if frame.Generation == config.Generation {
				s.audioStats.ConsumedFrames++
				s.audioStats.ConsumedBytes += uint64(len(frame.Data))
				s.audioStats.LastConsumedAtUnixMs = time.Now().UnixMilli()
				s.audioMu.Unlock()
				frame.Data = append([]byte(nil), frame.Data...)
				return frame, config, nil
			}
			s.audioStats.RejectedFrames++
		}
		notify := s.audioNotify
		s.audioMu.Unlock()
		if notify == nil {
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop audio queue is unavailable")
		}
		select {
		case <-ctx.Done():
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, ctx.Err()
		case <-s.done:
			return AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is closed")
		case <-notify:
		}
	}
}
