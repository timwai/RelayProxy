package congestion

import (
	"testing"

	quiccongestion "github.com/apernet/quic-go/congestion"
)

func TestSeedPacketSize(t *testing.T) {
	tests := []struct {
		name     string
		quicSize quiccongestion.ByteCount
		byAddr   quiccongestion.ByteCount
		want     quiccongestion.ByteCount
	}{
		{name: "connection lower", quicSize: 1200, byAddr: 1252, want: 1200},
		{name: "address lower", quicSize: 1350, byAddr: 1252, want: 1252},
		{name: "missing connection size", quicSize: 0, byAddr: 1252, want: 1252},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seedPacketSize(tt.quicSize, tt.byAddr); got != tt.want {
				t.Fatalf("seedPacketSize(%d, %d)=%d, want %d", tt.quicSize, tt.byAddr, got, tt.want)
			}
		})
	}
}
