//go:build windows && amd64

package codec

import (
	"testing"
)

func TestNVDECD3D11OutputOwnerLifetime(t *testing.T) {
	surface := &nvdecD3D11AYUVInteropSurface{}
	owner := newNVDECD3D11OutputOwner(surface)
	if owner == nil || owner.refs != 1 {
		t.Fatalf("new owner=%v refs=%d", owner, owner.refs)
	}
	if err := owner.retain(); err != nil {
		t.Fatal(err)
	}
	if owner.refs != 2 {
		t.Fatalf("refs after retain=%d want=2", owner.refs)
	}
	owner.release()
	if owner.refs != 1 || owner.surface == nil {
		t.Fatalf("owner released too early refs=%d surface=%v", owner.refs, owner.surface)
	}
	owner.release()
	if owner.refs != 0 || owner.surface != nil {
		t.Fatalf("owner final release refs=%d surface=%v", owner.refs, owner.surface)
	}
	surface.mu.Lock()
	closed := surface.closed
	surface.mu.Unlock()
	if !closed {
		t.Fatal("final owner release did not close interop surface")
	}
	if err := owner.retain(); err != ErrDecoderUnavailable {
		t.Fatalf("retain after final release error=%v", err)
	}
}

func TestNVDECH265DecoderMetadata(t *testing.T) {
	decoder := &nvdecH265Decoder{}
	if !decoder.Hardware() {
		t.Fatal("NVDEC decoder must report hardware acceleration")
	}
	if got := decoder.Backend(); got != nvdecH265D3D11Backend {
		t.Fatalf("backend=%q want=%q", got, nvdecH265D3D11Backend)
	}
}

func TestOpenNVDECH265DecoderRejectsNilD3D11Device(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8
	if _, err := OpenNVDECH265DecoderWithD3D11(t.Context(), cfg, 0); err == nil {
		t.Fatal("nil D3D11 device was accepted")
	}
}
