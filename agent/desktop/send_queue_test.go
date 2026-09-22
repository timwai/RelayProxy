package desktop

import (
	"testing"
	"time"
)

func TestSmoothSendQueueDelayMs(t *testing.T) {
	first := smoothSendQueueDelayMs(0, 40*time.Millisecond)
	if first < 39.9 || first > 40.1 {
		t.Fatalf("first=%v", first)
	}
	second := smoothSendQueueDelayMs(first, 10*time.Millisecond)
	if second < 33.9 || second > 34.1 {
		t.Fatalf("second=%v", second)
	}
}

func TestStaleScheduledFrameCount(t *testing.T) {
	base := time.Unix(100, 0)
	interval := 20 * time.Millisecond

	if got := staleScheduledFrameCount(base, base.Add(19*time.Millisecond), interval); got != 0 {
		t.Fatalf("fresh schedule drops=%d", got)
	}
	if got := staleScheduledFrameCount(base, base.Add(20*time.Millisecond), interval); got != 1 {
		t.Fatalf("one stale interval drops=%d", got)
	}
	if got := staleScheduledFrameCount(base, base.Add(55*time.Millisecond), interval); got != 2 {
		t.Fatalf("multi-interval lag drops=%d", got)
	}
	if got := staleScheduledFrameCount(base, base.Add(time.Second), 0); got != 0 {
		t.Fatalf("invalid interval drops=%d", got)
	}
}

func TestFrameIntervalForFPS(t *testing.T) {
	if got := frameIntervalForFPS(20); got != 50*time.Millisecond {
		t.Fatalf("20 fps interval=%s want=50ms", got)
	}
	if got := frameIntervalForFPS(0); got != time.Second {
		t.Fatalf("invalid fps interval=%s want=1s", got)
	}
}
