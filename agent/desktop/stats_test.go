package desktop

import (
	"testing"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func TestSessionStatsTracksPacketLossAndRecovery(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.started = time.Now().Add(-time.Second)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 10}, 100)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 12}, 100)
	if got := stats.Snapshot(time.Now()).LossPercent; got <= 0 {
		t.Fatalf("lossPercent=%v", got)
	}
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 11}, 100)
	if got := stats.Snapshot(time.Now()).LossPercent; got != 0 {
		t.Fatalf("recovered lossPercent=%v", got)
	}
}

func TestSessionStatsRTTAndRemoteMerge(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	now := time.Now()
	probe := stats.NewProbe(now)
	stats.ObservePong(probe, now.Add(20*time.Millisecond))
	stats.MergeRemote(protocol.DesktopSessionStats{
		CaptureFPS:    30,
		EncodeFPS:     29,
		TargetBitrate: 6_000_000,
	})
	got := stats.Snapshot(now.Add(time.Second))
	if got.RTTMs < 19 || got.RTTMs > 21 {
		t.Fatalf("rtt=%v", got.RTTMs)
	}
	if got.EncodeFPS != 29 || got.TargetBitrate != 6_000_000 || got.Path != "relay" {
		t.Fatalf("stats=%+v", got)
	}
}

func TestSessionStatsSeparatesReceiveAndViewerFPS(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.started = time.Now().Add(-time.Second)
	stats.ObserveFrame()
	stats.MergeViewer(protocol.DesktopSessionStats{
		DecodeFPS: 59,
		RenderFPS: 58,
		DecodeMs:  2.5,
		RenderMs:  1.2,
	})
	got := stats.Snapshot(time.Now())
	if got.ReceiveFPS <= 0 {
		t.Fatalf("receiveFps=%v", got.ReceiveFPS)
	}
	if got.DecodeFPS != 59 || got.RenderFPS != 58 || got.DecodeMs != 2.5 || got.RenderMs != 1.2 {
		t.Fatalf("viewer stats=%+v", got)
	}
}

func TestAdaptationSnapshotUsesWindowedLoss(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 100}, 100)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 102}, 100)
	first := stats.AdaptationSnapshot(time.Now())
	if first.LossPercent <= 0 {
		t.Fatalf("first lossPercent=%v", first.LossPercent)
	}

	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 101}, 100)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 103}, 100)
	second := stats.AdaptationSnapshot(time.Now())
	if second.LossPercent != 0 {
		t.Fatalf("recovered window lossPercent=%v", second.LossPercent)
	}

	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 104}, 100)
	third := stats.AdaptationSnapshot(time.Now())
	if third.LossPercent != 0 {
		t.Fatalf("stable window lossPercent=%v", third.LossPercent)
	}
}

func TestPathQualityResetsAtPathBoundary(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.MergeRemote(protocol.DesktopSessionStats{SendQueueDelayMs: 7})
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 100}, 100)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 101}, 100)

	relay := stats.PathQuality(time.Now(), true)
	if !relay.Available || !relay.Relay || relay.LossPercent != 0 || relay.QueueDelayMs != 7 {
		t.Fatalf("relay quality=%+v", relay)
	}

	stats.SetPath("udp_p2p")
	if direct := stats.PathQuality(time.Now(), false); direct.Available {
		t.Fatalf("new path should not be available before media arrives: %+v", direct)
	}

	// A large sequence jump on the first packet of the new path must not be
	// counted as loss from the old path.
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 500}, 100)
	direct := stats.PathQuality(time.Now(), false)
	if !direct.Available || direct.Relay || direct.LossPercent != 0 {
		t.Fatalf("direct quality after first packet=%+v", direct)
	}

	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 502}, 100)
	if got := stats.PathQuality(time.Now(), false).LossPercent; got <= 0 {
		t.Fatalf("direct lossPercent=%v", got)
	}
}

func TestSessionStatsPreservesMediaPipelineDiagnostics(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.started = time.Now().Add(-time.Second)
	stats.MergeRemote(protocol.DesktopSessionStats{
		CaptureBackend:  "dxgi",
		EncoderBackend:  "media-foundation",
		EncoderHardware: true,
	})
	stats.MergeViewer(protocol.DesktopSessionStats{
		DecoderBackend:  "media-foundation-d3d11-zero-copy",
		DecoderHardware: true,
	})
	got := stats.Snapshot(time.Now())
	if got.CaptureBackend != "dxgi" || got.EncoderBackend != "media-foundation" || !got.EncoderHardware {
		t.Fatalf("host media diagnostics=%+v", got)
	}
	if got.DecoderBackend != "media-foundation-d3d11-zero-copy" || !got.DecoderHardware {
		t.Fatalf("viewer media diagnostics=%+v", got)
	}
}

func TestDiagnosticsSnapshotUsesShortWindow(t *testing.T) {
	stats := newSessionStatsTracker("udp_p2p")
	now := time.Unix(100, 0)
	stats.diagnosticAt = now.Add(-500 * time.Millisecond)
	stats.recvBytes = 62_500
	stats.recvPackets = 100
	stats.recvFrames = 15
	stats.lossDetected = 5
	stats.lossRecovered = 1
	stats.dropped = 2
	stats.rttMs = 24
	stats.jitterMs = 3
	stats.MergeRemote(protocol.DesktopSessionStats{
		CaptureFPS:       29,
		EncodeFPS:        28,
		TargetBitrate:    4_000_000,
		TargetFPS:        30,
		SendQueueDelayMs: 12,
		CaptureBackend:   "dxgi",
		EncoderBackend:   "media-foundation",
		EncoderHardware:  true,
		DroppedFrames:    3,
	})
	stats.MergeViewer(protocol.DesktopSessionStats{
		DecodeFPS:       27,
		RenderFPS:       26,
		DecodeMs:        2.5,
		RenderMs:        1.2,
		DecoderBackend:  "media-foundation-d3d11-zero-copy",
		DecoderHardware: true,
	})

	got := stats.DiagnosticsSnapshot(now)
	if got.ActualBitrate != 1_000_000 || got.DeliveryRate != 1_000_000 {
		t.Fatalf("window bitrate=%d delivery=%d", got.ActualBitrate, got.DeliveryRate)
	}
	if got.ReceiveFPS != 30 {
		t.Fatalf("window receive fps=%v want=30", got.ReceiveFPS)
	}
	wantLoss := float64(4) * 100 / 104
	if got.LossPercent < wantLoss-0.001 || got.LossPercent > wantLoss+0.001 {
		t.Fatalf("window loss=%v want=%v", got.LossPercent, wantLoss)
	}
	if got.Path != "udp_p2p" || got.RTTMs != 24 || got.JitterMs != 3 ||
		got.SendQueueDelayMs != 12 || got.DroppedFrames != 5 {
		t.Fatalf("network diagnostics=%+v", got)
	}
	if got.CaptureBackend != "dxgi" || got.EncoderBackend != "media-foundation" ||
		!got.EncoderHardware || got.DecoderBackend != "media-foundation-d3d11-zero-copy" ||
		!got.DecoderHardware {
		t.Fatalf("pipeline diagnostics=%+v", got)
	}

	empty := stats.DiagnosticsSnapshot(now.Add(500 * time.Millisecond))
	if empty.ActualBitrate != 0 || empty.ReceiveFPS != 0 || empty.LossPercent != 0 ||
		empty.DroppedFrames != 0 {
		t.Fatalf("diagnostic window did not advance: %+v", empty)
	}
}

func TestDiagnosticsSnapshotDoesNotConsumeABRLossWindow(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	now := time.Now()
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 10}, 100)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 12}, 100)
	if got := stats.DiagnosticsSnapshot(now).LossPercent; got <= 0 {
		t.Fatalf("diagnostic loss=%v", got)
	}
	if got := stats.AdaptationSnapshot(now).LossPercent; got <= 0 {
		t.Fatalf("diagnostics unexpectedly consumed ABR loss window: %v", got)
	}
}

func TestDiagnosticsWindowResetsAtPathBoundary(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	now := time.Now()
	stats.diagnosticAt = now.Add(-500 * time.Millisecond)
	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 10}, 1000)
	stats.ObserveFrame()
	before := stats.DiagnosticsSnapshot(now)
	if before.ActualBitrate <= 0 || before.ReceiveFPS <= 0 || before.Path != "relay" {
		t.Fatalf("relay diagnostic sample=%+v", before)
	}

	stats.ObservePacket(desktopmedia.MediaHeader{Sequence: 11}, 1000)
	stats.ObserveFrame()
	stats.SetPath("udp_p2p")
	after := stats.DiagnosticsSnapshot(time.Now().Add(500 * time.Millisecond))
	if after.ActualBitrate != 0 || after.ReceiveFPS != 0 || after.LossPercent != 0 ||
		after.Path != "udp_p2p" {
		t.Fatalf("path boundary leaked previous media into diagnostics: %+v", after)
	}
}

func TestAdaptationSnapshotStillIncludesLocalDroppedFrames(t *testing.T) {
	stats := newSessionStatsTracker("relay")
	stats.MergeRemote(protocol.DesktopSessionStats{DroppedFrames: 3})
	stats.ObserveDroppedFrame()
	got := stats.AdaptationSnapshot(time.Now())
	if got.DroppedFrames != 4 {
		t.Fatalf("adaptation dropped frames=%d want=4", got.DroppedFrames)
	}
}
