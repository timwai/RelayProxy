package adapt

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestABRDropsFastOnLossAndRecoversSlowly(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneOffice, 8_000_000))
	decision := controller.Observe(protocol.DesktopSessionStats{LossPercent: 5})
	if !decision.Changed || decision.TargetBitrate != 4_800_000 {
		t.Fatalf("severe loss decision=%+v", decision)
	}
	for i := 0; i < 3; i++ {
		decision = controller.Observe(protocol.DesktopSessionStats{LossPercent: 0.1, RTTMs: 30, JitterMs: 2})
		if decision.Changed {
			t.Fatalf("recovered too quickly at window %d: %+v", i, decision)
		}
	}
	decision = controller.Observe(protocol.DesktopSessionStats{LossPercent: 0.1, RTTMs: 30, JitterMs: 2})
	if !decision.Changed || decision.TargetBitrate <= 4_800_000 || decision.TargetBitrate >= 8_000_000 {
		t.Fatalf("stable recovery decision=%+v", decision)
	}
}

func TestABRDoesNotTreatLowDeliveryAsCongestion(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneOffice, 6_000_000))
	for i := 0; i < 8; i++ {
		decision := controller.Observe(protocol.DesktopSessionStats{
			DeliveryRate: 100_000,
			RTTMs:        25,
			JitterMs:     1,
		})
		if decision.TargetBitrate < 6_000_000 {
			t.Fatalf("static desktop delivery caused downgrade: %+v", decision)
		}
	}
}

func TestABRUsesDroppedFrameDelta(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneGaming, 10_000_000))
	controller.Observe(protocol.DesktopSessionStats{DroppedFrames: 10})
	first := controller.TargetBitrate()
	if first >= 10_000_000 {
		t.Fatalf("dropped frames did not reduce bitrate: %d", first)
	}
	controller.Observe(protocol.DesktopSessionStats{DroppedFrames: 10})
	if controller.TargetBitrate() != first {
		t.Fatalf("cumulative dropped count was treated as a new loss: %d -> %d", first, controller.TargetBitrate())
	}
}

func TestABRHonorsFloorAndCeiling(t *testing.T) {
	cfg := DefaultConfig(protocol.DesktopSceneGaming, 2_000_000)
	controller := NewController(cfg)
	for i := 0; i < 10; i++ {
		controller.Observe(protocol.DesktopSessionStats{LossPercent: 10})
	}
	if controller.TargetBitrate() != cfg.MinBitrate {
		t.Fatalf("target=%d min=%d", controller.TargetBitrate(), cfg.MinBitrate)
	}
	for i := 0; i < 100; i++ {
		controller.Observe(protocol.DesktopSessionStats{RTTMs: 20, JitterMs: 1})
	}
	if controller.TargetBitrate() != cfg.MaxBitrate {
		t.Fatalf("target=%d max=%d", controller.TargetBitrate(), cfg.MaxBitrate)
	}
}

func TestABRDropsOnSendQueueBeforePacketLoss(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneOffice, 8_000_000))

	decision := controller.Observe(protocol.DesktopSessionStats{
		RTTMs:            35,
		JitterMs:         3,
		LossPercent:      0,
		SendQueueDelayMs: 130,
	})
	if !decision.Changed || decision.TargetBitrate != 4_800_000 || decision.Reason != "severe_queue" {
		t.Fatalf("queue congestion decision=%+v", decision)
	}
}

func TestABRBandwidthCollapseAndStableRecovery(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneOffice, 10_000_000))

	first := controller.Observe(protocol.DesktopSessionStats{
		RTTMs:            35,
		JitterMs:         3,
		SendQueueDelayMs: 75,
	})
	if !first.Changed || first.TargetBitrate != 7_500_000 || first.Reason != "queue" {
		t.Fatalf("first congestion decision=%+v", first)
	}

	second := controller.Observe(protocol.DesktopSessionStats{
		RTTMs:            40,
		JitterMs:         4,
		SendQueueDelayMs: 140,
	})
	if !second.Changed || second.TargetBitrate != 4_500_000 || second.Reason != "severe_queue" {
		t.Fatalf("collapse decision=%+v", second)
	}

	for i := 0; i < 3; i++ {
		decision := controller.Observe(protocol.DesktopSessionStats{
			RTTMs:            35,
			JitterMs:         2,
			LossPercent:      0.1,
			SendQueueDelayMs: 4,
		})
		if decision.Changed {
			t.Fatalf("recovered too quickly at window %d: %+v", i, decision)
		}
	}
	recovery := controller.Observe(protocol.DesktopSessionStats{
		RTTMs:            35,
		JitterMs:         2,
		LossPercent:      0.1,
		SendQueueDelayMs: 4,
	})
	if !recovery.Changed || recovery.Reason != "stable_recovery" || recovery.TargetBitrate <= 4_500_000 {
		t.Fatalf("stable recovery decision=%+v", recovery)
	}
}

func TestABRQueueDelayBlocksRecoveryWithoutForcingExtraDrop(t *testing.T) {
	controller := NewController(DefaultConfig(protocol.DesktopSceneOffice, 8_000_000))
	controller.Observe(protocol.DesktopSessionStats{LossPercent: 5})
	reduced := controller.TargetBitrate()

	for i := 0; i < 8; i++ {
		decision := controller.Observe(protocol.DesktopSessionStats{
			RTTMs:            30,
			JitterMs:         2,
			LossPercent:      0.1,
			SendQueueDelayMs: 20,
		})
		if decision.TargetBitrate != reduced {
			t.Fatalf("queue-delayed recovery changed bitrate at window %d: %+v", i, decision)
		}
	}
}
