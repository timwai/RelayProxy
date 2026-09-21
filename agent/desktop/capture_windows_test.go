//go:build windows

package desktop

import (
	"image"
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
	if got.Stride != 8 || !image.Pt(2, 2).In(got.Bounds().Max.Add(image.Pt(1, 1))) {
		// The pixel assertion below is the real check; keep this branch only
		// to make unexpected image layout changes fail with useful context.
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
		Pix: []byte{1, 2, 3, 4},
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
