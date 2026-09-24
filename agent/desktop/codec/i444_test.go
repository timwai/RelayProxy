package codec

import (
	"image"
	"image/color"
	"testing"
)

func TestRGBAtoI444PreservesPerPixelChroma(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 0xff})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 0xff})
	src.SetRGBA(0, 1, color.RGBA{B: 255, A: 0xff})
	src.SetRGBA(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 0xff})

	got, err := RGBAtoI444(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 12 {
		t.Fatalf("I444 bytes=%d want=12", len(got))
	}
	u := got[4:8]
	v := got[8:12]
	if u[0] == u[1] && u[0] == u[2] && v[0] == v[1] && v[0] == v[2] {
		t.Fatal("I444 chroma was unexpectedly shared across pixels")
	}
}

func TestBGRAtoI444HandlesPaddedStride(t *testing.T) {
	const width, height, stride = 2, 2, 12
	pix := make([]byte, stride*height)
	setBGRA := func(x, y int, b, g, r byte) {
		offset := y*stride + x*4
		pix[offset], pix[offset+1], pix[offset+2], pix[offset+3] = b, g, r, 0xff
	}
	setBGRA(0, 0, 0, 0, 255)
	setBGRA(1, 0, 0, 255, 0)
	setBGRA(0, 1, 255, 0, 0)
	setBGRA(1, 1, 255, 255, 255)

	got, err := BGRAtoI444(pix, width, height, stride, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != width*height*3 {
		t.Fatalf("I444 bytes=%d", len(got))
	}
}

func TestI444RoundTripKeepsColoredEdges(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 0xff})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 0xff})
	src.SetRGBA(0, 1, color.RGBA{B: 255, A: 0xff})
	src.SetRGBA(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 0xff})

	i444, err := RGBAtoI444(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	bgra, err := I444ToBGRA(i444, 2, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bgra) != 16 {
		t.Fatalf("BGRA bytes=%d want=16", len(bgra))
	}
	r0, g0, b0 := int(bgra[2]), int(bgra[1]), int(bgra[0])
	r1, g1, b1 := int(bgra[6]), int(bgra[5]), int(bgra[4])
	if r0 <= g0 || r0 <= b0 {
		t.Fatalf("first pixel lost red dominance: r=%d g=%d b=%d", r0, g0, b0)
	}
	if g1 <= r1 || g1 <= b1 {
		t.Fatalf("second pixel lost green dominance: r=%d g=%d b=%d", r1, g1, b1)
	}
}

func TestRawFrameValidatesI444Planes(t *testing.T) {
	frame := RawFrame{
		Format: PixelFormatI444,
		Pix:    make([]byte, 4*2*3),
		Width:  4,
		Height: 2,
		Stride: 4,
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	frame.Pix = frame.Pix[:len(frame.Pix)-1]
	if err := frame.Validate(); err == nil {
		t.Fatal("short I444 frame was accepted")
	}
}
