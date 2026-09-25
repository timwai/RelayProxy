//go:build windows && amd64

package codec

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

var _ D3D11Encoder = (*oneVPLH265Encoder)(nil)

func TestOneVPLHEVC444VideoParamABI(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.FPS = 30
	cfg.TargetBitrate = 12_000_000
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8
	param, normalized, err := oneVPLHEVC444VideoParam(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Sizeof(param) != oneVPLVideoParamSize {
		t.Fatalf("mfxVideoParam size=%d", unsafe.Sizeof(param))
	}
	if normalized != cfg {
		t.Fatalf("normalized=%+v want=%+v", normalized, cfg)
	}
	if !oneVPLHEVC444ParamPreserved(&param) {
		t.Fatal("requested HEVC 4:4:4 fields are incomplete")
	}
	if got := param.u16(oneVPLFrameInfoWidth); got != 1920 {
		t.Fatalf("aligned width=%d", got)
	}
	if got := param.u16(oneVPLFrameInfoHeight); got != 1088 {
		t.Fatalf("aligned height=%d want=1088", got)
	}
	if got := param.u16(oneVPLFrameInfoCropH); got != 1080 {
		t.Fatalf("crop height=%d", got)
	}
	if got := param.u16(oneVPLVideoParamIOPattern); got != oneVPLIOPatternInSystemMemory {
		t.Fatalf("IOPattern=0x%x", got)
	}
}

func TestOneVPLBitrateFieldsScalePastUint16Kbps(t *testing.T) {
	multiplier, value := oneVPLBitrateFields(100_000_000)
	if multiplier < 2 {
		t.Fatalf("multiplier=%d should scale 100 Mbps", multiplier)
	}
	if int(multiplier)*int(value) < 100_000 {
		t.Fatalf("scaled bitrate=%d kbps", int(multiplier)*int(value))
	}
}

func TestOneVPLBitstreamABI(t *testing.T) {
	var bitstream oneVPLBitstream
	if unsafe.Sizeof(bitstream) != oneVPLBitstreamSize {
		t.Fatalf("mfxBitstream size=%d", unsafe.Sizeof(bitstream))
	}
	binary.LittleEndian.PutUint32(bitstream[oneVPLBitstreamDataOffset:], 123)
	binary.LittleEndian.PutUint32(bitstream[oneVPLBitstreamDataLength:], 456)
	if got := binary.LittleEndian.Uint32(bitstream[48:52]); got != 123 {
		t.Fatalf("DataOffset=%d", got)
	}
	if got := binary.LittleEndian.Uint32(bitstream[52:56]); got != 456 {
		t.Fatalf("DataLength=%d", got)
	}
}

func TestOneVPLHEVC444Rejects420Config(t *testing.T) {
	cfg := DefaultVideoConfig()
	if _, _, err := oneVPLHEVC444VideoParam(cfg); err == nil {
		t.Fatal("4:2:0 config was accepted by 4:4:4 oneVPL builder")
	}
}

func TestOneVPLH265KeyFrameDetection(t *testing.T) {
	if !oneVPLH265IsKeyFrame(nil, oneVPLFrameTypeI|oneVPLFrameTypeIDR) {
		t.Fatal("IDR frame type was not treated as a key frame")
	}
	idr := []byte{0, 0, 0, 1, byte(19 << 1), 1, 2, 3}
	if !oneVPLH265IsKeyFrame(idr, 0) {
		t.Fatal("HEVC IDR NAL was not treated as a key frame")
	}
}

func TestOneVPLHEVC444VideoMemoryParam(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8
	param, _, err := oneVPLHEVC444VideoParamForIO(cfg, oneVPLIOPatternInVideoMemory)
	if err != nil {
		t.Fatal(err)
	}
	if got := param.u16(oneVPLVideoParamIOPattern); got != oneVPLIOPatternInVideoMemory {
		t.Fatalf("IOPattern=0x%x want video memory", got)
	}
	if !oneVPLHEVC444ParamPreservedForIO(&param, oneVPLIOPatternInVideoMemory) {
		t.Fatal("video-memory HEVC 4:4:4 encoder fields are incomplete")
	}
	if oneVPLHEVC444ParamPreserved(&param) {
		t.Fatal("video-memory params were mistaken for system-memory params")
	}
}
