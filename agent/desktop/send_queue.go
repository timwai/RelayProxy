package desktop

import "time"

func smoothSendQueueDelayMs(current float64, elapsed time.Duration) float64 {
	if elapsed < 0 {
		elapsed = 0
	}
	sample := float64(elapsed.Microseconds()) / 1000
	if current <= 0 {
		return sample
	}
	return current*0.8 + sample*0.2
}

// staleScheduledFrameCount returns how many frame intervals the capture loop is
// behind its ticker schedule. Media loops use this to stay realtime: once a
// scheduled capture is at least one whole frame interval old, it is better to
// drop that obsolete sample and wait for the next current frame than to burst
// through historical ticks after a blocked network send or slow encode.
func staleScheduledFrameCount(scheduled, now time.Time, frameInterval time.Duration) uint64 {
	if frameInterval <= 0 || scheduled.IsZero() || !now.After(scheduled) {
		return 0
	}
	lag := now.Sub(scheduled)
	if lag < frameInterval {
		return 0
	}
	return uint64(lag / frameInterval)
}
