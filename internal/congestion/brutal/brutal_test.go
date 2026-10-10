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

func TestBrutalBeforeRTTStatsInstalled(t *testing.T) {
	b := NewBrutalSender(1_000_000, false)
	if got := b.GetCongestionWindow(); got < congestion.InitialPacketSize {
		t.Fatalf("initial congestion window %d is smaller than packet size", got)
	}
	if !b.CanSend(0) {
		t.Fatal("sender cannot send its initial flight")
	}
}
