package desktop

import (
	"context"
	"errors"
	"testing"

	desktopcodec "relayproxy/agent/desktop/codec"
)

type abrTestEncoder struct {
	configs []desktopcodec.VideoConfig
	err     error
}

func (e *abrTestEncoder) Encode(context.Context, desktopcodec.RawFrame) ([]desktopcodec.EncodedPacket, error) {
	return nil, nil
}
func (e *abrTestEncoder) ForceIDR(context.Context) error { return nil }
func (e *abrTestEncoder) Reconfigure(_ context.Context, cfg desktopcodec.VideoConfig) error {
	if e.err != nil {
		return e.err
	}
	e.configs = append(e.configs, cfg)
	return nil
}
func (e *abrTestEncoder) Stats() desktopcodec.EncoderStats { return desktopcodec.EncoderStats{} }
func (e *abrTestEncoder) Close() error                    { return nil }

func TestReconfigureH264BitrateClampsToSessionBounds(t *testing.T) {
	encoder := &abrTestEncoder{}
	current := desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 4_000_000,
	}
	next, err := reconfigureH264Bitrate(context.Background(), encoder, current, 8_000_000, 6_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetBitrate != 6_000_000 {
		t.Fatalf("target=%d", next.TargetBitrate)
	}
	if len(encoder.configs) != 1 || encoder.configs[0].TargetBitrate != 6_000_000 {
		t.Fatalf("configs=%+v", encoder.configs)
	}

	next, err = reconfigureH264Bitrate(context.Background(), encoder, next, 100_000, 6_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetBitrate != 250_000 {
		t.Fatalf("floor target=%d", next.TargetBitrate)
	}
}

func TestReconfigureH264BitrateKeepsCurrentOnFailure(t *testing.T) {
	encoder := &abrTestEncoder{err: desktopcodec.ErrEncoderControlUnsupported}
	current := desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 4_000_000,
	}
	next, err := reconfigureH264Bitrate(context.Background(), encoder, current, 2_000_000, 6_000_000)
	if !errors.Is(err, desktopcodec.ErrEncoderControlUnsupported) {
		t.Fatalf("err=%v", err)
	}
	if next.TargetBitrate != current.TargetBitrate {
		t.Fatalf("target changed on failure: %d -> %d", current.TargetBitrate, next.TargetBitrate)
	}
}

func TestReconfigureH264BitrateSkipsUnchangedTarget(t *testing.T) {
	encoder := &abrTestEncoder{}
	current := desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 4_000_000,
	}
	next, err := reconfigureH264Bitrate(context.Background(), encoder, current, 4_000_000, 6_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if next.TargetBitrate != current.TargetBitrate || len(encoder.configs) != 0 {
		t.Fatalf("unchanged target reconfigured: next=%+v configs=%+v", next, encoder.configs)
	}
}
