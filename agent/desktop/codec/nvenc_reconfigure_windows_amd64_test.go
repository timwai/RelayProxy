//go:build windows && amd64

package codec

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestNVENCReconfigureABISize(t *testing.T) {
	if got := unsafe.Sizeof(nvencReconfigureParamsBlob{}); got != nvencReconfigureParamsSize {
		t.Fatalf("NV_ENC_RECONFIGURE_PARAMS blob size=%d want=%d", got, nvencReconfigureParamsSize)
	}
	if got := unsafe.Alignof(nvencReconfigureParamsBlob{}); got != 8 {
		t.Fatalf("NV_ENC_RECONFIGURE_PARAMS alignment=%d want=8", got)
	}
	if nvencVersionWithReservedBit(2) != 0xf102000d {
		t.Fatalf("reconfigure version=%#x", nvencVersionWithReservedBit(2))
	}
}

func TestBuildNVENCReconfigureParams(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.FPS = 60
	cfg.TargetBitrate = 24_000_000
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8

	config := &nvencConfigBlob{}
	if err := configureNVENCHEVC444(config, cfg); err != nil {
		t.Fatal(err)
	}
	initParams, params, err := buildNVENCReconfigureParams(config, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if initParams == nil || params == nil {
		t.Fatal("reconfigure builder returned nil blobs")
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencReconfigureVersionOffset:nvencReconfigureVersionOffset+4]); got != nvencVersionWithReservedBit(2) {
		t.Fatalf("version=%#x", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencReconfigureFlagsOffset:nvencReconfigureFlagsOffset+4]); got != nvencReconfigureForceIDR {
		t.Fatalf("flags=%#x want=%#x", got, nvencReconfigureForceIDR)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencReconfigureInitOffset+nvencInitializeWidthOffset : nvencReconfigureInitOffset+nvencInitializeWidthOffset+4]); got != 1920 {
		t.Fatalf("embedded width=%d", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencReconfigureInitOffset+nvencInitializeHeightOffset : nvencReconfigureInitOffset+nvencInitializeHeightOffset+4]); got != 1080 {
		t.Fatalf("embedded height=%d", got)
	}
}

func TestNVENCReconfigureConfigBitrateOnly(t *testing.T) {
	current := DefaultVideoConfig()
	current.Width = 1920
	current.Height = 1080
	current.FPS = 60
	current.TargetBitrate = 18_000_000
	current.Chroma = Chroma444
	current.BitDepth = 8

	next := current
	next.TargetBitrate = 24_000_000
	if !bitrateOnlyReconfigure(current, next) {
		t.Fatal("bitrate-only change was rejected")
	}

	next = current
	next.FPS = 30
	if bitrateOnlyReconfigure(current, next) {
		t.Fatal("FPS change was accepted as bitrate-only")
	}

	next = current
	next.DisableLowLatency = !current.DisableLowLatency
	if bitrateOnlyReconfigure(current, next) {
		t.Fatal("latency-mode change was accepted as bitrate-only")
	}
}
