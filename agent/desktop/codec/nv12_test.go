package codec

import (
	"image"
	"testing"
)

func solidRGBA(width, height int, r, g, b byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = r, g, b, 255
		}
	}
	return img
}

func TestRGBAtoNV12BlackAndWhite(t *testing.T) {
	black, err := RGBAtoNV12(solidRGBA(2, 2, 0, 0, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(black) != 6 {
		t.Fatalf("black len=%d", len(black))
	}
	for i := 0; i < 4; i++ {
		if black[i] != 16 {
			t.Fatalf("black Y[%d]=%d", i, black[i])
		}
	}
	if black[4] != 128 || black[5] != 128 {
		t.Fatalf("black UV=%v", black[4:])
	}

	white, err := RGBAtoNV12(solidRGBA(2, 2, 255, 255, 255), black[:0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if white[i] != 235 {
			t.Fatalf("white Y[%d]=%d", i, white[i])
		}
	}
	if white[4] != 128 || white[5] != 128 {
		t.Fatalf("white UV=%v", white[4:])
	}
}

func TestRGBAtoNV12RejectsOddSize(t *testing.T) {
	if _, err := RGBAtoNV12(solidRGBA(3, 2, 0, 0, 0), nil); err == nil {
		t.Fatal("odd-width frame accepted")
	}
}

func TestRGBAtoNV12ReusesDestination(t *testing.T) {
	buf := make([]byte, 64)
	out, err := RGBAtoNV12(solidRGBA(4, 4, 20, 80, 140), buf[:0])
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 24 || &out[0] != &buf[0] {
		t.Fatal("destination buffer was not reused")
	}
}

func TestBGRAtoNV12MatchesRGBAWithPaddedStride(t *testing.T) {
	rgba := solidRGBA(4, 2, 20, 80, 140)
	want, err := RGBAtoNV12(rgba, nil)
	if err != nil {
		t.Fatal(err)
	}

	stride := 24
	bgra := make([]byte, stride*2)
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			o := y*stride + x*4
			bgra[o] = 140
			bgra[o+1] = 80
			bgra[o+2] = 20
			bgra[o+3] = 0
		}
	}
	got, err := BGRAtoNV12(bgra, 4, 2, stride, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("BGRA NV12 len=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BGRA NV12[%d]=%d want=%d", i, got[i], want[i])
		}
	}
}

func TestBGRAtoNV12RejectsShortStride(t *testing.T) {
	if _, err := BGRAtoNV12(make([]byte, 4*4*2), 4, 2, 12, nil); err == nil {
		t.Fatal("BGRA frame with short stride accepted")
	}
}
