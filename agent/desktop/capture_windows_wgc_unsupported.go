//go:build windows && !amd64

package desktop

import (
	"context"
	"fmt"

	"github.com/go-mswin/screencapture"
)

func openWGCFrameStream(
	ctx context.Context,
	display screencapture.Display,
	maxFPS int,
) (windowsFrameStream, error) {
	_ = ctx
	_ = display
	_ = maxFPS
	return nil, fmt.Errorf("%w: supported by the current WinRT bindings on amd64 only",
		errWindowsGraphicsCaptureUnavailable)
}
