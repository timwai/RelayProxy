package common

import (
	"testing"
	"time"

	"github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/monotime"
)

func TestPacerBudgetAtHighRateAfterIdle(t *testing.T) {
	// This rate is the maximum allowed by the authenticated Brutal
	// negotiation. The nanoseconds multiplication in the old pacer
	// overflowed at elapsed=147.6ms, causing an artificial stall.
	const rate congestion.ByteCount = 125_000_000_000
	p := NewPacer(func() congestion.ByteCount { return rate })
	base := monotime.Time(time.Second)
	p.SentPacket(base, p.maxBurstSize())
	if got := p.Budget(base); got != 0 {
		t.Fatalf("initial budget = %d, want 0", got)
	}
	for _, delay := range []time.Duration{time.Millisecond, 10 * time.Millisecond, 147573953 * time.Nanosecond, 200 * time.Millisecond, 2 * time.Second} {
		got := p.Budget(base.Add(delay))
		if want := p.maxBurstSize(); delay >= 10*time.Millisecond && got != want {
			t.Fatalf("budget at delay=%v got=%d want burst cap=%d", delay, got, want)
		}
		if got < 0 || got > p.maxBurstSize() {
			t.Fatalf("budget at delay=%v outside [0,%d]: %d", delay, p.maxBurstSize(), got)
		}
	}
}

func TestPacerBudgetNeverDecreasesOnClockRegression(t *testing.T) {
	p := NewPacer(func() congestion.ByteCount { return 10_000 })
	base := monotime.Time(time.Second)
	p.SentPacket(base, congestion.InitialPacketSize)
	before := p.Budget(base)
	if got := p.Budget(base.Add(-time.Millisecond)); got != before {
		t.Fatalf("negative elapsed changed budget: %d vs %d", got, before)
	}
}
