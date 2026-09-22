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
