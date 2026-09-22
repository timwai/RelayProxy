package desktop

import (
	"testing"
	"time"

	desktopadapt "relayproxy/agent/desktop/adapt"
	"relayproxy/internal/protocol"
)

func TestPathSwitchScenarioPromotesStableDirectPath(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 3 * time.Second
	policy.EmergencyMargin = 1000

	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        85,
		JitterMs:     8,
		LossPercent:  0.4,
		QueueDelayMs: 5,
	}
	direct := PathQuality{
		Available:    true,
		RTTMs:        36,
		JitterMs:     3,
		LossPercent:  0.1,
		QueueDelayMs: 2,
	}

	var gate PathSwitchGate
	start := time.Unix(1000, 0)
	for second := 0; second < 3; second++ {
		decision := gate.Evaluate(start.Add(time.Duration(second)*time.Second), "relay", relay, "udp_p2p", direct, policy)
		if decision.Switch {
			t.Fatalf("switched before hold elapsed at second=%d: %+v", second, decision)
		}
	}
	decision := gate.Evaluate(start.Add(3*time.Second), "relay", relay, "udp_p2p", direct, policy)
	if !decision.Switch || decision.Reason != "candidate_stable_better" {
		t.Fatalf("stable direct path was not promoted: %+v", decision)
	}
}

func TestPathSwitchScenarioIgnoresTransientDirectDegradation(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 3 * time.Second
	policy.EmergencyMargin = 1000

	direct := PathQuality{
		Available:    true,
		RTTMs:        32,
		JitterMs:     2,
		LossPercent:  0.1,
		QueueDelayMs: 1,
	}
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        72,
		JitterMs:     6,
		LossPercent:  0.3,
		QueueDelayMs: 4,
	}

	// One bad direct sample starts a fallback probation.
	bad := direct
	bad.RTTMs = 140
	bad.JitterMs = 28
	bad.LossPercent = 3.5
	bad.QueueDelayMs = 30

	var gate PathSwitchGate
	start := time.Unix(1100, 0)
	first := gate.Evaluate(start, "udp_p2p", bad, "relay", relay, policy)
	if first.Switch || first.Reason != "candidate_probation" {
		t.Fatalf("transient degradation should only start probation: %+v", first)
	}

	// The next healthy sample must reset the pending fallback.
	recovered := gate.Evaluate(start.Add(time.Second), "udp_p2p", direct, "relay", relay, policy)
	if recovered.Switch || recovered.Reason != "candidate_not_better_enough" {
		t.Fatalf("healthy direct sample should cancel fallback: %+v", recovered)
	}

	// A later bad sample must start a new full hold instead of inheriting time.
	again := gate.Evaluate(start.Add(4*time.Second), "udp_p2p", bad, "relay", relay, policy)
	if again.Switch || again.HoldRemaining != policy.UpgradeHold {
		t.Fatalf("fallback probation was not restarted: %+v", again)
	}
}

func TestPathSwitchScenarioFallsBackAfterSustainedDirectLoss(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 3 * time.Second
	policy.EmergencyMargin = 1000

	direct := PathQuality{
		Available:    true,
		RTTMs:        48,
		JitterMs:     15,
		LossPercent:  3.2,
		QueueDelayMs: 24,
	}
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        68,
		JitterMs:     5,
		LossPercent:  0.2,
		QueueDelayMs: 3,
	}

	var gate PathSwitchGate
	start := time.Unix(1200, 0)
	for second := 0; second < 3; second++ {
		decision := gate.Evaluate(start.Add(time.Duration(second)*time.Second), "udp_p2p", direct, "relay", relay, policy)
		if decision.Switch {
			t.Fatalf("fallback occurred before sustained-loss hold elapsed at second=%d: %+v", second, decision)
		}
	}
	decision := gate.Evaluate(start.Add(3*time.Second), "udp_p2p", direct, "relay", relay, policy)
	if !decision.Switch || decision.Reason != "candidate_stable_better" {
		t.Fatalf("sustained bad direct path did not fall back: %+v", decision)
	}
}

func TestPathSwitchScenarioFailsOverImmediatelyWhenDirectUnavailable(t *testing.T) {
	policy := DefaultPathScorePolicy()
	directDown := PathQuality{Available: false}
	relay := PathQuality{Available: true, Relay: true, RTTMs: 90}

	var gate PathSwitchGate
	decision := gate.Evaluate(time.Unix(1300, 0), "udp_p2p", directDown, "relay", relay, policy)
	if !decision.Switch || decision.Reason != "current_unavailable" {
		t.Fatalf("unavailable direct path must fail over immediately: %+v", decision)
	}
}

func TestCombinedWeakNetworkABRAndPathSwitchDoNotFlap(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.EmergencyMargin = 1000

	abr := desktopadapt.NewController(desktopadapt.DefaultConfig(protocol.DesktopSceneOffice, 10_000_000))
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        78,
		JitterMs:     6,
		LossPercent:  0.2,
		QueueDelayMs: 4,
	}
	healthyDirect := PathQuality{
		Available:    true,
		RTTMs:        34,
		JitterMs:     3,
		LossPercent:  0.1,
		QueueDelayMs: 2,
	}
	impairedDirect := PathQuality{
		Available:    true,
		RTTMs:        72,
		JitterMs:     32,
		LossPercent:  3.2,
		QueueDelayMs: 85,
	}

	var gate PathSwitchGate
	start := time.Unix(3000, 0)

	// Start on a healthy direct path. A short combined impairment should make
	// ABR react immediately, but path hysteresis must keep the direct path.
	firstABR := abr.Observe(protocol.DesktopSessionStats{
		RTTMs:            impairedDirect.RTTMs,
		JitterMs:         impairedDirect.JitterMs,
		LossPercent:      impairedDirect.LossPercent,
		SendQueueDelayMs: impairedDirect.QueueDelayMs,
		DroppedFrames:    1,
	})
	if !firstABR.Changed || firstABR.TargetBitrate >= 10_000_000 {
		t.Fatalf("combined impairment did not reduce bitrate: %+v", firstABR)
	}
	firstPath := gate.Evaluate(start, "udp_p2p", impairedDirect, "relay", relay, policy)
	if firstPath.Switch || firstPath.Reason != "candidate_probation" {
		t.Fatalf("transient combined impairment flapped path: %+v", firstPath)
	}

	// One healthy window cancels fallback probation. ABR must not bounce upward
	// immediately because its stable recovery needs multiple windows.
	recoveredPath := gate.Evaluate(start.Add(time.Second), "udp_p2p", healthyDirect, "relay", relay, policy)
	if recoveredPath.Switch || recoveredPath.Reason != "candidate_not_better_enough" {
		t.Fatalf("healthy direct sample did not cancel fallback: %+v", recoveredPath)
	}
	recoveredABR := abr.Observe(protocol.DesktopSessionStats{
		RTTMs:            healthyDirect.RTTMs,
		JitterMs:         healthyDirect.JitterMs,
		LossPercent:      healthyDirect.LossPercent,
		SendQueueDelayMs: healthyDirect.QueueDelayMs,
		DroppedFrames:    1,
	})
	if recoveredABR.Changed {
		t.Fatalf("ABR recovered too quickly after combined impairment: %+v", recoveredABR)
	}

	// Sustained combined impairment eventually causes exactly one path fallback
	// after the full hold. ABR may continue reducing bitrate while probation is
	// active, but it must not make the path gate bypass hysteresis.
	for second := 2; second < 2+int(policy.UpgradeHold/time.Second); second++ {
		abr.Observe(protocol.DesktopSessionStats{
			RTTMs:            impairedDirect.RTTMs,
			JitterMs:         impairedDirect.JitterMs,
			LossPercent:      impairedDirect.LossPercent,
			SendQueueDelayMs: impairedDirect.QueueDelayMs,
			DroppedFrames:    uint64(second),
		})
		decision := gate.Evaluate(start.Add(time.Duration(second)*time.Second), "udp_p2p", impairedDirect, "relay", relay, policy)
		if decision.Switch {
			t.Fatalf("path switched before hold elapsed at second=%d: %+v", second, decision)
		}
	}
	fallbackAt := start.Add((2*time.Second)+policy.UpgradeHold)
	fallback := gate.Evaluate(fallbackAt, "udp_p2p", impairedDirect, "relay", relay, policy)
	if !fallback.Switch || fallback.Reason != "candidate_stable_better" {
		t.Fatalf("sustained combined impairment did not fall back: %+v", fallback)
	}

	// After fallback, a healthy direct path must earn a fresh promotion hold.
	var promoteGate PathSwitchGate
	promotion := promoteGate.Evaluate(fallbackAt.Add(time.Second), "relay", relay, "udp_p2p", healthyDirect, policy)
	if promotion.Switch || promotion.Reason != "candidate_probation" {
		t.Fatalf("direct path re-promoted without fresh hold: %+v", promotion)
	}
	beforeHold := promoteGate.Evaluate(fallbackAt.Add(time.Second+policy.UpgradeHold-time.Millisecond), "relay", relay, "udp_p2p", healthyDirect, policy)
	if beforeHold.Switch {
		t.Fatalf("direct path re-promoted before recovery hold: %+v", beforeHold)
	}
	afterHold := promoteGate.Evaluate(fallbackAt.Add(time.Second+policy.UpgradeHold), "relay", relay, "udp_p2p", healthyDirect, policy)
	if !afterHold.Switch || afterHold.Reason != "candidate_stable_better" {
		t.Fatalf("stable direct path did not re-promote after hold: %+v", afterHold)
	}
}
