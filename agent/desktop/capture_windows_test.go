//go:build windows

package desktop

import (
	"testing"
	"time"

	"github.com/go-mswin/screencapture"
)

func TestCopyDXGIFrameConvertsBGRAAndStride(t *testing.T) {
	pix := []byte{
		1, 2, 3, 0, 4, 5, 6, 0, 99, 99, 99, 99,
		7, 8, 9, 0, 10, 11, 12, 0, 88, 88, 88, 88,
	}
	frame := screencapture.Frame{Pix: pix, Width: 2, Height: 2, Stride: 12, Seq: 1, At: time.Now()}
	got, err := copyDXGIFrame(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		3, 2, 1, 255, 6, 5, 4, 255,
		9, 8, 7, 255, 12, 11, 10, 255,
	}
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 2 || got.Stride != 8 {
		t.Fatalf("unexpected image layout: bounds=%v stride=%d", got.Bounds(), got.Stride)
	}
	if len(got.Pix) != len(want) {
		t.Fatalf("pix len=%d want=%d", len(got.Pix), len(want))
	}
	for i := range want {
		if got.Pix[i] != want[i] {
			t.Fatalf("pix[%d]=%d want=%d", i, got.Pix[i], want[i])
		}
	}
}

func TestCopyDXGIFrameReusesBuffer(t *testing.T) {
	frame := screencapture.Frame{
		Pix:   []byte{1, 2, 3, 4},
		Width: 1, Height: 1, Stride: 4, Seq: 1, At: time.Now(),
	}
	first, err := copyDXGIFrame(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := copyDXGIFrame(frame, first)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("DXGI conversion allocated a replacement buffer for the same size")
	}
}

func TestWindowsDesktopCapabilitySnapshot(t *testing.T) {
	captures, displays := windowsDesktopCapabilitySnapshot([]screencapture.Display{
		{
			ID: 10, DeviceName: `\\.\DISPLAY1`, PixelWidth: 1920, PixelHeight: 1080,
			Primary: true, AdapterIndex: 0, OutputIndex: 0,
		},
		{
			ID: 20, DeviceName: `\\.\DISPLAY2`, PixelWidth: 2560, PixelHeight: 1440,
			AdapterIndex: -1, OutputIndex: -1,
		},
	})
	if len(captures) != 2 || captures[0].Backend != "gdi" || captures[1].Backend != "dxgi" {
		t.Fatalf("capture capabilities=%+v", captures)
	}
	if len(displays) != 2 {
		t.Fatalf("display capabilities=%d want=2", len(displays))
	}
	if displays[0].ID != "10" || !displays[0].Primary || displays[0].Width != 1920 || displays[0].Height != 1080 {
		t.Fatalf("primary display=%+v", displays[0])
	}
	if displays[1].ID != "20" || displays[1].Width != 2560 || displays[1].Height != 1440 {
		t.Fatalf("secondary display=%+v", displays[1])
	}
}

func TestResolveWindowsDisplayUsesSessionScopedID(t *testing.T) {
	displays := []screencapture.Display{
		{ID: 10, DeviceName: `\\.\DISPLAY1`, Bounds: screencapture.Rect{X: 0, Y: 0, W: 1920, H: 1080}},
		{ID: 20, DeviceName: `\\.\DISPLAY2`, Bounds: screencapture.Rect{X: 1920, Y: 0, W: 2560, H: 1440}},
	}
	got, selected, err := resolveWindowsDisplay(displays, "20")
	if err != nil {
		t.Fatal(err)
	}
	if !selected || got.ID != 20 || got.DeviceName != `\\.\DISPLAY2` {
		t.Fatalf("selected display=%+v selected=%v", got, selected)
	}
	if _, selected, err := resolveWindowsDisplay(displays, "999"); err == nil || !selected {
		t.Fatalf("stale display id was not rejected: selected=%v err=%v", selected, err)
	}
	if _, selected, err := resolveWindowsDisplay(displays, "invalid"); err == nil || !selected {
		t.Fatalf("invalid display id was not rejected: selected=%v err=%v", selected, err)
	}
}

func TestResolveWindowsDisplayKeepsVirtualDesktopForMultipleDisplays(t *testing.T) {
	displays := []screencapture.Display{{ID: 10}, {ID: 20}}
	got, selected, err := resolveWindowsDisplay(displays, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected || got.ID != 0 {
		t.Fatalf("default multi-display target=%+v selected=%v", got, selected)
	}

	got, selected, err = resolveWindowsDisplay(displays[:1], "")
	if err != nil {
		t.Fatal(err)
	}
	if selected || got.ID != 10 {
		t.Fatalf("single-display optimization target=%+v selected=%v", got, selected)
	}
}

func TestMapDisplayNormalizedToVirtualSideBySide(t *testing.T) {
	virtual := screencapture.Rect{X: -1920, Y: 0, W: 3840, H: 1080}
	left := screencapture.Rect{X: -1920, Y: 0, W: 1920, H: 1080}
	right := screencapture.Rect{X: 0, Y: 0, W: 1920, H: 1080}

	leftStart, _ := mapDisplayNormalizedToVirtual(0, 0, left, virtual)
	leftEnd, _ := mapDisplayNormalizedToVirtual(65535, 0, left, virtual)
	rightStart, _ := mapDisplayNormalizedToVirtual(0, 0, right, virtual)
	rightEnd, _ := mapDisplayNormalizedToVirtual(65535, 0, right, virtual)

	if leftStart != 0 {
		t.Fatalf("left display start=%d want=0", leftStart)
	}
	if leftEnd >= 32768 {
		t.Fatalf("left display end=%d crossed virtual midpoint", leftEnd)
	}
	if rightStart <= 32767 {
		t.Fatalf("right display start=%d did not enter right half", rightStart)
	}
	if rightEnd != 65535 {
		t.Fatalf("right display end=%d want=65535", rightEnd)
	}
}

func TestMapDisplayNormalizedToVirtualHandlesNegativeVerticalOrigin(t *testing.T) {
	virtual := screencapture.Rect{X: 0, Y: -1200, W: 1920, H: 2280}
	upper := screencapture.Rect{X: 0, Y: -1200, W: 1920, H: 1200}
	_, start := mapDisplayNormalizedToVirtual(0, 0, upper, virtual)
	_, end := mapDisplayNormalizedToVirtual(0, 65535, upper, virtual)
	if start != 0 || end >= 35000 {
		t.Fatalf("upper display mapped y range=%d..%d", start, end)
	}
}

func TestWindowsCaptureBackendPolicy(t *testing.T) {
	tests := []struct {
		preference protocol.DesktopCaptureBackend
		want       screencapture.Backend
		wantErr    bool
		explicit   bool
	}{
		{preference: "", want: screencapture.BackendAuto},
		{preference: protocol.DesktopCaptureAuto, want: screencapture.BackendAuto},
		{preference: protocol.DesktopCaptureDXGI, want: screencapture.BackendDuplication, explicit: true},
		{preference: protocol.DesktopCaptureGDI, want: screencapture.BackendGDI, explicit: true},
		{preference: protocol.DesktopCaptureWGC, wantErr: true, explicit: true},
		{preference: protocol.DesktopCaptureBackend("invalid"), wantErr: true, explicit: true},
	}
	for _, tt := range tests {
		got, err := windowsCaptureBackend(tt.preference)
		if (err != nil) != tt.wantErr {
			t.Fatalf("preference=%q err=%v wantErr=%v", tt.preference, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Fatalf("preference=%q backend=%v want=%v", tt.preference, got, tt.want)
		}
		if gotExplicit := explicitWindowsCaptureBackend(tt.preference); gotExplicit != tt.explicit {
			t.Fatalf("preference=%q explicit=%v want=%v", tt.preference, gotExplicit, tt.explicit)
		}
	}
	if !windowsCaptureRequiresDisplayTarget(screencapture.BackendDuplication) {
		t.Fatal("DXGI must require a concrete display target")
	}
	if windowsCaptureRequiresDisplayTarget(screencapture.BackendGDI) ||
		windowsCaptureRequiresDisplayTarget(screencapture.BackendAuto) {
		t.Fatal("GDI/Auto unexpectedly require a concrete display target")
	}
	if got := normalizedWindowsCaptureBackend(""); got != protocol.DesktopCaptureAuto {
		t.Fatalf("normalized empty capture backend=%q want=auto", got)
	}
}
