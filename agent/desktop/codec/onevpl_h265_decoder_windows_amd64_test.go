//go:build windows && amd64

package codec

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestOneVPLHEVC444DecoderDesiredParamABI(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Width = 1920
	cfg.Height = 1080
	cfg.FPS = 30
	cfg.Chroma = Chroma444
	cfg.BitDepth = 8

	param, normalized, err := oneVPLHEVC444DecoderDesiredParam(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != cfg {
		t.Fatalf("normalized=%+v want=%+v", normalized, cfg)
	}
	if !oneVPLHEVC444DecoderParamPreserved(&param) {
		t.Fatal("requested HEVC 4:4:4 decoder fields are incomplete")
	}
	if got := param.u16(oneVPLVideoParamIOPattern); got != oneVPLIOPatternOutSystemMemory {
		t.Fatalf("IOPattern=0x%x", got)
	}
	if got := param.u16(oneVPLFrameInfoHeight); got != 1088 {
		t.Fatalf("aligned height=%d want=1088", got)
	}
}

func TestApplyOneVPLHEVC444DecoderOutputRejects420Header(t *testing.T) {
	cfg := DefaultVideoConfig()
	cfg.Chroma = Chroma444
	var header oneVPLVideoParam
	header.putU32(oneVPLVideoParamCodecID, oneVPLCodecHEVC)
	header.putU16(oneVPLVideoParamCodecProfile, oneVPLHEVCProfileRExt)
	header.putU16(oneVPLFrameInfoChroma, 1)
	if _, err := applyOneVPLHEVC444DecoderOutput(header, cfg); err == nil {
		t.Fatal("4:2:0 HEVC header was accepted by 4:4:4 decoder")
	}
}

func TestOneVPLHEVCInputBitstreamFields(t *testing.T) {
	data := []byte{0, 0, 0, 1, 0x40, 1, 2, 3}
	bitstream := oneVPLHEVCInputBitstream(data, 2*time.Second)
	if got := binary.LittleEndian.Uint32(bitstream[oneVPLBitstreamCodecID:]); got != oneVPLCodecHEVC {
		t.Fatalf("CodecId=0x%x", got)
	}
	if got := binary.LittleEndian.Uint32(bitstream[oneVPLBitstreamDataLength:]); got != uint32(len(data)) {
		t.Fatalf("DataLength=%d want=%d", got, len(data))
	}
	if got := binary.LittleEndian.Uint32(bitstream[oneVPLBitstreamMaxLength:]); got != uint32(len(data)) {
		t.Fatalf("MaxLength=%d want=%d", got, len(data))
	}
	if got := binary.LittleEndian.Uint64(bitstream[oneVPLBitstreamTimestamp:]); got != 180000 {
		t.Fatalf("TimeStamp=%d want=180000", got)
	}
}

func TestOneVPLHEVC444DecoderRejects420Config(t *testing.T) {
	cfg := DefaultVideoConfig()
	if _, _, err := oneVPLHEVC444DecoderDesiredParam(cfg); err == nil {
		t.Fatal("4:2:0 config was accepted by 4:4:4 oneVPL decoder builder")
	}
}
