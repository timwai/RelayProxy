package codec

import "testing"

func TestNV12ToBGRABlackAndWhite(t *testing.T) {
	src := []byte{
		16, 235,
		16, 235,
		128, 128,
	}
	got, err := NV12ToBGRA(src, 2, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := [][4]byte{
		{0, 0, 0, 255},
		{255, 255, 255, 255},
		{0, 0, 0, 255},
		{255, 255, 255, 255},
	}
	for i, px := range want {
		base := i * 4
		for c := 0; c < 4; c++ {
			if delta := int(got[base+c]) - int(px[c]); delta < -1 || delta > 1 {
				t.Fatalf("pixel %d channel %d = %d want %d", i, c, got[base+c], px[c])
			}
		}
	}
}

func TestNV12ToBGRARejectsShortBuffer(t *testing.T) {
	if _, err := NV12ToBGRA([]byte{16, 16}, 2, 2, 2, nil); err == nil {
		t.Fatal("short NV12 buffer was accepted")
	}
}
