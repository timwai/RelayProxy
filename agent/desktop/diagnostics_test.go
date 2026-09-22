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
	}, protocol.DesktopSessionStats{Path: "relay"})

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

	report.Samples[0].Stats.Path = "mutated"
	again := recorder.Report(start.Add(12*time.Minute), protocol.DesktopVideoConfig{}, protocol.DesktopSessionStats{})
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
