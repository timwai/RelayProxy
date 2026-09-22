package desktop

import (
	"math"
	"time"
)

// PathQuality is the transport-facing input to Relay Desktop path selection.
// Lower scores are better. Availability is explicit so a zero-value sample is
// never mistaken for a healthy path.
type PathQuality struct {
	Available    bool
	Relay        bool
	RTTMs        float64
	JitterMs     float64
	LossPercent  float64
	QueueDelayMs float64
}

// PathScorePolicy keeps path-ranking constants in one testable place. The
// defaults are intentionally conservative and should be tuned with NetEm and
// cross-NAT measurements instead of being copied into connection code.
type PathScorePolicy struct {
	RTTWeight        float64
	JitterWeight     float64
	LossWeight       float64
	QueueDelayWeight float64
	RelayPenalty     float64

	// UpgradeMargin is the minimum score improvement required before a
	// candidate starts its promotion hold window.
	UpgradeMargin float64
	// UpgradeHold prevents a briefly-good candidate from causing path flaps.
	UpgradeHold time.Duration
	// EmergencyMargin allows an obviously-better path to bypass UpgradeHold.
	EmergencyMargin float64
}

func DefaultPathScorePolicy() PathScorePolicy {
	return PathScorePolicy{
		RTTWeight:        0.35,
		JitterWeight:     0.75,
		LossWeight:       18,
		QueueDelayWeight: 0.65,
		RelayPenalty:     8,
		UpgradeMargin:    12,
		UpgradeHold:      3 * time.Second,
		EmergencyMargin:  60,
	}
}

func pathMetric(value float64) float64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func nonNegativeWeight(value float64) float64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

// Score converts a quality sample into a comparable cost. An unavailable path
// always scores +Inf. Loss is deliberately weighted much more heavily than a
// small RTT change because interactive video recovers from latency more
// gracefully than repeated frame loss.
func (p PathScorePolicy) Score(q PathQuality) float64 {
	if !q.Available {
		return math.Inf(1)
	}
	score :=
		pathMetric(q.RTTMs)*nonNegativeWeight(p.RTTWeight) +
			pathMetric(q.JitterMs)*nonNegativeWeight(p.JitterWeight) +
			pathMetric(q.LossPercent)*nonNegativeWeight(p.LossWeight) +
			pathMetric(q.QueueDelayMs)*nonNegativeWeight(p.QueueDelayWeight)
	if q.Relay {
		score += nonNegativeWeight(p.RelayPenalty)
	}
	return score
}

func (p PathScorePolicy) normalized() PathScorePolicy {
	defaults := DefaultPathScorePolicy()
	if p.UpgradeMargin <= 0 || math.IsNaN(p.UpgradeMargin) || math.IsInf(p.UpgradeMargin, 0) {
		p.UpgradeMargin = defaults.UpgradeMargin
	}
	if p.UpgradeHold < 0 {
		p.UpgradeHold = defaults.UpgradeHold
	}
	if p.EmergencyMargin <= 0 || math.IsNaN(p.EmergencyMargin) || math.IsInf(p.EmergencyMargin, 0) {
		p.EmergencyMargin = defaults.EmergencyMargin
	}
	return p
}

type PathSwitchDecision struct {
	Switch         bool
	Reason         string
	CurrentScore   float64
	CandidateScore float64
	Improvement    float64
	HoldRemaining  time.Duration
}

// PathSwitchGate applies hysteresis to path promotion. The same candidate must
// stay sufficiently better for UpgradeHold before it replaces the active path.
// A failed active path or a very large improvement can switch immediately.
type PathSwitchGate struct {
	candidate   string
	betterSince time.Time
}

func (g *PathSwitchGate) Reset() {
	if g == nil {
		return
	}
	g.candidate = ""
	g.betterSince = time.Time{}
}

func (g *PathSwitchGate) Evaluate(
	now time.Time,
	currentName string,
	current PathQuality,
	candidateName string,
	candidate PathQuality,
	policy PathScorePolicy,
) PathSwitchDecision {
	policy = policy.normalized()
	decision := PathSwitchDecision{
		CurrentScore:   policy.Score(current),
		CandidateScore: policy.Score(candidate),
	}

	if g == nil {
		decision.Reason = "gate_unavailable"
		return decision
	}
	if candidateName == "" || candidateName == currentName {
		g.Reset()
		decision.Reason = "no_candidate"
		return decision
	}
	if !candidate.Available {
		g.Reset()
		decision.Reason = "candidate_unavailable"
		return decision
	}
	if !current.Available {
		g.Reset()
		decision.Switch = true
		decision.Improvement = math.Inf(1)
		decision.Reason = "current_unavailable"
		return decision
	}

	decision.Improvement = decision.CurrentScore - decision.CandidateScore
	if decision.Improvement < policy.UpgradeMargin {
		g.Reset()
		decision.Reason = "candidate_not_better_enough"
		return decision
	}
	if decision.Improvement >= policy.EmergencyMargin {
		g.Reset()
		decision.Switch = true
		decision.Reason = "candidate_much_better"
		return decision
	}

	if now.IsZero() {
		now = time.Now()
	}
	if g.candidate != candidateName || g.betterSince.IsZero() {
		g.candidate = candidateName
		g.betterSince = now
		if policy.UpgradeHold <= 0 {
			g.Reset()
			decision.Switch = true
			decision.Reason = "candidate_better"
			return decision
		}
		decision.HoldRemaining = policy.UpgradeHold
		decision.Reason = "candidate_probation"
		return decision
	}

	elapsed := now.Sub(g.betterSince)
	if elapsed < 0 {
		g.betterSince = now
		elapsed = 0
	}
	if elapsed >= policy.UpgradeHold {
		g.Reset()
		decision.Switch = true
		decision.Reason = "candidate_stable_better"
		return decision
	}
	decision.HoldRemaining = policy.UpgradeHold - elapsed
	decision.Reason = "candidate_probation"
	return decision
}
