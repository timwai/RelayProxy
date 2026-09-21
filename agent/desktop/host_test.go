package desktop

import (
	"context"
	"image"
	"testing"

	"relayproxy/internal/protocol"
)

type testCaptureSource struct {
	frame *image.RGBA
}

func (s *testCaptureSource) Capture(context.Context) (*image.RGBA, error) { return s.frame, nil }
func (s *testCaptureSource) Close() error                                 { return nil }

func TestFitRGBAPreservesAspectRatio(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	got := fitRGBA(src, 1280, 720)
	if got.Bounds().Dx() != 1280 || got.Bounds().Dy() != 720 {
		t.Fatalf("scaled size=%dx%d", got.Bounds().Dx(), got.Bounds().Dy())
	}
}

func TestHostCaptureJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 20, 80, 140, 255
	}
	host, err := NewHost(&testCaptureSource{frame: src}, HostConfig{MaxFPS: 5, MaxWidth: 320, MaxHeight: 180, JPEGQuality: 70})
	if err != nil {
		t.Fatal(err)
	}
	data, err := host.captureJPEG(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatal("capture did not produce JPEG")
	}
}

func TestResolveHostConfigQualityAndExplicitOverrides(t *testing.T) {
	cfg := ResolveHostConfig(DefaultHostConfig(), protocol.RemoteDesktopConnectOptions{
		Quality:    protocol.DesktopQualityHigh,
		Resolution: protocol.DesktopResolutionOptions{Mode: "fixed", Width: 1600, Height: 900},
		FPS:        24,
		MaxBitrate: 8_000_000,
	})
	if cfg.MaxWidth != 1600 || cfg.MaxHeight != 900 || cfg.MaxFPS != 24 {
		t.Fatalf("unexpected media size/fps: %+v", cfg)
	}
	if cfg.JPEGQuality != 78 || cfg.MaxBitrate != 8_000_000 {
		t.Fatalf("unexpected quality policy: %+v", cfg)
	}
}

func TestResolveHostConfigClampsUnsafeValues(t *testing.T) {
	cfg := ResolveHostConfig(DefaultHostConfig(), protocol.RemoteDesktopConnectOptions{
		Resolution: protocol.DesktopResolutionOptions{Mode: "fixed", Width: 9000, Height: 9000},
		FPS:        240,
		MaxBitrate: 500_000_000,
	})
	if cfg.MaxWidth != maxJPEGWidth || cfg.MaxHeight != maxJPEGHeight || cfg.MaxFPS != maxJPEGFPS || cfg.MaxBitrate != maxJPEGBitrate {
		t.Fatalf("unsafe values were not clamped: %+v", cfg)
	}
}
