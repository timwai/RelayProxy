package codec

import "testing"

func TestI444ToAYUVUsesDXGIChannelOrder(t *testing.T) {
	// Two pixels: Y=[10,20], U=[30,40], V=[50,60].
	src := []byte{10, 20, 30, 40, 50, 60}
	got, err := I444ToAYUV(src, 2, 1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		50, 30, 10, 0xff,
		60, 40, 20, 0xff,
	}
	if string(got) != string(want) {
		t.Fatalf("AYUV=%v want=%v", got, want)
	}
}

func TestI444ToAYUVHonorsPlaneStride(t *testing.T) {
	// Each plane is two rows with a four-byte stride; only first two bytes per
	// row belong to visible pixels.
	src := []byte{
		10, 20, 0, 0, 11, 21, 0, 0,
		30, 40, 0, 0, 31, 41, 0, 0,
		50, 60, 0, 0, 51, 61, 0, 0,
	}
	got, err := I444ToAYUV(src, 2, 2, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		50, 30, 10, 0xff, 60, 40, 20, 0xff,
		51, 31, 11, 0xff, 61, 41, 21, 0xff,
	}
	if string(got) != string(want) {
		t.Fatalf("AYUV=%v want=%v", got, want)
	}
}


func TestAYUVToBGRAPreservesDominantColors(t *testing.T) {
	i444 := []byte{
		63, 173,
		102, 42,
		240, 26,
	}
	ayuv, err := I444ToAYUV(i444, 2, 1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	bgra, err := AYUVToBGRA(ayuv, 2, 1, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if int(bgra[2]) <= int(bgra[1]) || int(bgra[2]) <= int(bgra[0]) {
		t.Fatalf("first pixel lost red dominance: %v", bgra[:4])
	}
	if int(bgra[5]) <= int(bgra[4]) || int(bgra[5]) <= int(bgra[6]) {
		t.Fatalf("second pixel lost green dominance: %v", bgra[4:8])
	}
}
