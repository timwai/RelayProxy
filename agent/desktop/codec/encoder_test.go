package codec

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeVideoConfigDefaults(t *testing.T) {
	cfg, err := NormalizeVideoConfig(VideoConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultVideoConfig()
	if cfg != want {
		t.Fatalf("config=%+v want=%+v", cfg, want)
	}
}

func TestNormalizeVideoConfigRejectsOdd420Dimensions(t *testing.T) {
	_, err := NormalizeVideoConfig(VideoConfig{
		Width: 1279, Height: 720, FPS: 30, TargetBitrate: 6_000_000, KeyframeEvery: time.Second,
	})
	if !errors.Is(err, ErrInvalidVideoConfig) {
		t.Fatalf("err=%v", err)
	}
}

func TestRawFrameValidation(t *testing.T) {
	frame := RawFrame{Format: PixelFormatNV12, Width: 1280, Height: 720, Stride: 1280, Pix: make([]byte, 1280*720*3/2)}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	frame.Pix = frame.Pix[:100]
	if !errors.Is(frame.Validate(), ErrInvalidFrame) {
		t.Fatal("short NV12 frame accepted")
	}
}
