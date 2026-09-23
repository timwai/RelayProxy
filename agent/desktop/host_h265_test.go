package desktop

import (
	"context"
	"errors"
	"testing"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
)

func testH265SequenceHeader() []byte {
	return []byte{
		0, 0, 0, 1, 0x40, 0x01, 0x01,
		0, 0, 0, 1, 0x42, 0x01,
		0x01,
		0x01,
		0x60, 0x00, 0x00, 0x00,
		0xB0, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x5D,
		0, 0, 0, 1, 0x44, 0x01, 0x01,
	}
}

func TestOpenH265GenerationEncoderNormalizesAndForcesIDR(t *testing.T) {
	fake := &abrTestEncoder{sequenceHeader: testH265SequenceHeader()}
	var opened desktopcodec.VideoConfig
	opener := func(_ context.Context, cfg desktopcodec.VideoConfig, preferHardware bool) (h265GenerationEncoder, error) {
		if !preferHardware {
			t.Fatal("generation encoder did not prefer hardware")
		}
		opened = cfg
		return fake, nil
	}
	encoder, normalized, header, err := openH265GenerationEncoder(context.Background(), desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 4_000_000,
		KeyframeEvery: 2 * time.Second,
	}, opener)
	if err != nil {
		t.Fatal(err)
	}
	if encoder != fake || opened.Width != 1280 || normalized.Height != 720 {
		t.Fatalf("unexpected generation encoder/config: encoder=%T opened=%+v normalized=%+v", encoder, opened, normalized)
	}
	if fake.forceIDRCalls != 1 {
		t.Fatalf("ForceIDR calls=%d want=1", fake.forceIDRCalls)
	}
	if len(header) == 0 {
		t.Fatal("missing HEVC sequence header")
	}
	header[0] ^= 0xff
	if fake.sequenceHeader[0] != 0 {
		t.Fatal("sequence header leaked encoder storage")
	}
}

func TestH265DesktopVideoConfigCarriesGenerationBounds(t *testing.T) {
	cfg := desktopcodec.VideoConfig{
		Width: 1920, Height: 1080, FPS: 60, TargetBitrate: 8_000_000,
		KeyframeEvery: 2 * time.Second,
	}
	got := h265DesktopVideoConfig(
		7,
		cfg,
		3840,
		2160,
		20_000_000,
		"display-hevc",
		testH265SequenceHeader(),
	)
	if got.Generation != 7 || got.Codec != "h265" {
		t.Fatalf("video config=%+v", got)
	}
	if got.CodecString != "hvc1.1.6.L93.B0" {
		t.Fatalf("codec string=%q", got.CodecString)
	}
	if got.Width != 1920 || got.Height != 1080 || got.MaxWidth != 3840 || got.MaxHeight != 2160 {
		t.Fatalf("video config dimensions=%+v", got)
	}
	if got.TargetBitrate != 8_000_000 || got.MaxBitrate != 20_000_000 || got.FPS != 60 {
		t.Fatalf("video config bitrate/fps=%+v", got)
	}
	if got.Chroma != "420" || got.BitDepth != 8 || got.DisplayID != "display-hevc" {
		t.Fatalf("video config metadata=%+v", got)
	}
}

func TestOpenNextH265CPUGenerationAdvancesGeneration(t *testing.T) {
	opened := &abrTestEncoder{sequenceHeader: testH265SequenceHeader()}
	opener := func(context.Context, desktopcodec.VideoConfig, bool) (h265GenerationEncoder, error) {
		return opened, nil
	}
	cfg := desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 4_000_000,
		KeyframeEvery: 2 * time.Second,
	}
	encoder, normalized, header, generation, err := openNextH265CPUGeneration(
		context.Background(), 9, cfg, opener,
	)
	if err != nil {
		t.Fatal(err)
	}
	if encoder != opened || generation != 10 {
		t.Fatalf("encoder=%p generation=%d", encoder, generation)
	}
	if normalized.Width != 1280 || normalized.Height != 720 || normalized.TargetBitrate != 4_000_000 {
		t.Fatalf("normalized=%+v", normalized)
	}
	if len(header) == 0 || opened.forceIDRCalls != 1 {
		t.Fatalf("header=%d ForceIDR calls=%d", len(header), opened.forceIDRCalls)
	}
}

func TestH265RuntimeErrorPreservesGenerationAndCause(t *testing.T) {
	cause := errors.New("HEVC encode failed")
	err := &h265RuntimeError{Generation: 6, Err: cause}
	if err.Generation != 6 || !errors.Is(err, cause) {
		t.Fatalf("runtime error=%+v", err)
	}
}
