package desktop

import (
	"context"
	"log"
	"math"
	"time"

	"relayproxy/internal/protocol"
)

const (
	audioLossFeedbackInterval  = time.Second
	audioLossFeedbackMinFrames = 10
	audioLossFeedbackStep      = 5
)

type audioLossFeedbackState struct {
	initialized       bool
	receivedFrames    uint64
	concealmentFrames uint64
	gapSkippedFrames  uint64
	targetPercent     int
}

func quantizeAudioLossPercent(missing, total uint64) int {
	if total == 0 || missing == 0 {
		return 0
	}
	percent := float64(missing) * 100 / float64(total)
	target := int(math.Round(percent/float64(audioLossFeedbackStep))) * audioLossFeedbackStep
	if target < 0 {
		return 0
	}
	if target > 100 {
		return 100
	}
	return target
}

func (s *audioLossFeedbackState) reset() {
	if s == nil {
		return
	}
	*s = audioLossFeedbackState{}
}

func (s *audioLossFeedbackState) Observe(stats DesktopAudioDiagnostics) (int, bool) {
	if s == nil {
		return 0, false
	}
	if stats.Config.Codec != protocol.DesktopAudioCodecOpus || stats.Config.Generation == 0 {
		s.reset()
		return 0, false
	}
	if !s.initialized ||
		stats.ReceivedFrames < s.receivedFrames ||
		stats.ConcealmentFrames < s.concealmentFrames ||
		stats.GapSkippedFrames < s.gapSkippedFrames {
		s.initialized = true
		s.receivedFrames = stats.ReceivedFrames
		s.concealmentFrames = stats.ConcealmentFrames
		s.gapSkippedFrames = stats.GapSkippedFrames
		s.targetPercent = 0
		return 0, false
	}

	received := stats.ReceivedFrames - s.receivedFrames
	concealed := stats.ConcealmentFrames - s.concealmentFrames
	skipped := stats.GapSkippedFrames - s.gapSkippedFrames
	s.receivedFrames = stats.ReceivedFrames
	s.concealmentFrames = stats.ConcealmentFrames
	s.gapSkippedFrames = stats.GapSkippedFrames

	missing := concealed + skipped
	total := received + missing
	if total < audioLossFeedbackMinFrames {
		return s.targetPercent, false
	}
	target := quantizeAudioLossPercent(missing, total)
	if target == s.targetPercent {
		return target, false
	}
	s.targetPercent = target
	return target, true
}

func (s *ControllerSession) audioControlLoop(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(audioLossFeedbackInterval)
	defer ticker.Stop()

	var feedback audioLossFeedbackState
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			target, changed := feedback.Observe(s.AudioDiagnosticsSnapshot())
			if !changed {
				continue
			}
			control := protocol.DesktopAudioControl{ExpectedLossPercent: target}
			controlCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := s.conn.SendSessionMessage(controlCtx, protocol.DesktopSessionMessage{
				Type:         protocol.DesktopSessionAudioControl,
				AudioControl: &control,
			})
			cancel()
			if err != nil {
				log.Printf("[Desktop] audio loss control failed: %v", err)
				return
			}
			log.Printf("[Desktop] Opus expected packet loss=%d%%", target)
		}
	}
}
