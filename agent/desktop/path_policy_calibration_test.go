package desktop

import (
	"testing"
	"time"
)

func TestDefaultPathPolicyPromotesCleanDirectPathAfterHold(t *testing.T) {
	policy := DefaultPathScorePolicy()
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        70,
		JitterMs:     5,
		LossPercent:  0.1,
		QueueDelayMs: 2,
	}
	direct := PathQuality{
		Available:    true,
		RTTMs:        35,
		JitterMs:     4,
		LossPercent:  0.2,
		QueueDelayMs: 2,
	}

	var gate PathSwitchGate
	start := time.Unix(2000, 0)
	first := gate.Evaluate(start, "relay", relay, "udp_p2p", direct, policy)
	if first.Switch || first.Reason != "candidate_probation" {
		t.Fatalf("first decision=%+v", first)
	}
	beforeHold := gate.Evaluate(start.Add(policy.UpgradeHold-time.Millisecond), "relay", relay, "udp_p2p", direct, policy)
	if beforeHold.Switch {
		t.Fatalf("clean direct path switched before hold elapsed: %+v", beforeHold)
	}
	afterHold := gate.Evaluate(start.Add(policy.UpgradeHold), "relay", relay, "udp_p2p", direct, policy)
	if !afterHold.Switch || afterHold.Reason != "candidate_stable_better" {
		t.Fatalf("clean direct path was not promoted: %+v", afterHold)
	}
}

func TestDefaultPathPolicyRejectsLossyDirectRTTAdvantage(t *testing.T) {
	policy := DefaultPathScorePolicy()
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        50,
		JitterMs:     3,
		LossPercent:  0.1,
		QueueDelayMs: 2,
	}
	direct := PathQuality{
		Available:    true,
		RTTMs:        30,
		JitterMs:     2,
		LossPercent:  1.5,
		QueueDelayMs: 2,
	}

	var gate PathSwitchGate
	decision := gate.Evaluate(time.Unix(2100, 0), "relay", relay, "udp_p2p", direct, policy)
	if decision.Switch || decision.Reason != "candidate_not_better_enough" {
		t.Fatalf("lossy direct path unexpectedly promoted: %+v", decision)
	}
	if decision.CandidateScore <= decision.CurrentScore {
		t.Fatalf("lossy direct score=%v relay score=%v", decision.CandidateScore, decision.CurrentScore)
	}
}

func TestDefaultPathPolicyFallsBackFromQueuedLossyDirectPath(t *testing.T) {
	policy := DefaultPathScorePolicy()
	direct := PathQuality{
		Available:    true,
		RTTMs:        40,
		JitterMs:     12,
		LossPercent:  2.0,
		QueueDelayMs: 45,
	}
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        65,
		JitterMs:     5,
		LossPercent:  0.2,
		QueueDelayMs: 3,
	}

	var gate PathSwitchGate
	start := time.Unix(2200, 0)
	first := gate.Evaluate(start, "udp_p2p", direct, "relay", relay, policy)
	if first.Switch || first.Reason != "candidate_probation" {
		t.Fatalf("fallback should start probation: %+v", first)
	}
	afterHold := gate.Evaluate(start.Add(policy.UpgradeHold), "udp_p2p", direct, "relay", relay, policy)
	if !afterHold.Switch || afterHold.Reason != "candidate_stable_better" {
		t.Fatalf("sustained queued direct path did not fall back: %+v", afterHold)
	}
}

func TestDefaultPathPolicyDoesNotFlapNearEqualPaths(t *testing.T) {
	policy := DefaultPathScorePolicy()
	relay := PathQuality{
		Available:    true,
		Relay:        true,
		RTTMs:        45,
		JitterMs:     3,
		LossPercent:  0.1,
		QueueDelayMs: 2,
	}
	direct := PathQuality{
		Available:    true,
		RTTMs:        42,
		JitterMs:     3,
		LossPercent:  0.2,
		QueueDelayMs: 2,
	}

	var gate PathSwitchGate
	start := time.Unix(2300, 0)
	for i := 0; i < 10; i++ {
		decision := gate.Evaluate(start.Add(time.Duration(i)*time.Second), "relay", relay, "udp_p2p", direct, policy)
		if decision.Switch {
			t.Fatalf("near-equal paths flapped at sample %d: %+v", i, decision)
		}
		if decision.Reason != "candidate_not_better_enough" {
			t.Fatalf("unexpected near-equal decision at sample %d: %+v", i, decision)
		}
	}
}
