//go:build windows

package viewer

import (
	"errors"
	"testing"

	"github.com/lxn/win"
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

func TestAspectFitRect(t *testing.T) {
	tests := []struct {
		name     string
		viewport Viewport
		media    Viewport
		want     fitRect
	}{
		{
			name:     "matching aspect fills viewport",
			viewport: Viewport{Width: 1280, Height: 720},
			media:    Viewport{Width: 1920, Height: 1080},
			want:     fitRect{Left: 0, Top: 0, Right: 1280, Bottom: 720},
		},
		{
			name:     "wide media in square viewport gets horizontal bars",
			viewport: Viewport{Width: 1000, Height: 1000},
			media:    Viewport{Width: 1920, Height: 1080},
			want:     fitRect{Left: 0, Top: 219, Right: 1000, Bottom: 781},
		},
		{
			name:     "four three media in wide viewport gets vertical bars",
			viewport: Viewport{Width: 1200, Height: 600},
			media:    Viewport{Width: 1600, Height: 1200},
			want:     fitRect{Left: 200, Top: 0, Right: 1000, Bottom: 600},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aspectFitRect(tt.viewport, tt.media); got != tt.want {
				t.Fatalf("aspectFitRect()=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestNormalizedPointerUsesVisibleMediaRect(t *testing.T) {
	viewer := &windowsViewer{}
	viewer.viewport.Store(packViewport(1200, 600))
	viewer.media.Store(packViewport(1600, 1200))

	tests := []struct {
		name  string
		x     int
		y     int
		wantX uint16
		wantY uint16
	}{
		{name: "center", x: 600, y: 300, wantX: 32808, wantY: 32822},
		{name: "left black bar clamps to image", x: 0, y: 300, wantX: 0, wantY: 32822},
		{name: "right black bar clamps to image", x: 1199, y: 300, wantX: 65535, wantY: 32822},
		{name: "top edge", x: 600, y: 0, wantX: 32808, wantY: 0},
		{name: "bottom edge", x: 600, y: 599, wantX: 32808, wantY: 65535},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotX, gotY := viewer.normalizedPointer(tt.x, tt.y)
			if gotX != tt.wantX || gotY != tt.wantY {
				t.Fatalf("normalizedPointer(%d,%d)=(%d,%d) want=(%d,%d)", tt.x, tt.y, gotX, gotY, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestNativeViewerFullscreenShortcut(t *testing.T) {
	altContext := uintptr(1 << 29)
	if !nativeViewerFullscreenShortcut(win.WM_SYSKEYDOWN, win.VK_RETURN, altContext) {
		t.Fatal("Alt+Enter syskeydown was not recognized")
	}
	if !nativeViewerFullscreenShortcut(win.WM_SYSKEYUP, win.VK_RETURN, altContext) {
		t.Fatal("Alt+Enter syskeyup was not recognized")
	}
	if nativeViewerFullscreenShortcut(win.WM_KEYDOWN, win.VK_RETURN, altContext) {
		t.Fatal("plain WM_KEYDOWN should not trigger fullscreen")
	}
	if nativeViewerFullscreenShortcut(win.WM_SYSKEYDOWN, win.VK_RETURN, 0) {
		t.Fatal("Enter without Alt context should not trigger fullscreen")
	}
	if nativeViewerFullscreenShortcut(win.WM_SYSKEYDOWN, win.VK_F11, altContext) {
		t.Fatal("unrelated system key should not trigger fullscreen")
	}
}


func TestWindowPlacementRoundTrip(t *testing.T) {
	for _, tt := range []WindowPlacement{
		{X: 120, Y: 80, Width: 1280, Height: 720},
		{X: -1600, Y: 40, Width: 1600, Height: 900, Maximized: true},
	} {
		win32 := windowPlacementToWin32(tt)
		got := windowPlacementFromWin32(win32)
		if got != tt {
			t.Fatalf("window placement round trip=%+v want=%+v", got, tt)
		}
	}
}
