package tunnel

import "time"

var clockEpoch = time.Now()

// monotonicTimeNanos reads the runtime clock without a background ticker.
// Quiet mobile sessions incur no timer wakeups, and wall-clock changes cannot
// postpone deadline refreshes or distort transfer timings. Reserve zero for
// the unset state used by the atomic counters.
func monotonicTimeNanos() int64 {
	return time.Since(clockEpoch).Nanoseconds() + 1
}
