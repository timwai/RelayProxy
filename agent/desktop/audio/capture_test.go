package audio

import (
	"testing"
	"time"
)

func TestNormalizeFrameDurationDefaultsToTwentyMilliseconds(t *testing.T) {
	cfg, duration, bytes, err := NormalizeFrameDuration(PCMConfig{
		SampleRate: 48_000,
		Channels:   2,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if duration != 20*time.Millisecond {
		t.Fatalf("duration=%s", duration)
	}
	if cfg.BitsPerSample != 16 {
		t.Fatalf("bits=%d", cfg.BitsPerSample)
	}
	if bytes != 3_840 {
		t.Fatalf("bytes=%d want=3840", bytes)
	}
}

func TestNormalizeFrameDurationRejectsFractionalSampleCount(t *testing.T) {
	_, _, _, err := NormalizeFrameDuration(PCMConfig{
		SampleRate:    44_100,
		Channels:      2,
		BitsPerSample: 16,
	}, time.Microsecond)
	if err == nil {
		t.Fatal("fractional-sample duration was accepted")
	}
}
