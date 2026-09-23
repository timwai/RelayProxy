package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func opusAudioDiagnostics(received, concealed, skipped uint64) DesktopAudioDiagnostics {
	return DesktopAudioDiagnostics{
		Config: protocol.DesktopAudioConfig{
			Generation: 1,
			Codec:      protocol.DesktopAudioCodecOpus,
		},
		ReceivedFrames:    received,
		ConcealmentFrames: concealed,
		GapSkippedFrames:  skipped,
	}
}

func TestQuantizeAudioLossPercent(t *testing.T) {
	tests := []struct {
		missing uint64
		total   uint64
		want    int
	}{
		{0, 50, 0},
		{1, 50, 0},
		{2, 50, 5},
		{5, 50, 10},
		{25, 50, 50},
		{50, 50, 100},
	}
	for _, tc := range tests {
		if got := quantizeAudioLossPercent(tc.missing, tc.total); got != tc.want {
			t.Fatalf("missing=%d total=%d target=%d want=%d", tc.missing, tc.total, got, tc.want)
		}
	}
}

func TestAudioLossFeedbackUsesConcealedAndSkippedFrames(t *testing.T) {
	var state audioLossFeedbackState
	if _, changed := state.Observe(opusAudioDiagnostics(100, 0, 0)); changed {
		t.Fatal("initial baseline emitted control")
	}
	target, changed := state.Observe(opusAudioDiagnostics(145, 3, 2))
	if !changed || target != 10 {
		t.Fatalf("loss target=%d changed=%v want=10,true", target, changed)
	}
	if target, changed := state.Observe(opusAudioDiagnostics(195, 3, 2)); !changed || target != 0 {
		t.Fatalf("clean recovery target=%d changed=%v want=0,true", target, changed)
	}
}

func TestAudioLossFeedbackIgnoresSmallWindowsAndPCM(t *testing.T) {
	var state audioLossFeedbackState
	state.Observe(opusAudioDiagnostics(10, 0, 0))
	if target, changed := state.Observe(opusAudioDiagnostics(15, 1, 0)); changed || target != 0 {
		t.Fatalf("small window target=%d changed=%v", target, changed)
	}

	pcm := opusAudioDiagnostics(100, 10, 0)
	pcm.Config.Codec = protocol.DesktopAudioCodecPCMS16LE
	if target, changed := state.Observe(pcm); changed || target != 0 || state.initialized {
		t.Fatalf("PCM did not reset feedback: target=%d changed=%v state=%+v", target, changed, state)
	}
}
