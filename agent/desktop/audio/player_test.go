package audio

import "testing"

func TestNormalizePCMConfig(t *testing.T) {
	got, err := NormalizePCMConfig(PCMConfig{SampleRate: 48_000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.BitsPerSample != 16 || got.BlockAlign() != 4 || got.BytesPerSecond() != 192_000 {
		t.Fatalf("normalized PCM config=%+v align=%d bytes/sec=%d", got, got.BlockAlign(), got.BytesPerSecond())
	}
}

func TestNormalizePCMConfigRejectsUnsupportedFormats(t *testing.T) {
	for _, cfg := range []PCMConfig{
		{SampleRate: 7_999, Channels: 2, BitsPerSample: 16},
		{SampleRate: 48_000, Channels: 0, BitsPerSample: 16},
		{SampleRate: 48_000, Channels: 2, BitsPerSample: 24},
	} {
		if _, err := NormalizePCMConfig(cfg); err == nil {
			t.Fatalf("invalid PCM config accepted: %+v", cfg)
		}
	}
}

func TestPCMConfigValidatePayload(t *testing.T) {
	cfg := PCMConfig{SampleRate: 48_000, Channels: 2, BitsPerSample: 16}
	if err := cfg.ValidatePayload(make([]byte, 3_840)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidatePayload(make([]byte, 3_839)); err == nil {
		t.Fatal("misaligned PCM payload was accepted")
	}
}
