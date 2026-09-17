package tunnel

import (
	"context"
	"io"
	"sync"
)

// InterruptOnCancel binds blocking I/O to a context. The returned stop waits
// for an already-started close callback, so it cannot close a connection after
// a successful dial has handed ownership to its caller.
func InterruptOnCancel(ctx context.Context, closers ...io.Closer) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		for _, c := range closers {
			_ = c.Close()
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			if !stop() {
				<-done
			}
		})
	}
}
