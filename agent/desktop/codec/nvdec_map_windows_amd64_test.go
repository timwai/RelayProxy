//go:build windows && amd64

package codec

import (
	"testing"
	"time"
	"unsafe"
)

func TestNVDECDisplayAndProcABISizes(t *testing.T) {
	if got := unsafe.Sizeof(nvdecParserDisplayInfo{}); got != 24 {
		t.Fatalf("CUVIDPARSERDISPINFO size=%d want=24", got)
	}
	if got := unsafe.Alignof(nvdecParserDisplayInfo{}); got != 8 {
		t.Fatalf("CUVIDPARSERDISPINFO alignment=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvdecParserDisplayInfo{}.Timestamp); got != 16 {
		t.Fatalf("CUVIDPARSERDISPINFO timestamp offset=%d want=16", got)
	}

	if got := unsafe.Sizeof(nvdecProcParams{}); got != 264 {
		t.Fatalf("CUVIDPROCPARAMS size=%d want=264", got)
	}
	if got := unsafe.Alignof(nvdecProcParams{}); got != 8 {
		t.Fatalf("CUVIDPROCPARAMS alignment=%d want=8", got)
	}
	if got := unsafe.Offsetof(nvdecProcParams{}.RawInputDPtr); got != 24 {
		t.Fatalf("CUVIDPROCPARAMS raw_input_dptr offset=%d want=24", got)
	}
	if got := unsafe.Offsetof(nvdecProcParams{}.OutputStream); got != 56 {
		t.Fatalf("CUVIDPROCPARAMS output_stream offset=%d want=56", got)
	}
	if got := unsafe.Offsetof(nvdecProcParams{}.Reserved2); got != 248 {
		t.Fatalf("CUVIDPROCPARAMS Reserved2 offset=%d want=248", got)
	}
}

func TestNVDECTimestampDuration(t *testing.T) {
	cases := []struct {
		ticks int64
		want  time.Duration
	}{
		{0, 0},
		{10_000_000, time.Second},
		{5_000_000, 500 * time.Millisecond},
		{12_500_000, 1250 * time.Millisecond},
	}
	for _, tc := range cases {
		if got := nvdecTimestampDuration(tc.ticks); got != tc.want {
			t.Fatalf("ticks=%d duration=%v want=%v", tc.ticks, got, tc.want)
		}
	}
}

func TestNVDECHandleDisplayQueuesProgressiveFrame(t *testing.T) {
	parser := &nvdecHEVC444Parser{
		session:      &nvdecD3D11Session{},
		decoder:      1,
		mappedFrames: make(map[uint64]*nvdecMappedFrame),
	}
	info := nvdecParserDisplayInfo{
		PictureIndex:     3,
		ProgressiveFrame: 1,
		Timestamp:        20_000_000,
	}
	if err := parser.handleDisplay(uintptr(unsafe.Pointer(&info))); err != nil {
		t.Fatal(err)
	}
	if parser.PendingDisplayFrames() != 1 {
		t.Fatalf("pending display frames=%d want=1", parser.PendingDisplayFrames())
	}
	if parser.displayCalls != 1 {
		t.Fatalf("display calls=%d want=1", parser.displayCalls)
	}
}

func TestNVDECHandleDisplayRejectsInterlacedFrame(t *testing.T) {
	parser := &nvdecHEVC444Parser{
		session: &nvdecD3D11Session{},
		decoder: 1,
	}
	info := nvdecParserDisplayInfo{
		PictureIndex:     1,
		ProgressiveFrame: 0,
	}
	if err := parser.handleDisplay(uintptr(unsafe.Pointer(&info))); err == nil {
		t.Fatal("interlaced display frame was accepted")
	}
}

func TestNVDECMappedFrameDetachIsIdempotent(t *testing.T) {
	frame := &nvdecMappedFrame{
		devicePtr: 0x1234,
		pitch:     4096,
		width:     1920,
		height:    1080,
	}
	if got := frame.detach(); got != 0x1234 {
		t.Fatalf("first detach=%#x want=%#x", got, uint64(0x1234))
	}
	if got := frame.detach(); got != 0 {
		t.Fatalf("second detach=%#x want=0", got)
	}
	if frame.DevicePointer() != 0 || frame.Pitch() != 0 {
		t.Fatal("detached frame still exposes mapped memory")
	}
}
