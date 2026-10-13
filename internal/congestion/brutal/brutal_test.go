package brutal

import (
	"testing"
	"time"

	"github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/monotime"
)

func feedAckRate(disableLossCompensation bool, ackCount, lossCount int) float64 {
	b := NewBrutalSender(1_000_000, disableLossCompensation)
	acked := make([]congestion.AckedPacketInfo, ackCount)
	lost := make([]congestion.LostPacketInfo, lossCount)
	b.OnCongestionEventEx(0, monotime.Time(5*time.Second), acked, lost)
	return b.ackRate
}

func TestBrutalLossCompensation(t *testing.T) {
	tests := []struct {
		name      string
		ack, loss int
		want      float64
	}{
		{"no loss", 100, 0, 1.0},
		{"20 percent loss", 80, 20, 0.8},
		{"50 percent loss clamps to floor", 50, 50, minAckRate},
		{"few samples stays 1", 10, 5, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := feedAckRate(false, tt.ack, tt.loss); got != tt.want {
				t.Errorf("compensation on: ackRate = %v, want %v", got, tt.want)
			}
			if got := feedAckRate(true, tt.ack, tt.loss); got != 1.0 {
				t.Errorf("compensation off: ackRate = %v, want 1.0", got)
			}
		})
	}
}

type fixedRTTStats struct {
	congestion.RTTStatsProvider
	rtt time.Duration
}

func (p fixedRTTStats) SmoothedRTT() time.Duration { return p.rtt }

func TestBrutalMinimumCongestionWindowSurvivesLowRTTAndLoss(t *testing.T) {
	sender := NewBrutalSender(8_000_000, false)
	sender.SetMaxDatagramSize(1400)
	want := congestion.ByteCount(minimumWindowDatagrams * 1400)
	for _, rtt := range []time.Duration{0, time.Microsecond, 50 * time.Microsecond, 100 * time.Microsecond} {
		sender.SetRTTStatsProvider(fixedRTTStats{rtt: rtt})
		if got := sender.GetCongestionWindow(); got < want {
			t.Errorf("RTT=%v: cwnd=%d < minimum recovery flight=%d", rtt, got, want)
		}
	}
	// Rate-based BDP still grows above the safe floor at real WAN RTT.
	sender.SetRTTStatsProvider(fixedRTTStats{rtt: 50 * time.Millisecond})
	if got := sender.GetCongestionWindow(); got <= want {
		t.Fatalf("normal WAN bandwidth-delay product was clipped: cwnd=%d floor=%d", got, want)
	}
}

func TestBrutalBeforeRTTStatsInstalled(t *testing.T) {
	b := NewBrutalSender(1_000_000, false)
	if got := b.GetCongestionWindow(); got < congestion.InitialPacketSize {
		t.Fatalf("initial congestion window %d is smaller than packet size", got)
	}
	if !b.CanSend(0) {
		t.Fatal("sender cannot send its initial flight")
	}
}

func TestBrutalDebugBeforeRTTStatsInstalled(t *testing.T) {
	b := NewBrutalSender(1_000_000, false)
	b.debug = true
	// Enough ACKs to exercise both the initial sampling and 20% loss
	// diagnostic branches without installing RTT statistics.
	b.OnCongestionEventEx(0, monotime.Time(10*time.Second),
		make([]congestion.AckedPacketInfo, 10), nil)
	if got := b.debugRTTMilliseconds(); got != 0 {
		t.Fatalf("uninitialized RTT = %d, want 0", got)
	}
	b.OnCongestionEventEx(0, monotime.Time(17*time.Second),
		make([]congestion.AckedPacketInfo, 80),
		make([]congestion.LostPacketInfo, 20))
	if got := b.ackRate; got != 0.8 {
		t.Fatalf("debug-mode loss compensation = %v, want 0.8", got)
	}
}
