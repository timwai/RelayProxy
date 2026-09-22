package desktop

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopmedia "relayproxy/internal/desktop"
)

type abrTestEncoder struct {
	configs        []desktopcodec.VideoConfig
	err            error
	forceIDRCalls  int
	closed         bool
	sequenceHeader []byte
}

func (e *abrTestEncoder) Encode(context.Context, desktopcodec.RawFrame) ([]desktopcodec.EncodedPacket, error) {
	return nil, nil
}
func (e *abrTestEncoder) ForceIDR(context.Context) error {
	e.forceIDRCalls++
	return nil
}
func (e *abrTestEncoder) Reconfigure(_ context.Context, cfg desktopcodec.VideoConfig) error {
	if e.err != nil {
		return e.err
	}
	e.configs = append(e.configs, cfg)
	return nil
}
func (e *abrTestEncoder) SequenceHeader() []byte {
	return append([]byte(nil), e.sequenceHeader...)
}
func (e *abrTestEncoder) Stats() desktopcodec.EncoderStats { return desktopcodec.EncoderStats{} }
func (e *abrTestEncoder) Close() error {
	e.closed = true
	return nil
}

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

type delayedDesktopPath struct {
	delay time.Duration
}

func (p *delayedDesktopPath) Name() string { return "test-delayed" }
func (p *delayedDesktopPath) Send(ctx context.Context, _ []byte) error {
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (p *delayedDesktopPath) Receive(context.Context) ([]byte, error) {
	return nil, errors.New("receive is not used")
}
func (p *delayedDesktopPath) Close() error { return nil }

func TestSendEncodedDesktopFrameMeasuresQueueDelay(t *testing.T) {
	conn := desktopmedia.NewMediaConn(nil, nil)
	conn.SetDatagramPath(&delayedDesktopPath{delay: 15 * time.Millisecond})
	defer conn.Close()

	frame := desktopmedia.EncodedFrame{
		SessionID:  1,
		StreamID:   1,
		Generation: 1,
		FrameID:    1,
		Timestamp:  1,
		KeyFrame:   true,
		Data:       []byte("frame"),
	}
	sequence := uint32(1)
	var queueDelayMs float64
	if err := sendEncodedDesktopFrame(
		context.Background(),
		conn,
		frame,
		1200,
		&sequence,
		&queueDelayMs,
	); err != nil {
		t.Fatal(err)
	}
	if queueDelayMs < 10 {
		t.Fatalf("queue delay=%vms did not include blocked media send", queueDelayMs)
	}
}

func TestH264ResolutionConfigPreservesSourceAspect(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1920, 1200))
	current := desktopcodec.VideoConfig{
		Width: 1920, Height: 1200, FPS: 30, TargetBitrate: 6_000_000,
		KeyframeEvery: 2 * time.Second,
	}
	frame, next, err := h264ResolutionConfig(
		src,
		desktopResolutionTarget{MaxWidth: 1280, MaxHeight: 720},
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if next.Width != 1152 || next.Height != 720 {
		t.Fatalf("next size=%dx%d want=1152x720", next.Width, next.Height)
	}
	if frame.Bounds().Dx() != next.Width || frame.Bounds().Dy() != next.Height {
		t.Fatalf("frame bounds=%v config=%dx%d", frame.Bounds(), next.Width, next.Height)
	}
	if next.Width%2 != 0 || next.Height%2 != 0 || next.TargetBitrate != current.TargetBitrate {
		t.Fatalf("resolution update changed unsafe fields: %+v", next)
	}
}

func TestOpenH264GenerationEncoderNormalizesAndForcesIDR(t *testing.T) {
	fake := &abrTestEncoder{sequenceHeader: []byte{1, 2, 3}}
	var opened desktopcodec.VideoConfig
	opener := func(_ context.Context, cfg desktopcodec.VideoConfig, preferHardware bool) (h264GenerationEncoder, error) {
		if !preferHardware {
			t.Fatal("generation encoder did not prefer hardware")
		}
		opened = cfg
		return fake, nil
	}
	encoder, normalized, header, err := openH264GenerationEncoder(context.Background(), desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 6_000_000,
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
	if len(header) != 3 || header[0] != 1 {
		t.Fatalf("sequence header=%v", header)
	}
	header[0] = 9
	if fake.sequenceHeader[0] != 1 {
		t.Fatal("sequence header leaked encoder storage")
	}
}

func TestH264DesktopVideoConfigCarriesGenerationBounds(t *testing.T) {
	cfg := desktopcodec.VideoConfig{
		Width: 1280, Height: 720, FPS: 30, TargetBitrate: 2_500_000,
		KeyframeEvery: 2 * time.Second,
	}
	got := h264DesktopVideoConfig(4, cfg, 1920, 1080, 8_000_000, "display-2", []byte{0, 0, 0, 1, 0x67})
	if got.Generation != 4 || got.Width != 1280 || got.Height != 720 || got.FPS != 30 {
		t.Fatalf("video config=%+v", got)
	}
	if got.TargetBitrate != 2_500_000 || got.MaxBitrate != 8_000_000 || got.DisplayID != "display-2" {
		t.Fatalf("video config bounds=%+v", got)
	}
	if got.MaxWidth != 1920 || got.MaxHeight != 1080 {
		t.Fatalf("video config resolution ceiling=%+v", got)
	}
	if got.Codec != "h264" || got.Chroma != "420" || got.BitDepth != 8 {
		t.Fatalf("video config codec fields=%+v", got)
	}
}

func TestNextDesktopMediaGeneration(t *testing.T) {
	next, err := nextDesktopMediaGeneration(0)
	if err != nil || next != 1 {
		t.Fatalf("generation 0 -> %d err=%v", next, err)
	}
	next, err = nextDesktopMediaGeneration(7)
	if err != nil || next != 8 {
		t.Fatalf("generation 7 -> %d err=%v", next, err)
	}
	if _, err := nextDesktopMediaGeneration(^uint32(0)); err == nil {
		t.Fatal("generation overflow was accepted")
	}
}

func TestH264RuntimeErrorPreservesGenerationAndCause(t *testing.T) {
	cause := errors.New("encode failed")
	err := &h264RuntimeError{Generation: 5, Err: cause}
	if err.Generation != 5 || !errors.Is(err, cause) {
		t.Fatalf("runtime error=%+v", err)
	}
}

func TestRawFrameFitsH264(t *testing.T) {
	frame := desktopcodec.RawFrame{
		Format: desktopcodec.PixelFormatBGRA,
		Width:  1920,
		Height: 1080,
		Stride: 1920*4 + 128,
		Pix:    make([]byte, (1920*4+128)*1080),
	}
	if !rawFrameFitsH264(frame, 1920, 1080) {
		t.Fatal("native-size padded BGRA frame was rejected")
	}
	if rawFrameFitsH264(frame, 1280, 720) {
		t.Fatal("oversized raw frame bypassed the scaler")
	}
	frame.Stride = 100
	if rawFrameFitsH264(frame, 1920, 1080) {
		t.Fatal("invalid BGRA stride was accepted")
	}
}

func TestEncoderStageMilliseconds(t *testing.T) {
	total, convert, codec := encoderStageMilliseconds(desktopcodec.EncoderStats{
		LastEncodeTime:  8 * time.Millisecond,
		LastConvertTime: 3 * time.Millisecond,
	})
	if total != 8 || convert != 3 || codec != 5 {
		t.Fatalf("encoder stages total=%v convert=%v codec=%v", total, convert, codec)
	}
	_, _, codec = encoderStageMilliseconds(desktopcodec.EncoderStats{
		LastEncodeTime:  2 * time.Millisecond,
		LastConvertTime: 3 * time.Millisecond,
	})
	if codec != 0 {
		t.Fatalf("negative codec duration was not clamped: %v", codec)
	}
}
