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

func TestBitrateOnlyReconfigure(t *testing.T) {
	current := DefaultVideoConfig()
	next := current
	next.TargetBitrate = current.TargetBitrate / 2
	if !bitrateOnlyReconfigure(current, next) {
		t.Fatal("bitrate-only change should be supported in place")
	}
	next.Width = current.Width + 2
	if bitrateOnlyReconfigure(current, next) {
		t.Fatal("resolution change must require encoder rebuild")
	}
}

func TestRawFrameValidationAcceptsPaddedBGRA(t *testing.T) {
	frame := RawFrame{
		Format: PixelFormatBGRA,
		Width:  4,
		Height: 2,
		Stride: 24,
		Pix:    make([]byte, 48),
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	frame.Stride = 15
	if !errors.Is(frame.Validate(), ErrInvalidFrame) {
		t.Fatal("BGRA frame with short stride accepted")
	}
}

func TestD3D11EncodeFrameValidation(t *testing.T) {
	frame := D3D11EncodeFrame{
		Resource:  1,
		Width:     1280,
		Height:    720,
		Timestamp: time.Second,
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}

	frame.Resource = 0
	if !errors.Is(frame.Validate(), ErrInvalidFrame) {
		t.Fatal("nil D3D11 resource accepted")
	}

	frame.Resource = 1
	frame.Width = 1279
	if !errors.Is(frame.Validate(), ErrInvalidFrame) {
		t.Fatal("odd D3D11 width accepted")
	}
}
