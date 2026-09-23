//go:build windows && amd64

package desktop

import "testing"

func TestWGCMinUpdateInterval(t *testing.T) {
	tests := []struct {
		name     string
		maxFPS   int
		wantTick int64
	}{
		{name: "unlimited zero", maxFPS: 0, wantTick: 0},
		{name: "unlimited negative", maxFPS: -1, wantTick: 0},
		{name: "one fps", maxFPS: 1, wantTick: 10_000_000},
		{name: "thirty fps", maxFPS: 30, wantTick: 333_333},
		{name: "sixty fps", maxFPS: 60, wantTick: 166_666},
		{name: "minimum one tick", maxFPS: 20_000_000, wantTick: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wgcMinUpdateInterval(tt.maxFPS)
			if got.Duration != tt.wantTick {
				t.Fatalf("maxFPS=%d duration=%d want=%d", tt.maxFPS, got.Duration, tt.wantTick)
			}
		})
	}
}
