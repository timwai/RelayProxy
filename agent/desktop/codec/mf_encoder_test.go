package codec

import "testing"

func TestFrameToNV12PacksStride(t *testing.T) {
	const width, height, stride = 4, 2, 8
	raw := make([]byte, stride*height+stride*(height/2))
	copy(raw[0:], []byte{1, 2, 3, 4})
	copy(raw[stride:], []byte{5, 6, 7, 8})
	copy(raw[stride*height:], []byte{9, 10, 11, 12})

	out, err := frameToNV12(RawFrame{
		Format: PixelFormatNV12,
		Pix:    raw, Width: width, Height: height, Stride: stride,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if string(out) != string(want) {
		t.Fatalf("out=%v want=%v", out, want)
	}
}

func TestFrameToNV12RejectsInvalidFrame(t *testing.T) {
	_, err := frameToNV12(RawFrame{Format: PixelFormatNV12, Width: 3, Height: 2, Stride: 3, Pix: make([]byte, 9)}, nil)
	if err == nil {
		t.Fatal("invalid frame accepted")
	}
}
