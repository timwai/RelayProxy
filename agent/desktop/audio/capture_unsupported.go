//go:build !windows

package audio

import (
	"context"
	"time"
)

func OpenLoopbackCapture(context.Context, PCMConfig, time.Duration) (Capture, error) {
	return nil, ErrUnavailable
}
