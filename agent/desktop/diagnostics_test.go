package desktop

import (
	"testing"
	"time"

	desktopadapt "relayproxy/agent/desktop/adapt"
	"relayproxy/internal/protocol"
)

func TestSessionDiagnosticsRecorderKeepsBoundedRecentSamples(t *testing.T) {
	start := time.Unix(100, 0)
	recorder := newSessionDiagnosticsRecorder("target-a", protocol.RemoteDesktopConnectOptions{
		Scene:      protocol.DesktopSceneOffice,
		Codec:      "h264",
		FPS:        30,
		MaxBitrate: 8_000_000,
	}, start)

	for i := 0; i < desktopDiagnosticsMaxSamples+3; i++ {
		recorder.Record(
			start.Add(time.Duration(i)*500*time.Millisecond),
			protocol.DesktopVideoConfig{
				Generation: uint32(1 + i/100),
				Codec:      "h264",
				Width:      1920,
				Height:     1080,
			},
			protocol.DesktopSessionStats{
				Path:          "udp_p2p",
				ActualBitrate: int64(i),
			},
			DesktopAudioDiagnostics{},
			desktopadapt.MediaDecision{
				Changed:       i%5 == 0,
				TargetBitrate: 4_000_000,
				TargetFPS:     30,
				Reason:        "test",
			},
		)
	}

	report := recorder.Report(start.Add(11*time.Minute), protocol.DesktopVideoConfig{
		Generation: 13,
		Codec:      "h264",
		Width:      1280,
		Height:     720,
	}, protocol.DesktopSessionStats{Path: "relay"}, DesktopAudioDiagnostics{})

	if report.SchemaVersion != desktopDiagnosticsSchemaVersion ||
		report.SampleIntervalMs != desktopDiagnosticsIntervalMs ||
		report.TargetID != "target-a" ||
		report.Scene != protocol.DesktopSceneOffice {
		t.Fatalf("report metadata=%+v", report)
	}
	if len(report.Samples) != desktopDiagnosticsMaxSamples {
		t.Fatalf("sample count=%d want=%d", len(report.Samples), desktopDiagnosticsMaxSamples)
	}
	if got := report.Samples[0].Stats.ActualBitrate; got != 3 {
		t.Fatalf("oldest retained bitrate=%d want=3", got)
	}
	last := report.Samples[len(report.Samples)-1]
	if last.Stats.ActualBitrate != int64(desktopDiagnosticsMaxSamples+2) ||
		last.Adaptation.TargetBitrate != 4_000_000 ||
		last.Adaptation.Reason != "test" {
		t.Fatalf("last sample=%+v", last)
	}
	if report.CurrentConfig.Generation != 13 || report.CurrentStats.Path != "relay" {
		t.Fatalf("current report state=%+v %+v", report.CurrentConfig, report.CurrentStats)
	}
	if report.HEVCValidation != nil {
		t.Fatalf("ordinary H.264 report unexpectedly contains HEVC validation summary: %+v", report.HEVCValidation)
	}

	report.Samples[0].Stats.Path = "mutated"
	again := recorder.Report(start.Add(12*time.Minute), protocol.DesktopVideoConfig{}, protocol.DesktopSessionStats{}, DesktopAudioDiagnostics{})
	if again.Samples[0].Stats.Path != "udp_p2p" {
		t.Fatal("report exposed recorder sample storage")
	}
}

func TestDiagnosticAdaptationPreservesResolutionDecision(t *testing.T) {
	got := diagnosticAdaptation(desktopadapt.MediaDecision{
		Changed:               true,
		Reason:                "resolution_downshift",
		TargetBitrate:         2_000_000,
		TargetFPS:             22,
		TargetResolutionScale: 75,
		ResolutionChanged:     true,
	})
	if !got.Changed || !got.ResolutionChanged || got.TargetResolutionScale != 75 ||
		got.TargetBitrate != 2_000_000 || got.TargetFPS != 22 ||
		got.Reason != "resolution_downshift" {
		t.Fatalf("diagnostic adaptation=%+v", got)
	}
}

func TestSummarizeDesktopDiagnostics(t *testing.T) {
	start := time.Unix(200, 0)
	samples := []DesktopDiagnosticSample{
		{
			AtUnixMs: start.UnixMilli(),
			Config:   protocol.DesktopVideoConfig{Generation: 1, Codec: "h264", Width: 1920, Height: 1080},
			Stats: protocol.DesktopSessionStats{
				Path: "relay", RTTMs: 10, JitterMs: 1, LossPercent: 0,
				SendQueueDelayMs: 2, ActualBitrate: 1_000_000, ReceiveFPS: 30,
				DecodeFPS: 29, RenderFPS: 28, CaptureMs: 2, EncodeMs: 3,
				DecodeMs: 4, RenderMs: 5, CaptureBackend: "dxgi", CaptureFormat: "bgra-direct",
				EncoderBackend: "media-foundation", EncoderHardware: true,
				DecoderBackend: "mf-d3d11", DecoderHardware: true,
			},
		},
		{
			AtUnixMs: start.Add(500 * time.Millisecond).UnixMilli(),
			Config:   protocol.DesktopVideoConfig{Generation: 1, Codec: "h264", Width: 1920, Height: 1080},
			Stats: protocol.DesktopSessionStats{
				Path: "relay", RTTMs: 20, JitterMs: 2, LossPercent: 1,
				SendQueueDelayMs: 4, ActualBitrate: 2_000_000, ReceiveFPS: 25,
				DroppedFrames: 1, CaptureBackend: "dxgi", CaptureFormat: "bgra-direct",
				EncoderBackend: "media-foundation", EncoderHardware: true,
				DecoderBackend: "mf-d3d11", DecoderHardware: true,
			},
			Adaptation: DesktopDiagnosticAdaptation{
				Changed: true, Reason: "loss", TargetBitrate: 3_000_000,
			},
		},
		{
			AtUnixMs: start.Add(time.Second).UnixMilli(),
			Config:   protocol.DesktopVideoConfig{Generation: 2, Codec: "h264", Width: 1440, Height: 810},
			Stats: protocol.DesktopSessionStats{
				Path: "udp_p2p", RTTMs: 30, JitterMs: 3, LossPercent: 2,
				SendQueueDelayMs: 6, ActualBitrate: 3_000_000, ReceiveFPS: 20,
				DroppedFrames: 2, CaptureBackend: "dxgi", CaptureFormat: "bgra-direct",
				EncoderBackend: "media-foundation", EncoderHardware: true,
				DecoderBackend: "mf-d3d11", DecoderHardware: true,
			},
			Adaptation: DesktopDiagnosticAdaptation{
				Changed: true, Reason: "resolution_downshift",
				TargetBitrate: 2_000_000, TargetFPS: 22,
				TargetResolutionScale: 75, ResolutionChanged: true,
			},
		},
		{
			AtUnixMs: start.Add(1500 * time.Millisecond).UnixMilli(),
			Config:   protocol.DesktopVideoConfig{Generation: 2, Codec: "h264", Width: 1440, Height: 810},
			Stats: protocol.DesktopSessionStats{
				Path: "udp_p2p", RTTMs: 40, JitterMs: 4, LossPercent: 3,
				SendQueueDelayMs: 8, ActualBitrate: 4_000_000, ReceiveFPS: 15,
				CaptureBackend: "gdi", CaptureFormat: "rgba", EncoderBackend: "media-foundation",
				DecoderBackend: "webcodecs",
			},
		},
		{
			AtUnixMs: start.Add(2 * time.Second).UnixMilli(),
			Config:   protocol.DesktopVideoConfig{Generation: 3, Codec: "jpeg", Width: 1280, Height: 720},
			Stats: protocol.DesktopSessionStats{
				Path: "relay", RTTMs: 50, JitterMs: 5, LossPercent: 4,
				SendQueueDelayMs: 10, ActualBitrate: 5_000_000, ReceiveFPS: 10,
				DroppedFrames: 3, CaptureBackend: "gdi", CaptureFormat: "rgba",
				EncoderBackend: "jpeg-go", DecoderBackend: "image",
			},
			Adaptation: DesktopDiagnosticAdaptation{
				Changed: true, Reason: "stable_recovery", TargetBitrate: 4_000_000,
			},
		},
	}

	summary := summarizeDesktopDiagnostics(start, start.Add(3*time.Second), samples)
	if summary.SampleCount != 5 || summary.SessionDurationMs != 3000 || summary.SampleSpanMs != 2000 {
		t.Fatalf("summary timing=%+v", summary)
	}
	if summary.PathSwitches != 2 || summary.GenerationChanges != 2 ||
		summary.ABRChanges != 3 || summary.ResolutionChanges != 1 {
		t.Fatalf("summary transitions=%+v", summary)
	}
	if summary.DroppedFrames != 6 {
		t.Fatalf("dropped frames=%d want=6", summary.DroppedFrames)
	}
	if summary.Paths["relay"] != 3 || summary.Paths["udp_p2p"] != 2 ||
		summary.Codecs["h264"] != 4 || summary.Codecs["jpeg"] != 1 {
		t.Fatalf("summary path/codec counts=%+v %+v", summary.Paths, summary.Codecs)
	}
	if summary.Resolutions["1920x1080"] != 2 || summary.Resolutions["1440x810"] != 2 ||
		summary.Resolutions["1280x720"] != 1 {
		t.Fatalf("summary resolutions=%+v", summary.Resolutions)
	}
	if summary.ABRReasons["loss"] != 1 || summary.ABRReasons["resolution_downshift"] != 1 ||
		summary.ABRReasons["stable_recovery"] != 1 {
		t.Fatalf("summary ABR reasons=%+v", summary.ABRReasons)
	}
	if summary.CaptureBackends["dxgi"] != 3 || summary.CaptureBackends["gdi"] != 2 ||
		summary.CaptureFormats["bgra-direct"] != 3 || summary.CaptureFormats["rgba"] != 2 ||
		summary.EncoderBackends["media-foundation"] != 4 ||
		summary.DecoderBackends["mf-d3d11"] != 3 {
		t.Fatalf("summary backends=%+v %+v %+v",
			summary.CaptureBackends, summary.EncoderBackends, summary.DecoderBackends)
	}
	if summary.HardwareEncodingSamples != 3 || summary.HardwareDecodingSamples != 3 {
		t.Fatalf("hardware sample counts=%d/%d",
			summary.HardwareEncodingSamples, summary.HardwareDecodingSamples)
	}

	if got := summary.RTTMs; got.Samples != 5 || got.Min != 10 || got.Avg != 30 ||
		got.P50 != 30 || got.P95 != 50 || got.Max != 50 {
		t.Fatalf("RTT summary=%+v", got)
	}
	if got := summary.LossPercent; got.Samples != 5 || got.P50 != 2 || got.P95 != 4 {
		t.Fatalf("loss summary=%+v", got)
	}
	if got := summary.SendQueueDelayMs; got.Samples != 5 || got.P50 != 6 || got.P95 != 10 {
		t.Fatalf("queue summary=%+v", got)
	}
	if got := summary.ActualBitrate; got.Samples != 5 || got.P50 != 3_000_000 ||
		got.P95 != 5_000_000 {
		t.Fatalf("bitrate summary=%+v", got)
	}
}

func TestDesktopDiagnosticMetricSkipsUnavailableValues(t *testing.T) {
	samples := []DesktopDiagnosticSample{
		{Stats: protocol.DesktopSessionStats{RTTMs: 0, DecodeMs: 0}},
		{Stats: protocol.DesktopSessionStats{RTTMs: 20, DecodeMs: 2}},
		{Stats: protocol.DesktopSessionStats{RTTMs: 40, DecodeMs: 4}},
	}
	positive := func(_ DesktopDiagnosticSample, value float64) bool { return value > 0 }
	got := desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.DecodeMs }, positive)
	if got.Samples != 2 || got.Min != 2 || got.Avg != 3 || got.P50 != 2 || got.P95 != 4 {
		t.Fatalf("filtered metric=%+v", got)
	}
}

func TestSessionDiagnosticsReportSummarizesHEVCValidation(t *testing.T) {
	start := time.Unix(300, 0)
	recorder := newSessionDiagnosticsRecorder("target-hevc", protocol.RemoteDesktopConnectOptions{
		Backend: protocol.DesktopBackendRelay,
		Codec:   protocol.DesktopCodecH265Validation,
	}, start)

	samples := []struct {
		config protocol.DesktopVideoConfig
		stats  protocol.DesktopSessionStats
	}{
		{
			config: protocol.DesktopVideoConfig{Generation: 1, Codec: "h265", Width: 1920, Height: 1080},
			stats: protocol.DesktopSessionStats{
				CaptureBackend: "wgc", CaptureFormat: "bgra-d3d11",
				EncoderBackend: "media-foundation-hevc", EncoderHardware: true,
				DecoderBackend: "mf-hevc-d3d11", DecoderHardware: true,
			},
		},
		{
			config: protocol.DesktopVideoConfig{Generation: 2, Codec: "h265", Width: 1280, Height: 720},
			stats: protocol.DesktopSessionStats{
				CaptureBackend: "gdi", CaptureFormat: "rgba",
				EncoderBackend: "media-foundation-hevc",
				DecoderBackend: "mf-hevc",
			},
		},
		{
			config: protocol.DesktopVideoConfig{Generation: 3, Codec: "h264", Width: 1280, Height: 720},
			stats: protocol.DesktopSessionStats{
				EncoderBackend: "media-foundation",
				DecoderBackend: "mf-d3d11",
			},
		},
		{
			config: protocol.DesktopVideoConfig{Generation: 4, Codec: "jpeg", Width: 1280, Height: 720},
			stats: protocol.DesktopSessionStats{
				EncoderBackend: "jpeg-go",
				DecoderBackend: "image",
			},
		},
	}
	for i, sample := range samples {
		recorder.Record(start.Add(time.Duration(i)*500*time.Millisecond), sample.config, sample.stats, DesktopAudioDiagnostics{}, desktopadapt.MediaDecision{})
	}

	report := recorder.Report(start.Add(3*time.Second), samples[len(samples)-1].config, samples[len(samples)-1].stats, DesktopAudioDiagnostics{})
	got := report.HEVCValidation
	if got == nil || !got.Requested {
		t.Fatalf("missing HEVC validation summary: %+v", got)
	}
	if got.HEVCSamples != 2 || got.H264FallbackSamples != 1 || got.JPEGFallbackSamples != 1 {
		t.Fatalf("HEVC/fallback samples=%+v", got)
	}
	if got.HardwareEncodingSamples != 1 || got.HardwareDecodingSamples != 1 {
		t.Fatalf("HEVC hardware samples=%d/%d", got.HardwareEncodingSamples, got.HardwareDecodingSamples)
	}
	if got.CaptureBackends["wgc"] != 1 || got.CaptureBackends["gdi"] != 1 ||
		got.CaptureFormats["bgra-d3d11"] != 1 || got.CaptureFormats["rgba"] != 1 {
		t.Fatalf("HEVC capture summary=%+v %+v", got.CaptureBackends, got.CaptureFormats)
	}
	if got.EncoderBackends["media-foundation-hevc"] != 2 ||
		got.DecoderBackends["mf-hevc-d3d11"] != 1 || got.DecoderBackends["mf-hevc"] != 1 {
		t.Fatalf("HEVC backend summary=%+v %+v", got.EncoderBackends, got.DecoderBackends)
	}
}

func TestSessionDiagnosticsReportValidatesAYUVZeroCopy(t *testing.T) {
	start := time.Unix(350, 0)
	recorder := newSessionDiagnosticsRecorder("target-gpu", protocol.RemoteDesktopConnectOptions{
		Backend: protocol.DesktopBackendRelay,
		Codec:   "h265",
		Chroma:  protocol.DesktopChroma444,
	}, start)
	targetGPU := &protocol.DesktopGPUCapability{
		Backend:         "d3d11",
		EncodeZeroCopy:  true,
		DecodeZeroCopy:  true,
		DisplayZeroCopy: true,
		Formats:         []string{"nv12", "ayuv"},
	}
	recorder.SetTargetGPUCapability(targetGPU)
	targetGPU.Formats[1] = "mutated"

	config := protocol.DesktopVideoConfig{
		Generation: 1,
		Codec:      "h265",
		Chroma:     string(protocol.DesktopChroma444),
		BitDepth:   8,
		Width:      1920,
		Height:     1080,
	}
	recorder.Record(start, config, protocol.DesktopSessionStats{
		CaptureFormat:   "d3d11-ayuv",
		EncoderBackend:  "onevpl-hevc444-d3d11-zero-copy",
		EncoderHardware: true,
		DecoderBackend:  "onevpl-hevc444-d3d11-zero-copy",
		DecoderHardware: true,
		RenderBackend:   "d3d11-zero-copy",
	}, DesktopAudioDiagnostics{}, desktopadapt.MediaDecision{})
	recorder.Record(start.Add(500*time.Millisecond), config, protocol.DesktopSessionStats{
		CaptureFormat:   "rgba",
		EncoderBackend:  "onevpl-hevc444",
		EncoderHardware: true,
		DecoderBackend:  "onevpl-hevc444",
		DecoderHardware: true,
		RenderBackend:   "cpu-bgra",
	}, DesktopAudioDiagnostics{}, desktopadapt.MediaDecision{})

	report := recorder.Report(
		start.Add(time.Second),
		config,
		protocol.DesktopSessionStats{},
		DesktopAudioDiagnostics{},
	)
	if report.TargetGPU == nil || report.TargetGPU.Backend != "d3d11" ||
		len(report.TargetGPU.Formats) != 2 || report.TargetGPU.Formats[1] != "ayuv" {
		t.Fatalf("target GPU snapshot=%+v", report.TargetGPU)
	}
	got := report.GPUValidation
	if got == nil || got.ExpectedFormat != "ayuv" || !got.TargetAdvertised {
		t.Fatalf("GPU validation identity=%+v", got)
	}
	if got.MatchingSamples != 2 ||
		got.HostEncodeZeroCopySamples != 1 ||
		got.ViewerDecodeZeroCopySamples != 1 ||
		got.ViewerDisplayZeroCopySamples != 1 ||
		got.EndToEndZeroCopySamples != 1 ||
		got.FallbackSamples != 1 {
		t.Fatalf("GPU validation counts=%+v", got)
	}
	if got.CaptureFormats["d3d11-ayuv"] != 1 || got.CaptureFormats["rgba"] != 1 ||
		got.EncoderBackends["onevpl-hevc444-d3d11-zero-copy"] != 1 ||
		got.DecoderBackends["onevpl-hevc444-d3d11-zero-copy"] != 1 ||
		got.RenderBackends["d3d11-zero-copy"] != 1 ||
		got.RenderBackends["cpu-bgra"] != 1 {
		t.Fatalf("GPU validation backends=%+v", got)
	}

	report.TargetGPU.Formats[0] = "changed"
	again := recorder.Report(
		start.Add(2*time.Second),
		config,
		protocol.DesktopSessionStats{},
		DesktopAudioDiagnostics{},
	)
	if again.TargetGPU == nil || again.TargetGPU.Formats[0] != "nv12" {
		t.Fatalf("report exposed target GPU snapshot: %+v", again.TargetGPU)
	}
}

func TestSummarizeGPUValidationRequiresActualDisplayZeroCopy(t *testing.T) {
	config := protocol.DesktopVideoConfig{
		Codec:  "h265",
		Chroma: string(protocol.DesktopChroma444),
	}
	capability := &protocol.DesktopGPUCapability{
		Backend:         "d3d11",
		EncodeZeroCopy:  true,
		DecodeZeroCopy:  true,
		DisplayZeroCopy: true,
		Formats:         []string{"ayuv"},
	}
	samples := []DesktopDiagnosticSample{{
		Config: config,
		Stats: protocol.DesktopSessionStats{
			CaptureFormat:   "d3d11-ayuv",
			EncoderBackend:  "nvenc-hevc444-d3d11-zero-copy",
			EncoderHardware: true,
			DecoderBackend:  "amf-hevc444-d3d11-zero-copy",
			DecoderHardware: true,
			RenderBackend:   "cpu-bgra",
		},
	}}
	got := summarizeGPUValidation(capability, config, samples)
	if got == nil || !got.TargetAdvertised ||
		got.HostEncodeZeroCopySamples != 1 ||
		got.ViewerDecodeZeroCopySamples != 1 ||
		got.ViewerDisplayZeroCopySamples != 0 ||
		got.EndToEndZeroCopySamples != 0 ||
		got.FallbackSamples != 1 {
		t.Fatalf("CPU display must prevent end-to-end zero-copy: %+v", got)
	}
}

func TestSummarizeGPUValidationRequiresAllAdvertisedZeroCopyDirections(t *testing.T) {
	config := protocol.DesktopVideoConfig{
		Codec:  "h265",
		Chroma: string(protocol.DesktopChroma444),
	}
	capability := &protocol.DesktopGPUCapability{
		Backend:         "d3d11",
		EncodeZeroCopy:  true,
		DecodeZeroCopy:  true,
		DisplayZeroCopy: false,
		Formats:         []string{"ayuv"},
	}
	got := summarizeGPUValidation(capability, config, nil)
	if got == nil || got.ExpectedFormat != "ayuv" || got.TargetAdvertised {
		t.Fatalf("partial target capability must not count as advertised: %+v", got)
	}
}

func TestSummarizeDesktopDiagnosticsIncludesAudio(t *testing.T) {
	start := time.Unix(400, 0)
	audioConfig := protocol.DesktopAudioConfig{
		Generation:      1,
		Codec:           protocol.DesktopAudioCodecPCMS16LE,
		SampleRate:      48_000,
		Channels:        2,
		BitsPerSample:   16,
		FrameDurationMs: 20,
		TargetBitrate:   1_536_000,
	}
	samples := []DesktopDiagnosticSample{
		{
			AtUnixMs: start.UnixMilli(),
			Audio: DesktopAudioDiagnostics{
				Enabled: true, Config: audioConfig, QueueFrames: 2, QueueCapacity: 8,
				ReceivedFrames: 10, ConsumedFrames: 8,
			},
		},
		{
			AtUnixMs: start.Add(500 * time.Millisecond).UnixMilli(),
			Audio: DesktopAudioDiagnostics{
				Enabled: true, Config: audioConfig, QueueFrames: 4, QueueCapacity: 8,
				ReceivedFrames: 30, ConsumedFrames: 25, QueueDroppedFrames: 1,
			},
		},
		{
			AtUnixMs: start.Add(time.Second).UnixMilli(),
			Audio: DesktopAudioDiagnostics{
				Enabled: true, Config: audioConfig, QueueFrames: 1, QueueCapacity: 8,
				ReceivedFrames: 50, ConsumedFrames: 48, QueueDroppedFrames: 3,
				GenerationDiscardedFrames: 2, RejectedFrames: 4,
				ReorderedFrames: 5, DuplicateFrames: 2, LateFrames: 3, PlayoutTimeoutFrames: 1,
				ConcealmentFrames: 6, GapSkippedFrames: 8,
			},
		},
	}
	summary := summarizeDesktopDiagnostics(start, start.Add(1500*time.Millisecond), samples)
	if summary.AudioConfiguredSamples != 3 ||
		summary.AudioReceivedFrames != 50 ||
		summary.AudioConsumedFrames != 48 ||
		summary.AudioQueueDroppedFrames != 3 ||
		summary.AudioGenerationDiscards != 2 ||
		summary.AudioRejectedFrames != 4 ||
		summary.AudioReorderedFrames != 5 ||
		summary.AudioDuplicateFrames != 2 ||
		summary.AudioLateFrames != 3 ||
		summary.AudioPlayoutTimeouts != 1 ||
		summary.AudioConcealmentFrames != 6 ||
		summary.AudioGapSkippedFrames != 8 ||
		summary.AudioMaxQueueFrames != 4 {
		t.Fatalf("audio summary=%+v", summary)
	}
	if summary.AudioCodecs[protocol.DesktopAudioCodecPCMS16LE] != 3 {
		t.Fatalf("audio codecs=%+v", summary.AudioCodecs)
	}
	if got := summary.AudioQueueFrames; got.Samples != 3 || got.Min != 1 ||
		got.Avg != 7.0/3.0 || got.P50 != 2 || got.P95 != 4 || got.Max != 4 {
		t.Fatalf("audio queue summary=%+v", got)
	}
}

func TestSessionDiagnosticsReportCarriesCurrentAudio(t *testing.T) {
	start := time.Unix(500, 0)
	recorder := newSessionDiagnosticsRecorder("target-audio", protocol.RemoteDesktopConnectOptions{}, start)
	audio := DesktopAudioDiagnostics{
		Enabled:        true,
		Config:         testAudioConfig(3),
		QueueFrames:    2,
		QueueCapacity:  maxControllerAudioFrames,
		ReceivedFrames: 7,
		ConsumedFrames: 5,
	}
	recorder.Record(start, protocol.DesktopVideoConfig{}, protocol.DesktopSessionStats{}, audio, desktopadapt.MediaDecision{})
	report := recorder.Report(start.Add(time.Second), protocol.DesktopVideoConfig{}, protocol.DesktopSessionStats{}, audio)
	if report.SchemaVersion != 6 || report.CurrentAudio.Config.Generation != 3 ||
		report.CurrentAudio.QueueFrames != 2 || len(report.Samples) != 1 ||
		report.Samples[0].Audio.ReceivedFrames != 7 {
		t.Fatalf("audio report=%+v", report)
	}
}

func TestSummarizeAudioValidationReportsOpusRuntime(t *testing.T) {
	start := time.Unix(600, 0)
	options := protocol.RemoteDesktopConnectOptions{AudioCodec: protocol.DesktopAudioCodecOpus}
	config := protocol.DesktopAudioConfig{
		Generation:      1,
		Codec:           protocol.DesktopAudioCodecOpus,
		SampleRate:      48_000,
		Channels:        2,
		BitsPerSample:   16,
		FrameDurationMs: 20,
		TargetBitrate:   96_000,
	}
	samples := []DesktopDiagnosticSample{
		{
			AtUnixMs: start.UnixMilli(),
			Audio: DesktopAudioDiagnostics{
				Enabled: true, Config: config, QueueFrames: 1,
				ReceivedFrames: 10, ReceivedBytes: 1_200,
				ConsumedFrames: 9, ConsumedBytes: 1_080,
			},
		},
		{
			AtUnixMs: start.Add(time.Second).UnixMilli(),
			Audio: DesktopAudioDiagnostics{
				Enabled: true, Config: config, QueueFrames: 3,
				ReceivedFrames: 60, ReceivedBytes: 13_200,
				ConsumedFrames: 57, ConsumedBytes: 12_600,
				ConcealmentFrames: 2, GapSkippedFrames: 1,
				ReorderedFrames: 4, DuplicateFrames: 1, LateFrames: 2,
				PlayoutTimeoutFrames: 1,
			},
		},
	}
	current := samples[len(samples)-1].Audio
	got := summarizeAudioValidation(options, samples, current)
	if got == nil || !got.Requested || !got.Active {
		t.Fatalf("audio validation=%+v", got)
	}
	if got.RequestedCodec != protocol.DesktopAudioCodecOpus ||
		got.Codec != protocol.DesktopAudioCodecOpus ||
		got.OpusSamples != 2 || got.PCMSamples != 0 || got.PCMFallbackSamples != 0 {
		t.Fatalf("audio codec summary=%+v", got)
	}
	if got.RawPCMBitrate != 1_536_000 || got.TargetBitrate != 96_000 ||
		got.ObservedPayloadBitrate != 96_000 || got.ObservedCompressionRatio != 16 {
		t.Fatalf("audio bitrate summary=%+v", got)
	}
	if got.EstimatedNetworkLossPercent != float64(3)*100/63 ||
		got.ConcealmentFrames != 2 || got.GapSkippedFrames != 1 ||
		got.MaxQueueFrames != 3 || got.SampleSpanMs != 1000 {
		t.Fatalf("audio loss/queue summary=%+v", got)
	}
}

func TestSummarizeAudioValidationMarksPCMOpusFallback(t *testing.T) {
	disabled := false
	if got := summarizeAudioValidation(
		protocol.RemoteDesktopConnectOptions{Audio: &disabled},
		nil,
		DesktopAudioDiagnostics{},
	); got != nil {
		t.Fatalf("disabled audio unexpectedly has validation summary: %+v", got)
	}

	config := testAudioConfig(1)
	samples := []DesktopDiagnosticSample{{
		AtUnixMs: time.Unix(700, 0).UnixMilli(),
		Audio:    DesktopAudioDiagnostics{Enabled: true, Config: config, ReceivedFrames: 10},
	}}
	got := summarizeAudioValidation(
		protocol.RemoteDesktopConnectOptions{AudioCodec: protocol.DesktopAudioCodecOpus},
		samples,
		samples[0].Audio,
	)
	if got == nil || got.PCMSamples != 1 || got.PCMFallbackSamples != 1 ||
		got.Codec != protocol.DesktopAudioCodecPCMS16LE {
		t.Fatalf("PCM fallback summary=%+v", got)
	}
}
