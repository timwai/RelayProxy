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

func TestABRReducesFPSAfterPersistentSeverePressureForOffice(t *testing.T) {
	cfg := DefaultConfig(protocol.DesktopSceneOffice, 10_000_000)
	cfg.MaxFPS = 30
	cfg.InitialFPS = 30
	cfg.MinFPS = AdaptiveMinFPS(cfg.Scene, cfg.MaxFPS)
	controller := NewController(cfg)

	for i := 0; i < cfg.FPSPressureWindows-1; i++ {
		decision := controller.Observe(protocol.DesktopSessionStats{SendQueueDelayMs: 140})
		if decision.TargetFPS != 30 {
			t.Fatalf("fps reduced before persistent-pressure window %d: %+v", i, decision)
		}
	}
	decision := controller.Observe(protocol.DesktopSessionStats{SendQueueDelayMs: 140})
	if decision.TargetFPS != 22 {
		t.Fatalf("persistent severe pressure fps=%d want=22 decision=%+v", decision.TargetFPS, decision)
	}
	if controller.TargetFPS() != 22 {
		t.Fatalf("controller target fps=%d want=22", controller.TargetFPS())
	}
}

func TestABRKeepsFPSForGamingScene(t *testing.T) {
	cfg := DefaultConfig(protocol.DesktopSceneGaming, 10_000_000)
	cfg.MaxFPS = 30
	cfg.InitialFPS = 30
	cfg.MinFPS = AdaptiveMinFPS(cfg.Scene, cfg.MaxFPS)
	controller := NewController(cfg)

	for i := 0; i < 16; i++ {
		controller.Observe(protocol.DesktopSessionStats{
			LossPercent:      8,
			SendQueueDelayMs: 180,
			DroppedFrames:    uint64(i + 1),
		})
	}
	if controller.TargetFPS() != 30 {
		t.Fatalf("gaming fps changed under congestion: %d", controller.TargetFPS())
	}
}

func TestABRRecoversFPSOnlyAfterStableRecoveryWindow(t *testing.T) {
	cfg := DefaultConfig(protocol.DesktopSceneOffice, 2_000_000)
	cfg.MinBitrate = cfg.MaxBitrate
	cfg.MaxFPS = 30
	cfg.InitialFPS = 30
	cfg.MinFPS = 10
	cfg.FPSPressureWindows = 4
	cfg.FPSRecoveryWindows = 8
	controller := NewController(cfg)

	for i := 0; i < cfg.FPSPressureWindows; i++ {
		controller.Observe(protocol.DesktopSessionStats{SendQueueDelayMs: 150})
	}
	if controller.TargetFPS() != 22 {
		t.Fatalf("fps did not reduce under persistent pressure: %d", controller.TargetFPS())
	}

	for i := 0; i < cfg.FPSRecoveryWindows-1; i++ {
		decision := controller.Observe(protocol.DesktopSessionStats{
			RTTMs: 30, JitterMs: 2, LossPercent: 0.1, SendQueueDelayMs: 4,
		})
		if decision.TargetFPS != 22 || decision.Changed {
			t.Fatalf("fps recovered too quickly at stable window %d: %+v", i, decision)
		}
	}
	decision := controller.Observe(protocol.DesktopSessionStats{
		RTTMs: 30, JitterMs: 2, LossPercent: 0.1, SendQueueDelayMs: 4,
	})
	if !decision.Changed || decision.Reason != "fps_recovery" || decision.TargetFPS != 27 {
		t.Fatalf("fps recovery decision=%+v", decision)
	}
}

func TestAdaptiveMinFPSPreservesInteractiveScenes(t *testing.T) {
	if got := AdaptiveMinFPS(protocol.DesktopSceneGaming, 24); got != 24 {
		t.Fatalf("gaming min fps=%d want=24", got)
	}
	if got := AdaptiveMinFPS(protocol.DesktopScenePerformance, 30); got != 30 {
		t.Fatalf("performance min fps=%d want=30", got)
	}
	if got := AdaptiveMinFPS(protocol.DesktopSceneOffice, 30); got != 10 {
		t.Fatalf("office min fps=%d want=10", got)
	}
}
