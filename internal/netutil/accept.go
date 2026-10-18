package netutil

import (
	"context"
	"math/rand/v2"
	"time"
)

// AcceptBackoff prevents persistent listener errors (including fd exhaustion)
// from turning an accept loop into a CPU and logging busy loop.
type AcceptBackoff struct{ delay time.Duration }

func (b *AcceptBackoff) Reset() { b.delay = 0 }

func (b *AcceptBackoff) Wait(ctx context.Context) bool {
	if b.delay == 0 {
		b.delay = 5 * time.Millisecond
	} else {
		b.delay = min(2*b.delay, time.Second)
	}
	delay := min(b.delay+time.Duration(rand.Int64N(int64(b.delay/4)+1)), time.Second)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
