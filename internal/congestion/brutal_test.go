package congestion

import "testing"

func TestMbpsToBytesPerSecond(t *testing.T) {
	if got := MbpsToBytesPerSecond(100); got != 12_500_000 {
		t.Fatalf("100 Mbps = %d B/s, want 12500000", got)
	}
	if got := MbpsToBytesPerSecond(0); got != 0 {
		t.Fatalf("0 Mbps = %d B/s, want 0", got)
	}
}

func TestCapRequestedRate(t *testing.T) {
	tests := []struct {
		requested uint64
		cap       uint64
		want      uint64
	}{
		{0, 100, 0},
		{100, 0, 100},
		{100, 200, 100},
		{200, 100, 100},
	}
	for _, tt := range tests {
		if got := CapRequestedRate(tt.requested, tt.cap); got != tt.want {
			t.Fatalf("CapRequestedRate(%d,%d)=%d, want %d", tt.requested, tt.cap, got, tt.want)
		}
	}
}
