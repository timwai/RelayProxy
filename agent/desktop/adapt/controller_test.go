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
