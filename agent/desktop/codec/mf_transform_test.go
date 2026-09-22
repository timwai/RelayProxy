package codec

import "testing"

func TestPackIndependentEncoderConfiguration(t *testing.T) {
	cfg, err := NormalizeVideoConfig(VideoConfig{Width: 1920, Height: 1080, FPS: 30, TargetBitrate: 12_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 1920 || cfg.Height != 1080 || cfg.FPS != 30 || cfg.TargetBitrate != 12_000_000 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
