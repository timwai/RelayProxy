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

func TestAYUVToI444RoundTrip(t *testing.T) {
	src := []byte{
		10, 20, 11, 21,
		30, 40, 31, 41,
		50, 60, 51, 61,
	}
	ayuv, err := I444ToAYUV(src, 2, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AYUVToI444(ayuv, 2, 2, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(src) {
		t.Fatalf("I444 round trip=%v want=%v", got, src)
	}
}

func TestAYUVToI444HonorsPaddedStride(t *testing.T) {
	src := []byte{
		50, 30, 10, 0xff, 60, 40, 20, 0xff, 0, 0, 0, 0,
		51, 31, 11, 0xff, 61, 41, 21, 0xff, 0, 0, 0, 0,
	}
	got, err := AYUVToI444(src, 2, 2, 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		10, 20, 11, 21,
		30, 40, 31, 41,
		50, 60, 51, 61,
	}
	if string(got) != string(want) {
		t.Fatalf("I444=%v want=%v", got, want)
	}
}
