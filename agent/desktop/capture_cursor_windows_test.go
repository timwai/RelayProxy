//go:build windows

package desktop

import "testing"

func TestCursorImageFromRendersReconstructsAlpha(t *testing.T) {
	black := []byte{
		0, 0, 0, 0,
		0, 0, 255, 0,
	}
	white := []byte{
		255, 255, 255, 0,
		0, 0, 255, 0,
	}
	img, err := cursorImageFromRenders(black, white, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if img.Pix[3] != 0 {
		t.Fatalf("transparent pixel alpha=%d", img.Pix[3])
	}
	if img.Pix[4] != 255 || img.Pix[5] != 0 || img.Pix[6] != 0 || img.Pix[7] != 255 {
		t.Fatalf("opaque red pixel=%v", img.Pix[4:8])
	}
}
