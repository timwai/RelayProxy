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
