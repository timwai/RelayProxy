package desktop

import (
	"testing"
	"time"
)

func TestPathSwitchScenarioPromotesStableDirectPath(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 3 * time.Second
	policy.EmergencyMargin = 1000

	relay := PathQuality{
		Available: true,
		Relay: true,
		RTTMs: 85,
		JitterMs: 8,
		LossPercent: 0.4,
		QueueDelayMs: 5,
	}
	direct := PathQuality{
		Available: true,
		RTTMs: 36,
		JitterMs: 3,
		LossPercent: 0.1,
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
		Available: true,
		RTTMs: 32,
		JitterMs: 2,
		LossPercent: 0.1,
		QueueDelayMs: 1,
	}
	relay := PathQuality{
		Available: true,
		Relay: true,
		RTTMs: 72,
		JitterMs: 6,
		LossPercent: 0.3,
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
		Available: true,
		RTTMs: 48,
		JitterMs: 15,
		LossPercent: 3.2,
		QueueDelayMs: 24,
	}
	relay := PathQuality{
		Available: true,
		Relay: true,
		RTTMs: 68,
		JitterMs: 5,
		LossPercent: 0.2,
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
