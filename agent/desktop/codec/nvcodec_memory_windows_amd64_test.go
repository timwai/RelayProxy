//go:build windows && amd64

package codec

import (
	"testing"
	"unsafe"
)

func TestNVCodecDXGIVideoMemoryInfoABI(t *testing.T) {
	if got := unsafe.Sizeof(nvcodecDXGIVideoMemoryInfo{}); got != 32 {
		t.Fatalf("DXGI_QUERY_VIDEO_MEMORY_INFO size=%d want=32", got)
	}
	if got := unsafe.Alignof(nvcodecDXGIVideoMemoryInfo{}); got != 8 {
		t.Fatalf("DXGI_QUERY_VIDEO_MEMORY_INFO alignment=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvcodecDXGIVideoMemoryInfo{}.CurrentUsage); got != 8 {
		t.Fatalf("CurrentUsage offset=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvcodecDXGIVideoMemoryInfo{}.CurrentReservation); got != 24 {
		t.Fatalf("CurrentReservation offset=%d want=24", got)
	}
}

func TestNVCodecIDXGIAdapter3IID(t *testing.T) {
	got := nvcodecValidationIIDIDXGIAdapter3
	if got.Data1 != 0x645967a4 ||
		got.Data2 != 0x1392 ||
		got.Data3 != 0x4310 ||
		got.Data4 != [8]byte{0xa7, 0x98, 0x80, 0x53, 0xce, 0x3e, 0x93, 0xfd} {
		t.Fatalf("IDXGIAdapter3 IID=%v", got)
	}
}
