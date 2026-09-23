package protocol

import (
	"encoding/json"
	"testing"
)

func TestDesktopSessionAudioConfigRoundTrip(t *testing.T) {
	message := DesktopSessionMessage{
		Type: DesktopSessionAudioConfig,
		AudioConfig: &DesktopAudioConfig{
			Generation:      3,
			Codec:           "pcm_s16le",
			SampleRate:      48000,
			Channels:        2,
			BitsPerSample:   16,
			FrameDurationMs: 20,
			TargetBitrate:   1536000,
		},
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DesktopSessionMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != DesktopSessionAudioConfig || decoded.AudioConfig == nil {
		t.Fatalf("decoded message=%+v", decoded)
	}
	got := decoded.AudioConfig
	if got.Generation != 3 || got.Codec != "pcm_s16le" || got.SampleRate != 48000 ||
		got.Channels != 2 || got.BitsPerSample != 16 || got.FrameDurationMs != 20 ||
		got.TargetBitrate != 1536000 {
		t.Fatalf("decoded audio config=%+v", got)
	}
}
