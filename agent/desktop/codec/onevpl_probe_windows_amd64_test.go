//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

func TestOneVPLVariantWin64Layout(t *testing.T) {
	var value oneVPLVariant
	if got := unsafe.Sizeof(value); got != 16 {
		t.Fatalf("mfxVariant size=%d want=16", got)
	}
	if got := unsafe.Offsetof(value.Type); got != 4 {
		t.Fatalf("mfxVariant Type offset=%d want=4", got)
	}
	if got := unsafe.Offsetof(value.Data); got != 8 {
		t.Fatalf("mfxVariant Data offset=%d want=8", got)
	}
}

func TestOneVPLAYUVFourCC(t *testing.T) {
	want := uint32('A') | uint32('Y')<<8 | uint32('U')<<16 | uint32('V')<<24
	if oneVPLFourCCAYUV != want {
		t.Fatalf("AYUV FourCC=0x%08x want=0x%08x", oneVPLFourCCAYUV, want)
	}
}


func TestOneVPLDirectionMemoryFiltersIncludeResourceType(t *testing.T) {
	filters := oneVPLDirectionMemoryFilters(
		oneVPLPropHEVCEncoder,
		oneVPLPropHEVCEncoderMemory,
		oneVPLPropHEVCEncoderColor,
		oneVPLResourceDX11Texture,
	)
	var memoryFound bool
	for _, filter := range filters {
		if filter.name == oneVPLPropHEVCEncoderMemory {
			memoryFound = true
			if filter.value != oneVPLResourceDX11Texture {
				t.Fatalf("memory filter value=%d want=%d", filter.value, oneVPLResourceDX11Texture)
			}
		}
	}
	if !memoryFound {
		t.Fatal("oneVPL memory handle filter is missing")
	}
}
