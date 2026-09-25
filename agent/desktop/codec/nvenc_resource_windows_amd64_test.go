//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

func TestNVENCResourceABI(t *testing.T) {
	if got := unsafe.Sizeof(nvencRegisterResource{}); got != 1536 {
		t.Fatalf("NV_ENC_REGISTER_RESOURCE size=%d want=1536", got)
	}
	if got := unsafe.Offsetof(nvencRegisterResource{}.ResourceToRegister); got != 24 {
		t.Fatalf("resourceToRegister offset=%d want=24", got)
	}
	if got := unsafe.Offsetof(nvencRegisterResource{}.RegisteredResource); got != 32 {
		t.Fatalf("registeredResource offset=%d want=32", got)
	}
	if got := unsafe.Offsetof(nvencRegisterResource{}.BufferFormat); got != 40 {
		t.Fatalf("bufferFormat offset=%d want=40", got)
	}

	if got := unsafe.Sizeof(nvencMapInputResource{}); got != 1544 {
		t.Fatalf("NV_ENC_MAP_INPUT_RESOURCE size=%d want=1544", got)
	}
	if got := unsafe.Offsetof(nvencMapInputResource{}.RegisteredResource); got != 16 {
		t.Fatalf("map registeredResource offset=%d want=16", got)
	}
	if got := unsafe.Offsetof(nvencMapInputResource{}.MappedResource); got != 24 {
		t.Fatalf("mappedResource offset=%d want=24", got)
	}
	if got := unsafe.Offsetof(nvencMapInputResource{}.MappedBufferFormat); got != 32 {
		t.Fatalf("mappedBufferFmt offset=%d want=32", got)
	}

	if got := unsafe.Sizeof(nvencCreateBitstreamBuffer{}); got != 776 {
		t.Fatalf("NV_ENC_CREATE_BITSTREAM_BUFFER size=%d want=776", got)
	}
	if got := unsafe.Offsetof(nvencCreateBitstreamBuffer{}.BitstreamBuffer); got != 16 {
		t.Fatalf("bitstreamBuffer offset=%d want=16", got)
	}
}

func TestNVENCResourceConstants(t *testing.T) {
	if nvencInputResourceTypeDirectX != 0 {
		t.Fatalf("DirectX resource type=%d want=0", nvencInputResourceTypeDirectX)
	}
	if uint32(nvencBufferFormatAYUV) != 0x04000000 {
		t.Fatalf("AYUV buffer format=%#x", uint32(nvencBufferFormatAYUV))
	}
	if nvencBufferUsageInputImage != 0 {
		t.Fatalf("input image usage=%d want=0", nvencBufferUsageInputImage)
	}
	if got := nvencStructVersion(5); got != 0x7105000d {
		t.Fatalf("register resource struct version=%#x want=%#x", got, uint32(0x7105000d))
	}
	if got := nvencStructVersion(4); got != 0x7104000d {
		t.Fatalf("map input struct version=%#x want=%#x", got, uint32(0x7104000d))
	}
}

func TestNVENCResourceCloseIsIdempotentWithoutSession(t *testing.T) {
	resource := &nvencD3D11InputResource{}
	if err := resource.Close(); err != nil {
		t.Fatalf("first resource Close: %v", err)
	}
	if err := resource.Close(); err != nil {
		t.Fatalf("second resource Close: %v", err)
	}

	bitstream := &nvencBitstreamBuffer{}
	if err := bitstream.Close(); err != nil {
		t.Fatalf("first bitstream Close: %v", err)
	}
	if err := bitstream.Close(); err != nil {
		t.Fatalf("second bitstream Close: %v", err)
	}
}
