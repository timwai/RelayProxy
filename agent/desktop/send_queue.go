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
