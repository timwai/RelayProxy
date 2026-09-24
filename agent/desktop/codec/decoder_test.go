package codec

import (
	"sync/atomic"
	"testing"
)

func TestD3D11SurfaceCloseIsIdempotent(t *testing.T) {
	var releases atomic.Int32
	surface := &D3D11Surface{
		Resource: 1,
		release: func() {
			releases.Add(1)
		},
	}
	surface.Close()
	surface.Close()
	if got := releases.Load(); got != 1 {
		t.Fatalf("release count=%d want 1", got)
	}
}

func TestDecodedFrameCloseReleasesD3D11Surface(t *testing.T) {
	var releases atomic.Int32
	frame := DecodedFrame{
		D3D11: &D3D11Surface{
			Resource: 1,
			release: func() {
				releases.Add(1)
			},
		},
	}
	frame.Close()
	frame.Close()
	if frame.D3D11 != nil {
		t.Fatal("decoded frame retained D3D11 surface after Close")
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("release count=%d want 1", got)
	}
}

func TestD3D11SurfaceGPUFrameCarriesTypedMetadata(t *testing.T) {
	surface := &D3D11Surface{
		Device:      11,
		Resource:    22,
		Subresource: 3,
		Format:      PixelFormatNV12,
	}
	frame, err := surface.GPUFrame(1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Device != 11 || frame.Resource != 22 || frame.Subresource != 3 {
		t.Fatalf("GPU handles=%+v", frame)
	}
	if frame.Width != 1920 || frame.Height != 1080 || frame.Format != "nv12" {
		t.Fatalf("GPU metadata=%+v", frame)
	}
}

func TestD3D11SurfaceGPUFrameRejectsCPUOnlyFormat(t *testing.T) {
	surface := &D3D11Surface{
		Resource: 1,
		Format:   PixelFormatI444,
	}
	if _, err := surface.GPUFrame(1920, 1080); err == nil {
		t.Fatal("CPU-only I444 surface was exposed as a GPU frame")
	}
}

func TestD3D11SurfaceGPUFrameLifetime(t *testing.T) {
	retains := 0
	releases := 0
	surface := &D3D11Surface{
		Resource: 1,
		Format:   PixelFormatAYUV,
		gpuRetain: func() error {
			retains++
			return nil
		},
		gpuRelease: func() {
			releases++
		},
	}
	frame, err := surface.GPUFrame(640, 480)
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Retain(); err != nil {
		t.Fatal(err)
	}
	frame.Release()
	if retains != 1 || releases != 1 {
		t.Fatalf("GPU surface lifetime retains=%d releases=%d", retains, releases)
	}
}
