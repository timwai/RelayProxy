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
