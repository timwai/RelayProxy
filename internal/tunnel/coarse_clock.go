package tunnel

import (
	"sync"
	"sync/atomic"
	"time"
)

const coarseClockInterval = 100 * time.Millisecond

var (
	coarseClockOnce sync.Once
	coarseNowNanos  atomic.Int64
)

// coarseTimeNanos provides a process-wide low-cost clock for hot-path refresh
// checks. One shared ticker replaces time.Now calls on every copied chunk
// across every active proxy stream.
func coarseTimeNanos() int64 {
	coarseClockOnce.Do(func() {
		coarseNowNanos.Store(time.Now().UnixNano())
		go func() {
			ticker := time.NewTicker(coarseClockInterval)
			defer ticker.Stop()
			for now := range ticker.C {
				coarseNowNanos.Store(now.UnixNano())
			}
		}()
	})
	return coarseNowNanos.Load()
}
