package tunnel

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	pipeBufferSize        = 32 * 1024
	pipeTransferBatchSize = 256 * 1024
)

var pipeBufferPool = sync.Pool{
	New: func() any { return new([pipeBufferSize]byte) },
}

// DeadlineStream is the common subset of net.Conn and TunnelStream.
type DeadlineStream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Pipe preserves normal EOF half-closes, but aborts both directions on an I/O
// error, cancellation or idle timeout. It waits for both pumps before returning.
func Pipe(ctx context.Context, left, right DeadlineStream, idle time.Duration, transferred func(up bool, n int)) (up, down int64) {
	return PipeWithMetrics(ctx, left, right, idle, transferred, nil)
}

// PipeWithMetrics is Pipe with a concurrency-safe live snapshot of both copy
// pumps. Up is left-to-right; Down is right-to-left.
func PipeWithMetrics(ctx context.Context, left, right DeadlineStream, idle time.Duration, transferred func(up bool, n int), metrics *PipeMetrics) (up, down int64) {
	var once sync.Once
	stop := func() { once.Do(func() { _ = left.Close(); _ = right.Close() }) }
	defer stop()
	stopCancel := context.AfterFunc(ctx, stop)
	defer stopCancel()
	var nextRefresh atomic.Int64
	refreshInterval := time.Second
	if idle > 0 && idle < refreshInterval {
		refreshInterval = idle / 4
		if refreshInterval <= 0 {
			refreshInterval = idle
		}
	}
	useCoarseClock := idle >= time.Second
	refresh := func() {
		if idle <= 0 {
			return
		}
		var nowNanos int64
		if useCoarseClock {
			nowNanos = coarseTimeNanos()
		} else {
			nowNanos = time.Now().UnixNano()
		}
		next := nextRefresh.Load()
		if next != 0 && nowNanos < next {
			return
		}
		if !nextRefresh.CompareAndSwap(next, nowNanos+refreshInterval.Nanoseconds()) {
			return
		}
		// Only the infrequent actual deadline refresh needs a precise clock.
		d := time.Now().Add(idle)
		_ = left.SetDeadline(d)
		_ = right.SetDeadline(d)
	}
	refresh()
	copyOne := func(dst, src DeadlineStream, upward bool) int64 {
		bufp := pipeBufferPool.Get().(*[pipeBufferSize]byte)
		defer pipeBufferPool.Put(bufp)
		buf := bufp[:]

		var total int64
		pendingTransfer := 0
		flushTransfer := func() {
			if pendingTransfer > 0 && transferred != nil {
				transferred(upward, pendingTransfer)
				pendingTransfer = 0
			}
		}
		defer flushTransfer()

		for {
			if metrics != nil {
				metrics.begin(upward, "read")
			}
			n, er := src.Read(buf)
			if metrics != nil {
				metrics.end(upward, n)
			}
			if n > 0 {
				refresh()
				if metrics != nil {
					metrics.begin(upward, "write")
				}
				nw, ew := dst.Write(buf[:n])
				if metrics != nil {
					metrics.end(upward, nw)
				}
				total += int64(nw)
				if nw > 0 && transferred != nil {
					pendingTransfer += nw
					if pendingTransfer >= pipeTransferBatchSize {
						flushTransfer()
					}
				}
				if ew != nil || nw != n {
					stop()
					return total
				}
			}
			if er != nil {
				if er == io.EOF {
					if cw, ok := dst.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					} else {
						_ = dst.Close()
					}
				} else {
					stop()
				}
				return total
			}
		}
	}
	finished := make(chan int64, 1)
	go func() { finished <- copyOne(right, left, true) }()
	down = copyOne(left, right, false)
	up = <-finished
	return up, down
}
