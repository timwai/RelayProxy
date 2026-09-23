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

type testCapabilityCaptureSource struct {
	testCaptureSource
}

type testFPSCaptureSource struct {
	testCaptureSource
	fps int
}

func (s *testFPSCaptureSource) SetCaptureFPS(fps int) error {
	s.fps = fps
	return nil
}

type testSessionCaptureSource struct {
	testCaptureSource
	backend string
}

func (s *testSessionCaptureSource) BeginSession(context.Context, HostConfig) error { return nil }
func (s *testSessionCaptureSource) EndSession() error                              { return nil }
func (s *testSessionCaptureSource) CaptureBackend() string                         { return s.backend }

func (s *testCapabilityCaptureSource) DesktopCaptureCapabilities(context.Context) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability, error) {
	return []protocol.DesktopCaptureCapability{{Backend: "dxgi", Cursor: true}}, []protocol.DesktopDisplayCapability{{
		ID: "display-1", Name: "Primary", Width: 1920, Height: 1080, Primary: true,
	}, {
		ID: "display-2", Name: "Secondary", Width: 2560, Height: 1440,
	}}, nil
}

func TestFitRGBAPreservesAspectRatio(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	got := fitRGBA(src, 1280, 720)
	if got.Bounds().Dx() != 1280 || got.Bounds().Dy() != 720 {
		t.Fatalf("scaled size=%dx%d", got.Bounds().Dx(), got.Bounds().Dy())
	}
}

func TestApplyCaptureFPSDelegatesToOptionalController(t *testing.T) {
	source := &testFPSCaptureSource{}
	applyCaptureFPS(source, 15)
	if source.fps != 15 {
		t.Fatalf("capture fps=%d want=15", source.fps)
	}
}

func TestApplyCaptureFPSIgnoresUnsupportedSource(t *testing.T) {
	applyCaptureFPS(&testCaptureSource{}, 15)
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
		Quality:        protocol.DesktopQualityHigh,
		Resolution:     protocol.DesktopResolutionOptions{Mode: "fixed", Width: 1600, Height: 900},
		FPS:            24,
		MaxBitrate:     8_000_000,
		DisplayID:      "42",
		CaptureBackend: protocol.DesktopCaptureDXGI,
	})
	if cfg.MaxWidth != 1600 || cfg.MaxHeight != 900 || cfg.MaxFPS != 24 {
		t.Fatalf("unexpected media size/fps: %+v", cfg)
	}
	if cfg.JPEGQuality != 78 || cfg.MaxBitrate != 8_000_000 || cfg.DisplayID != "42" ||
		cfg.CaptureBackend != protocol.DesktopCaptureDXGI {
		t.Fatalf("unexpected quality/display/capture policy: %+v", cfg)
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

func TestHostCodecCapabilitiesAreCopied(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	host, err := NewHost(&testCaptureSource{frame: src}, DefaultHostConfig())
	if err != nil {
		t.Fatal(err)
	}
	input := []protocol.DesktopCodecCapability{{Codec: "h264", Encode: true}}
	host.SetCodecCapabilities(input)
	input[0].Codec = "mutated"
	first := host.CodecCapabilities()
	if len(first) != 1 || first[0].Codec != "h264" {
		t.Fatalf("host capability mutated through caller slice: %+v", first)
	}
	first[0].Codec = "changed"
	second := host.CodecCapabilities()
	if second[0].Codec != "h264" {
		t.Fatalf("host returned internal capability slice: %+v", second)
	}
}

func TestFitRGBAEven(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1366, 768))
	got := fitRGBAEven(src, 1280, 720)
	if got.Bounds().Dx()%2 != 0 || got.Bounds().Dy()%2 != 0 {
		t.Fatalf("H.264 frame is not even-sized: %v", got.Bounds())
	}
	if got.Bounds().Dx() > 1280 || got.Bounds().Dy() > 720 {
		t.Fatalf("H.264 frame exceeded bounds: %v", got.Bounds())
	}
}

func TestHostCanEncodeH264(t *testing.T) {
	host, err := NewHost(&testCaptureSource{frame: image.NewRGBA(image.Rect(0, 0, 2, 2))}, DefaultHostConfig())
	if err != nil {
		t.Fatal(err)
	}
	if host.canEncodeH264() {
		t.Fatal("H.264 unexpectedly enabled without capability")
	}
	host.SetCodecCapabilities([]protocol.DesktopCodecCapability{{Codec: "h264", Encode: true}})
	if !host.canEncodeH264() {
		t.Fatal("H.264 encode capability was ignored")
	}
}

func TestQueueLatestIntReplacesPendingValue(t *testing.T) {
	ch := make(chan int, 1)
	queueLatestInt(ch, 10)
	queueLatestInt(ch, 20)
	select {
	case got := <-ch:
		if got != 20 {
			t.Fatalf("latest queued value=%d want=20", got)
		}
	default:
		t.Fatal("latest queued value missing")
	}
}

func TestHostDesktopCapabilitiesIncludeDynamicCaptureSnapshot(t *testing.T) {
	source := &testCapabilityCaptureSource{
		testCaptureSource: testCaptureSource{frame: image.NewRGBA(image.Rect(0, 0, 2, 2))},
	}
	host, err := NewHost(source, DefaultHostConfig())
	if err != nil {
		t.Fatal(err)
	}
	host.SetCodecCapabilities([]protocol.DesktopCodecCapability{{Codec: "h264", Encode: true, Hardware: true}})
	caps := host.DesktopCapabilities(context.Background())
	if !caps.RelayDesktop || !caps.MultiMonitor {
		t.Fatalf("unexpected desktop summary: %+v", caps)
	}
	if caps.MaxWidth != maxJPEGWidth || caps.MaxHeight != maxJPEGHeight || caps.MaxFPS != maxJPEGFPS {
		t.Fatalf("unexpected host limits: %+v", caps)
	}
	if len(caps.Captures) != 1 || caps.Captures[0].Backend != "dxgi" || !caps.Captures[0].Cursor {
		t.Fatalf("capture capabilities=%+v", caps.Captures)
	}
	if len(caps.Displays) != 2 || caps.Displays[1].ID != "display-2" || caps.Displays[1].Width != 2560 {
		t.Fatalf("display capabilities=%+v", caps.Displays)
	}
	if len(caps.Codecs) != 1 || caps.Codecs[0].Codec != "h264" || !caps.Codecs[0].Hardware {
		t.Fatalf("codec capabilities=%+v", caps.Codecs)
	}
}

func TestValidateDesktopResolutionTarget(t *testing.T) {
	target, err := validateDesktopResolutionTarget(1280, 720, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if target.MaxWidth != 1280 || target.MaxHeight != 720 {
		t.Fatalf("target=%+v", target)
	}

	for _, test := range []struct {
		width, height int
	}{
		{0, 720},
		{1280, 0},
		{319, 180},
		{320, 179},
		{1921, 1080},
		{1920, 1081},
	} {
		if _, err := validateDesktopResolutionTarget(test.width, test.height, 1920, 1080); err == nil {
			t.Fatalf("invalid resolution %dx%d accepted", test.width, test.height)
		}
	}
}

func TestQueueLatestResolutionReplacesPendingValue(t *testing.T) {
	ch := make(chan desktopResolutionTarget, 1)
	queueLatestResolution(ch, desktopResolutionTarget{MaxWidth: 1920, MaxHeight: 1080})
	queueLatestResolution(ch, desktopResolutionTarget{MaxWidth: 1280, MaxHeight: 720})
	select {
	case got := <-ch:
		if got.MaxWidth != 1280 || got.MaxHeight != 720 {
			t.Fatalf("latest resolution=%+v", got)
		}
	default:
		t.Fatal("latest resolution update missing")
	}
}

func TestCaptureBackendNameReadsLiveSessionBackend(t *testing.T) {
	source := &testSessionCaptureSource{
		testCaptureSource: testCaptureSource{frame: image.NewRGBA(image.Rect(0, 0, 2, 2))},
		backend:           "dxgi",
	}
	if got := captureBackendName(source, "gdi"); got != "dxgi" {
		t.Fatalf("capture backend=%q want=dxgi", got)
	}
	source.backend = "gdi"
	if got := captureBackendName(source, "dxgi"); got != "gdi" {
		t.Fatalf("capture backend after fallback=%q want=gdi", got)
	}
	source.backend = ""
	if got := captureBackendName(source, "dxgi"); got != "dxgi" {
		t.Fatalf("capture backend fallback=%q want=dxgi", got)
	}
}

func TestResolveHostConfigDefaultsCaptureBackend(t *testing.T) {
	cfg := ResolveHostConfig(DefaultHostConfig(), protocol.RemoteDesktopConnectOptions{})
	if cfg.CaptureBackend != protocol.DesktopCaptureAuto {
		t.Fatalf("default capture backend=%q want=auto", cfg.CaptureBackend)
	}
}

func TestD3D11CaptureFrameLifetime(t *testing.T) {
	releases := 0
	frame := &D3D11CaptureFrame{
		Device: 1, Resource: 2,
		Width: 1920, Height: 1080,
		release: func() { releases++ },
	}
	if !frame.Valid() {
		t.Fatal("valid D3D11 capture frame rejected")
	}
	frame.Close()
	frame.Close()
	if releases != 1 {
		t.Fatalf("release count=%d want=1", releases)
	}
	frame.Resource = 0
	if frame.Valid() {
		t.Fatal("D3D11 capture frame with nil resource accepted")
	}
}
