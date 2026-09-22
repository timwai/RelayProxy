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
