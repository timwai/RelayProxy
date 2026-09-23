package desktop

import (
	"context"
	"errors"
	"strings"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const maxControllerAudioFrames = 8

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
		return false
	}
	snapshot := audioSnapshotFromEncodedFrame(frame)
	if len(s.audioQueue) >= maxControllerAudioFrames {
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
				s.audioMu.Unlock()
				frame.Data = append([]byte(nil), frame.Data...)
				return frame, config, nil
			}
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
