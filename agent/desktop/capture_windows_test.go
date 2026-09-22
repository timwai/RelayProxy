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
