//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

func TestNVCodecValidationAdapterDescABI(t *testing.T) {
	if got := unsafe.Sizeof(nvcodecValidationAdapterDesc1{}); got != 312 {
		t.Fatalf("DXGI_ADAPTER_DESC1 size=%d want=312", got)
	}
	if got := unsafe.Offsetof(nvcodecValidationAdapterDesc1{}.VendorID); got != 256 {
		t.Fatalf("VendorId offset=%d want=256", got)
	}
	if got := unsafe.Sizeof(nvcodecValidationSubresourceData{}); got != 16 {
		t.Fatalf("D3D11_SUBRESOURCE_DATA size=%d want=16", got)
	}
}

func TestNVCodecValidationAYUVPattern(t *testing.T) {
	const width, height = 640, 360
	pixels := nvcodecValidationAYUVPattern(width, height)
	if len(pixels) != width*height*4 {
		t.Fatalf("pattern bytes=%d", len(pixels))
	}
	checks := [][2]int{
		{width / 4, height / 4},
		{width * 3 / 4, height / 4},
		{width / 4, height * 3 / 4},
		{width * 3 / 4, height * 3 / 4},
	}
	for _, position := range checks {
		x, y := position[0], position[1]
		expected := nvcodecValidationExpectedPixel(x, y, width, height)
		offset := (y*width + x) * 4
		if got := pixels[offset : offset+4]; got[0] != expected.V ||
			got[1] != expected.U || got[2] != expected.Y || got[3] != 255 {
			t.Fatalf(
				"pixel %d,%d=%v want VUYA=[%d %d %d 255]",
				x,
				y,
				got,
				expected.V,
				expected.U,
				expected.Y,
			)
		}
	}
}

func TestNVCodecValidationConstants(t *testing.T) {
	if nvcodecValidationVendorNVIDIA != 0x10de {
		t.Fatalf("NVIDIA vendor=%#x", nvcodecValidationVendorNVIDIA)
	}
	if nvcodecValidationChannelTolerance < 16 || nvcodecValidationChannelTolerance > 64 {
		t.Fatalf("unexpected channel tolerance=%d", nvcodecValidationChannelTolerance)
	}
}
