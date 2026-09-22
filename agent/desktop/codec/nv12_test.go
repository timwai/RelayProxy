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
