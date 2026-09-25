//go:build windows && amd64

package codec

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestNVENCInitializeABISizes(t *testing.T) {
	if got := unsafe.Sizeof(nvencConfigBlob{}); got != nvencConfigSize {
		t.Fatalf("NV_ENC_CONFIG blob size=%d want=%d", got, nvencConfigSize)
	}
	if got := unsafe.Alignof(nvencConfigBlob{}); got != 8 {
		t.Fatalf("NV_ENC_CONFIG blob alignment=%d want=8", got)
	}
	if got := unsafe.Sizeof(nvencPresetConfigBlob{}); got != nvencPresetConfigSize {
		t.Fatalf("NV_ENC_PRESET_CONFIG blob size=%d want=%d", got, nvencPresetConfigSize)
	}
	if got := unsafe.Alignof(nvencPresetConfigBlob{}); got != 8 {
		t.Fatalf("NV_ENC_PRESET_CONFIG blob alignment=%d want=8", got)
	}
	if got := unsafe.Sizeof(nvencInitializeParamsBlob{}); got != nvencInitializeParamsSize {
		t.Fatalf("NV_ENC_INITIALIZE_PARAMS blob size=%d want=%d", got, nvencInitializeParamsSize)
	}
	if got := unsafe.Alignof(nvencInitializeParamsBlob{}); got != 8 {
		t.Fatalf("NV_ENC_INITIALIZE_PARAMS blob alignment=%d want=8", got)
	}
}

func TestConfigureNVENCHEVC444LowLatency(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.FPS = 60
	cfg.TargetBitrate = 18_000_000
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8

	blob := &nvencConfigBlob{}
	binary.LittleEndian.PutUint32(blob.Data[nvencConfigRCFlagsOffset:nvencConfigRCFlagsOffset+4], nvencRCFlagEnableLookahead)
	if err := configureNVENCHEVC444(blob, cfg); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(blob.Data[nvencConfigRCRateControlOffset:nvencConfigRCRateControlOffset+4]); got != nvencRateControlCBR {
		t.Fatalf("rate control=%d", got)
	}
	if got := binary.LittleEndian.Uint32(blob.Data[nvencConfigRCAverageBitrateOffset:nvencConfigRCAverageBitrateOffset+4]); got != 18_000_000 {
		t.Fatalf("average bitrate=%d", got)
	}
	if got := binary.LittleEndian.Uint32(blob.Data[nvencConfigRCVBVSizeOffset:nvencConfigRCVBVSizeOffset+4]); got != 300_000 {
		t.Fatalf("VBV size=%d want=300000", got)
	}
	flags := binary.LittleEndian.Uint32(blob.Data[nvencConfigRCFlagsOffset:nvencConfigRCFlagsOffset+4])
	if flags&nvencRCFlagEnableLookahead != 0 || flags&nvencRCFlagZeroReorder == 0 {
		t.Fatalf("low-latency RC flags=%#x", flags)
	}
	hevc := binary.LittleEndian.Uint32(blob.Data[nvencConfigHEVCBitfieldOffset:nvencConfigHEVCBitfieldOffset+4])
	if hevc&nvencHEVCChromaMask != nvencHEVCChroma444 || hevc&nvencHEVCRepeatSPSPPS == 0 {
		t.Fatalf("HEVC flags=%#x", hevc)
	}
	if got := binary.LittleEndian.Uint32(blob.Data[nvencConfigFrameIntervalPOffset:nvencConfigFrameIntervalPOffset+4]); got != 1 {
		t.Fatalf("frameIntervalP=%d want=1", got)
	}
	if got := binary.LittleEndian.Uint32(blob.Data[nvencConfigGOPLengthOffset:nvencConfigGOPLengthOffset+4]); got != 120 {
		t.Fatalf("GOP=%d want=120", got)
	}
}

func TestBuildNVENCInitializeParams(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1280
	cfg.Height = 720
	cfg.FPS = 30
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8
	config := &nvencConfigBlob{}

	params, err := buildNVENCInitializeParams(config, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeVersionOffset:nvencInitializeVersionOffset+4]); got != nvencVersionWithReservedBit(7) {
		t.Fatalf("init version=%#x", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeWidthOffset:nvencInitializeWidthOffset+4]); got != 1280 {
		t.Fatalf("width=%d", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeHeightOffset:nvencInitializeHeightOffset+4]); got != 720 {
		t.Fatalf("height=%d", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeEnablePTDOffset:nvencInitializeEnablePTDOffset+4]); got != 1 {
		t.Fatalf("enablePTD=%d", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeTuningInfoOffset:nvencInitializeTuningInfoOffset+4]); got != nvencTuningUltraLowLatency {
		t.Fatalf("tuning=%d", got)
	}
	if got := binary.LittleEndian.Uint32(params.Data[nvencInitializeBufferFormatOffset:nvencInitializeBufferFormatOffset+4]); got != uint32(nvencBufferFormatAYUV) {
		t.Fatalf("buffer format=%#x", got)
	}
	if got := binary.LittleEndian.Uint64(params.Data[nvencInitializeEncodeConfigOffset:nvencInitializeEncodeConfigOffset+8]); got != uint64(uintptr(unsafe.Pointer(&config.Data[0]))) {
		t.Fatalf("encodeConfig pointer=%#x", got)
	}
}

func TestConfigureNVENCHEVC444Rejects420(t *testing.T) {
	cfg := DefaultVideoConfig()
	if err := configureNVENCHEVC444(&nvencConfigBlob{}, cfg); err == nil {
		t.Fatal("4:2:0 config was accepted by NVENC HEVC 4:4:4 initializer")
	}
}

func TestNVENCGUIDConstants(t *testing.T) {
	if nvencHEVCFRExtGUID != (nvencGUID{
		Data1: 0x51ec32b5,
		Data2: 0x1b4c,
		Data3: 0x453c,
		Data4: [8]byte{0x9c, 0xbd, 0xb6, 0x16, 0xbd, 0x62, 0x13, 0x41},
	}) {
		t.Fatalf("HEVC FRExt GUID=%+v", nvencHEVCFRExtGUID)
	}
}
