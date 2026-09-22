package desktop

import (
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const h264RecoveryRetryFrames = 15

type h264RecoveryState struct {
	generation       uint32
	lastFrameID      uint32
	waitingKeyFrame  bool
	requestPending   bool
	droppedSinceIDR  int
}

func (s *h264RecoveryState) Reset() {
	*s = h264RecoveryState{}
}

func (s *h264RecoveryState) Observe(frame *desktopmedia.EncodedFrame, config protocol.DesktopVideoConfig) (accept bool, requestIDR bool) {
	if frame == nil {
		return false, false
	}
	if config.Codec != "h264" {
		s.Reset()
		return true, false
	}
	if frame.Generation != s.generation {
		s.generation = frame.Generation
		s.lastFrameID = 0
		s.waitingKeyFrame = true
		s.requestPending = false
		s.droppedSinceIDR = 0
	}
	if s.lastFrameID != 0 && frame.FrameID <= s.lastFrameID {
		return false, false
	}

	first := s.lastFrameID == 0
	gap := !first && frame.FrameID != s.lastFrameID+1
	s.lastFrameID = frame.FrameID

	if frame.KeyFrame {
		s.waitingKeyFrame = false
		s.requestPending = false
		s.droppedSinceIDR = 0
		return true, false
	}
	if first || gap {
		s.waitingKeyFrame = true
	}
	if !s.waitingKeyFrame {
		return true, false
	}

	if !s.requestPending {
		s.requestPending = true
		s.droppedSinceIDR = 0
		return false, true
	}
	s.droppedSinceIDR++
	if s.droppedSinceIDR >= h264RecoveryRetryFrames {
		s.droppedSinceIDR = 0
		return false, true
	}
	return false, false
}

func (s *h264RecoveryState) ForceRecovery() bool {
	s.waitingKeyFrame = true
	if s.requestPending {
		return false
	}
	s.requestPending = true
	s.droppedSinceIDR = 0
	return true
}

func (s *h264RecoveryState) RequestFailed() {
	s.requestPending = false
	s.droppedSinceIDR = 0
}
