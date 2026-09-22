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
