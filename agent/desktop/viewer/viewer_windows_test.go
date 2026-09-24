//go:build windows

package viewer

import (
	"errors"
	"testing"
)

func TestViewportPackingRoundTrip(t *testing.T) {
	for _, tt := range []Viewport{
		{Width: 1920, Height: 1080},
		{Width: 1280, Height: 720},
		{Width: 3840, Height: 2160},
	} {
		got := unpackViewport(packViewport(tt.Width, tt.Height))
		if got != tt {
			t.Fatalf("viewport round trip=%+v want=%+v", got, tt)
		}
	}
	if got := unpackViewport(packViewport(0, 1080)); got.Valid() {
		t.Fatalf("invalid viewport unexpectedly round-tripped: %+v", got)
	}
}

func TestWindowsViewerReconfigureNoopDoesNotNeedWindow(t *testing.T) {
	viewer := &windowsViewer{
		reconfigureCh: make(chan viewerReconfigureRequest, 1),
		done:          make(chan struct{}),
	}
	viewer.media.Store(packViewport(1920, 1080))

	if err := viewer.Reconfigure(1920, 1080); err != nil {
		t.Fatalf("same-size reconfigure=%v", err)
	}
	if got := viewer.mediaSize(); got != (Viewport{Width: 1920, Height: 1080}) {
		t.Fatalf("media size=%+v", got)
	}

	if err := viewer.Reconfigure(1280, 720); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("headless resize error=%v want ErrUnavailable", err)
	}
	if got := viewer.mediaSize(); got != (Viewport{Width: 1920, Height: 1080}) {
		t.Fatalf("failed resize changed media size: %+v", got)
	}
}
