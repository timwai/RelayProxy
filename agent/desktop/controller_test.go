package desktop

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"testing"

	desktopadapt "relayproxy/agent/desktop/adapt"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func TestSnapshotFromEncodedFrameH264(t *testing.T) {
	frame := &desktopmedia.EncodedFrame{Generation: 3, FrameID: 7, Timestamp: 1234, KeyFrame: true, Data: []byte{0, 0, 0, 1, 0x65}}
	got, ok := snapshotFromEncodedFrame(frame, protocol.DesktopVideoConfig{
		Generation: 3, Codec: "h264", CodecString: "avc1.42E01F", Width: 1280, Height: 720,
	}, true)
	if !ok || got.Generation != 3 || got.MimeType != "video/h264" || got.Codec != "avc1.42E01F" || !got.KeyFrame || got.Width != 1280 {
		t.Fatalf("snapshot=%+v ok=%v", got, ok)
	}
}

func TestSnapshotFromEncodedFrameH265(t *testing.T) {
	frame := &desktopmedia.EncodedFrame{Generation: 4, FrameID: 9, Timestamp: 2345, KeyFrame: true, Data: []byte{0, 0, 0, 1, 0x26, 0x01}}
	got, ok := snapshotFromEncodedFrame(frame, protocol.DesktopVideoConfig{
		Generation: 4, Codec: "h265", CodecString: "hvc1", Width: 1920, Height: 1080,
	}, true)
	if !ok || got.Generation != 4 || got.MimeType != "video/h265" || got.Codec != "hvc1" || !got.KeyFrame || got.Width != 1920 {
		t.Fatalf("snapshot=%+v ok=%v", got, ok)
	}
}

func TestSnapshotFromEncodedFrameLegacyJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 8))
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	got, ok := snapshotFromEncodedFrame(&desktopmedia.EncodedFrame{FrameID: 1, Data: out.Bytes()}, protocol.DesktopVideoConfig{}, false)
	if !ok || got.MimeType != "image/jpeg" || got.Width != 16 || got.Height != 8 {
		t.Fatalf("snapshot=%+v ok=%v", got, ok)
	}
}

func TestLatestCursorOmitsKnownShape(t *testing.T) {
	session := &ControllerSession{
		done: make(chan struct{}),
		latestCursor: protocol.DesktopCursorState{
			Sequence: 3, CursorID: "cursor-a", Visible: true, PNG: []byte{1, 2, 3},
		},
	}
	cursor, ok := session.LatestCursor("")
	if !ok || len(cursor.PNG) != 3 {
		t.Fatalf("cursor=%+v ok=%v", cursor, ok)
	}
	cursor.PNG[0] = 9
	if session.latestCursor.PNG[0] != 1 {
		t.Fatal("LatestCursor returned internal PNG storage")
	}
	cursor, ok = session.LatestCursor("cursor-a")
	if !ok || len(cursor.PNG) != 0 {
		t.Fatalf("known-shape cursor=%+v ok=%v", cursor, ok)
	}
}

func TestLatestClipboardSkipsKnownSequence(t *testing.T) {
	session := &ControllerSession{
		done: make(chan struct{}),
		latestClipboard: protocol.DesktopClipboardState{
			Sequence: 4,
			Text:     "hello",
		},
	}
	clipboard, ok := session.LatestClipboard(0)
	if !ok || clipboard.Sequence != 4 || clipboard.Text != "hello" {
		t.Fatalf("clipboard=%+v ok=%v", clipboard, ok)
	}
	if _, ok := session.LatestClipboard(4); ok {
		t.Fatal("known clipboard sequence was returned again")
	}
}

func TestConfigureABRForInterFrameCodecs(t *testing.T) {
	session := &ControllerSession{
		options: protocol.RemoteDesktopConnectOptions{Scene: protocol.DesktopSceneGaming},
	}
	session.configureABR(protocol.DesktopVideoConfig{
		Codec: "jpeg", TargetBitrate: 6_000_000, MaxBitrate: 6_000_000,
	})
	if decision := session.abrDecision(protocol.DesktopSessionStats{LossPercent: 10}); decision.Changed {
		t.Fatalf("JPEG unexpectedly adapted: %+v", decision)
	}

	session.configureABR(protocol.DesktopVideoConfig{
		Codec: "h264", TargetBitrate: 6_000_000, MaxBitrate: 6_000_000,
	})
	decision := session.abrDecision(protocol.DesktopSessionStats{LossPercent: 5})
	if !decision.Changed || decision.TargetBitrate >= 6_000_000 {
		t.Fatalf("H.264 was not adapted: %+v", decision)
	}

	session.configureABR(protocol.DesktopVideoConfig{
		Codec: "h265", TargetBitrate: 6_000_000, MaxBitrate: 6_000_000,
	})
	decision = session.abrDecision(protocol.DesktopSessionStats{LossPercent: 5})
	if !decision.Changed || decision.TargetBitrate >= 6_000_000 {
		t.Fatalf("H.265 was not adapted: %+v", decision)
	}
}

func TestVideoConfigSnapshotExposesSelectedDisplay(t *testing.T) {
	session := &ControllerSession{
		videoConfig: protocol.DesktopVideoConfig{
			Codec: "h264", Width: 1920, Height: 1080, DisplayID: "20",
		},
	}
	got := session.VideoConfigSnapshot()
	if got.Codec != "h264" || got.DisplayID != "20" || got.Width != 1920 || got.Height != 1080 {
		t.Fatalf("video config snapshot=%+v", got)
	}
}

func TestFrameMatchesVideoConfigGeneration(t *testing.T) {
	config := protocol.DesktopVideoConfig{Generation: 4, Codec: "h264"}
	if !frameMatchesVideoConfig(&desktopmedia.EncodedFrame{Generation: 4}, config, true) {
		t.Fatal("matching generation was rejected")
	}
	if frameMatchesVideoConfig(&desktopmedia.EncodedFrame{Generation: 3}, config, true) {
		t.Fatal("stale generation was accepted")
	}
	if frameMatchesVideoConfig(&desktopmedia.EncodedFrame{Generation: 5}, config, true) {
		t.Fatal("future generation was accepted before its config")
	}
	if !frameMatchesVideoConfig(&desktopmedia.EncodedFrame{Generation: 9}, protocol.DesktopVideoConfig{}, false) {
		t.Fatal("legacy unconfigured frame was rejected")
	}
}

func TestApplyVideoConfigRejectsStaleGenerationAndClearsLatest(t *testing.T) {
	session := &ControllerSession{
		videoConfig: protocol.DesktopVideoConfig{Generation: 1, Codec: "h264", Width: 1920, Height: 1080},
		latest:      FrameSnapshot{Generation: 1, Sequence: 8, Data: []byte{1}},
	}
	if !session.applyVideoConfig(protocol.DesktopVideoConfig{Generation: 2, Codec: "h264", Width: 1280, Height: 720}) {
		t.Fatal("new video generation was rejected")
	}
	if session.latest.Sequence != 0 || len(session.latest.Data) != 0 {
		t.Fatalf("previous generation frame was not cleared: %+v", session.latest)
	}
	if session.applyVideoConfig(protocol.DesktopVideoConfig{Generation: 1, Codec: "h264", Width: 1920, Height: 1080}) {
		t.Fatal("stale video config was accepted")
	}
	got := session.VideoConfigSnapshot()
	if got.Generation != 2 || got.Width != 1280 || got.Height != 720 {
		t.Fatalf("current video config regressed: %+v", got)
	}
}

func TestVideoConfigRequiresABRReset(t *testing.T) {
	base := protocol.DesktopVideoConfig{
		Generation: 1, Codec: "h264", Width: 1920, Height: 1080,
		FPS: 30, TargetBitrate: 6_000_000, MaxBitrate: 12_000_000,
	}
	if !videoConfigRequiresABRReset(protocol.DesktopVideoConfig{}, base) {
		t.Fatal("initial video config did not initialize ABR")
	}

	nextGeneration := base
	nextGeneration.Generation = 2
	nextGeneration.Width = 1280
	nextGeneration.Height = 720
	nextGeneration.TargetBitrate = 3_000_000
	if videoConfigRequiresABRReset(base, nextGeneration) {
		t.Fatal("resolution-only generation switch reset ABR")
	}

	nextFPS := nextGeneration
	nextFPS.FPS = 24
	if !videoConfigRequiresABRReset(base, nextFPS) {
		t.Fatal("FPS negotiation change did not reset ABR")
	}

	nextMaxBitrate := nextGeneration
	nextMaxBitrate.MaxBitrate = 8_000_000
	if !videoConfigRequiresABRReset(base, nextMaxBitrate) {
		t.Fatal("bitrate ceiling change did not reset ABR")
	}

	nextCodec := nextGeneration
	nextCodec.Codec = "jpeg"
	if !videoConfigRequiresABRReset(base, nextCodec) {
		t.Fatal("codec change did not reset ABR")
	}
}

func TestRequestResolutionRejectsUnsupportedSession(t *testing.T) {
	session := &ControllerSession{
		done: make(chan struct{}),
		videoConfig: protocol.DesktopVideoConfig{
			Generation: 1,
			Codec:      "jpeg",
			Width:      1280,
			Height:     720,
			MaxWidth:   1920,
			MaxHeight:  1080,
		},
	}
	if err := session.RequestResolution(context.Background(), 960, 540); err == nil {
		t.Fatal("JPEG session accepted runtime resolution switching")
	}

	session.videoConfig.Codec = "h264"
	if err := session.RequestResolution(context.Background(), 2560, 1440); err == nil {
		t.Fatal("resolution above negotiated ceiling was accepted")
	}
}

func TestVideoConfigResolutionScaleAndBounds(t *testing.T) {
	config := protocol.DesktopVideoConfig{
		Codec: "h264", Width: 1280, Height: 720, MaxWidth: 1920, MaxHeight: 1080,
	}
	if got := videoConfigResolutionScale(config); got != 67 {
		t.Fatalf("resolution scale=%d want=67", got)
	}
	width, height, ok := resolutionBoundsForScale(config, 75)
	if !ok || width != 1440 || height != 810 {
		t.Fatalf("75%% bounds=%dx%d ok=%v", width, height, ok)
	}
	width, height, ok = resolutionBoundsForScale(config, 50)
	if !ok || width != 960 || height != 540 {
		t.Fatalf("50%% bounds=%dx%d ok=%v", width, height, ok)
	}
}

func TestABRVideoControlOnlyCarriesResolutionOnTierChange(t *testing.T) {
	config := protocol.DesktopVideoConfig{
		Codec: "h264", Width: 1920, Height: 1080, MaxWidth: 1920, MaxHeight: 1080,
	}
	bitrateOnly := abrVideoControl(config, desktopadapt.MediaDecision{
		TargetBitrate: 4_000_000,
		TargetFPS:     30,
		Changed:       true,
	})
	if bitrateOnly.TargetWidth != 0 || bitrateOnly.TargetHeight != 0 {
		t.Fatalf("bitrate-only ABR unexpectedly rebuilt resolution: %+v", bitrateOnly)
	}

	resolution := abrVideoControl(config, desktopadapt.MediaDecision{
		TargetBitrate:         2_000_000,
		TargetFPS:             22,
		TargetResolutionScale: 75,
		ResolutionChanged:     true,
		Changed:               true,
	})
	if resolution.TargetWidth != 1440 || resolution.TargetHeight != 810 {
		t.Fatalf("resolution ABR control=%+v", resolution)
	}
}

func TestSyncABRResolutionTracksManualGeneration(t *testing.T) {
	session := &ControllerSession{
		options: protocol.RemoteDesktopConnectOptions{Scene: protocol.DesktopSceneOffice},
	}
	initial := protocol.DesktopVideoConfig{
		Generation: 1, Codec: "h264", Width: 1920, Height: 1080,
		MaxWidth: 1920, MaxHeight: 1080, FPS: 30,
		TargetBitrate: 8_000_000, MaxBitrate: 8_000_000,
	}
	session.configureABR(initial)
	if session.abr == nil || session.abr.TargetResolutionScale() != 100 {
		t.Fatalf("initial ABR resolution=%v", session.abr)
	}

	manual := initial
	manual.Generation = 2
	manual.Width = 1280
	manual.Height = 720
	session.syncABRResolution(manual)
	if got := session.abr.TargetResolutionScale(); got != 67 {
		t.Fatalf("manual generation resolution scale=%d want=67", got)
	}
}

func TestDesktopResolutionModeFollowsViewport(t *testing.T) {
	for _, mode := range []string{"", "auto", "follow_viewport", " FOLLOW_VIEWPORT "} {
		if !desktopResolutionModeFollowsViewport(mode) {
			t.Fatalf("mode %q should follow viewport", mode)
		}
	}
	for _, mode := range []string{"fixed", "native", "manual"} {
		if desktopResolutionModeFollowsViewport(mode) {
			t.Fatalf("mode %q unexpectedly follows viewport", mode)
		}
	}

	session := &ControllerSession{followViewport: true}
	if !session.ViewportFollowEnabled() {
		t.Fatal("controller session lost viewport-follow state")
	}
	session.followViewport = false
	if session.ViewportFollowEnabled() {
		t.Fatal("controller session reported disabled viewport follow as enabled")
	}
}

func TestRequestDisplayRejectsVirtualDesktopForPerDisplayBackends(t *testing.T) {
	for _, backend := range []protocol.DesktopCaptureBackend{
		protocol.DesktopCaptureDXGI,
		protocol.DesktopCaptureWGC,
	} {
		session := &ControllerSession{
			done: make(chan struct{}),
			options: protocol.RemoteDesktopConnectOptions{
				CaptureBackend: backend,
			},
			videoConfig: protocol.DesktopVideoConfig{
				Generation: 1,
				Codec:      "h264",
				DisplayID:  "20",
			},
		}
		if err := session.RequestDisplay(context.Background(), "20"); err != nil {
			t.Fatalf("%s same-display request failed: %v", backend, err)
		}
		if err := session.RequestDisplay(context.Background(), ""); err == nil {
			t.Fatalf("%s accepted virtual-desktop switch", backend)
		}
	}
}


func TestCaptureBackendPreferenceDefaultsToAuto(t *testing.T) {
	if got := (&ControllerSession{}).CaptureBackendPreference(); got != protocol.DesktopCaptureAuto {
		t.Fatalf("default capture backend=%q want=auto", got)
	}
	session := &ControllerSession{
		options: protocol.RemoteDesktopConnectOptions{CaptureBackend: protocol.DesktopCaptureWGC},
	}
	if got := session.CaptureBackendPreference(); got != protocol.DesktopCaptureWGC {
		t.Fatalf("capture backend=%q want=wgc", got)
	}
}
