package desktop

import (
	"math"
	"testing"
	"time"
)

func TestPathScorePolicyUnavailableAndRelayPenalty(t *testing.T) {
	policy := DefaultPathScorePolicy()
	if score := policy.Score(PathQuality{}); !math.IsInf(score, 1) {
		t.Fatalf("unavailable score=%v want=+Inf", score)
	}

	direct := PathQuality{
		Available:    true,
		RTTMs:        30,
		JitterMs:     3,
		LossPercent:  0.2,
		QueueDelayMs: 2,
	}
	relay := direct
	relay.Relay = true
	if gotDirect, gotRelay := policy.Score(direct), policy.Score(relay); gotRelay <= gotDirect {
		t.Fatalf("relay score=%v direct score=%v; relay penalty was not applied", gotRelay, gotDirect)
	}
}

func TestPathScorePolicyLossDominatesSmallRTTAdvantage(t *testing.T) {
	policy := DefaultPathScorePolicy()
	lowLoss := PathQuality{Available: true, RTTMs: 45, JitterMs: 2, LossPercent: 0.1}
	lowRTTHighLoss := PathQuality{Available: true, RTTMs: 25, JitterMs: 2, LossPercent: 2.0}
	if policy.Score(lowRTTHighLoss) <= policy.Score(lowLoss) {
		t.Fatalf("lossy path unexpectedly scored better: lossy=%v stable=%v", policy.Score(lowRTTHighLoss), policy.Score(lowLoss))
	}
}

func TestPathSwitchGateRequiresStableImprovement(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 3 * time.Second
	policy.EmergencyMargin = 1000

	current := PathQuality{Available: true, Relay: true, RTTMs: 80, LossPercent: 0.5}
	candidate := PathQuality{Available: true, RTTMs: 45, LossPercent: 0.2}
	var gate PathSwitchGate
	start := time.Unix(100, 0)

	first := gate.Evaluate(start, "relay", current, "udp_p2p", candidate, policy)
	if first.Switch || first.Reason != "candidate_probation" {
		t.Fatalf("first decision=%+v", first)
	}
	second := gate.Evaluate(start.Add(2*time.Second), "relay", current, "udp_p2p", candidate, policy)
	if second.Switch || second.HoldRemaining != time.Second {
		t.Fatalf("second decision=%+v", second)
	}
	third := gate.Evaluate(start.Add(3*time.Second), "relay", current, "udp_p2p", candidate, policy)
	if !third.Switch || third.Reason != "candidate_stable_better" {
		t.Fatalf("third decision=%+v", third)
	}
}

func TestPathSwitchGateResetsWhenCandidateStopsBeingBetter(t *testing.T) {
	policy := DefaultPathScorePolicy()
	policy.UpgradeHold = 2 * time.Second
	policy.EmergencyMargin = 1000

	current := PathQuality{Available: true, Relay: true, RTTMs: 80}
	better := PathQuality{Available: true, RTTMs: 40}
	worse := PathQuality{Available: true, RTTMs: 100}
	var gate PathSwitchGate
	start := time.Unix(200, 0)

	if decision := gate.Evaluate(start, "relay", current, "udp_p2p", better, policy); decision.Switch {
		t.Fatalf("unexpected initial switch: %+v", decision)
	}
	if decision := gate.Evaluate(start.Add(time.Second), "relay", current, "udp_p2p", worse, policy); decision.Reason != "candidate_not_better_enough" {
		t.Fatalf("unexpected reset decision: %+v", decision)
	}
	if decision := gate.Evaluate(start.Add(3*time.Second), "relay", current, "udp_p2p", better, policy); decision.Switch {
		t.Fatalf("candidate should have restarted probation: %+v", decision)
	}
}

func TestPathSwitchGateImmediateFailoverAndEmergencyUpgrade(t *testing.T) {
	policy := DefaultPathScorePolicy()
	currentDown := PathQuality{Available: false}
	candidate := PathQuality{Available: true, RTTMs: 40}
	var gate PathSwitchGate

	decision := gate.Evaluate(time.Unix(300, 0), "udp_p2p", currentDown, "relay", candidate, policy)
	if !decision.Switch || decision.Reason != "current_unavailable" {
		t.Fatalf("failover decision=%+v", decision)
	}

	policy.EmergencyMargin = 20
	current := PathQuality{Available: true, Relay: true, RTTMs: 150, LossPercent: 4}
	direct := PathQuality{Available: true, RTTMs: 20, LossPercent: 0}
	decision = gate.Evaluate(time.Unix(301, 0), "relay", current, "udp_p2p", direct, policy)
	if !decision.Switch || decision.Reason != "candidate_much_better" {
		t.Fatalf("emergency upgrade decision=%+v", decision)
	}
}
