//go:build windows && amd64

package codec

import (
	"encoding/binary"
	"testing"
	"time"
	"unsafe"
)

func TestNVENCEncodeABISizes(t *testing.T) {
	if got := unsafe.Sizeof(nvencPicParamsBlob{}); got != nvencPicParamsSize {
		t.Fatalf("NV_ENC_PIC_PARAMS blob size=%d want=%d", got, nvencPicParamsSize)
	}
	if got := unsafe.Alignof(nvencPicParamsBlob{}); got != 8 {
		t.Fatalf("NV_ENC_PIC_PARAMS alignment=%d want=8", got)
	}
	if got := unsafe.Sizeof(nvencLockBitstreamBlob{}); got != nvencLockBitstreamSize {
		t.Fatalf("NV_ENC_LOCK_BITSTREAM blob size=%d want=%d", got, nvencLockBitstreamSize)
	}
	if got := unsafe.Alignof(nvencLockBitstreamBlob{}); got != 8 {
		t.Fatalf("NV_ENC_LOCK_BITSTREAM alignment=%d want=8", got)
	}
	if got := unsafe.Sizeof(nvencSequenceParamBlob{}); got != nvencSequenceParamSize {
		t.Fatalf("NV_ENC_SEQUENCE_PARAM_PAYLOAD blob size=%d want=%d", got, nvencSequenceParamSize)
	}
	if got := unsafe.Alignof(nvencSequenceParamBlob{}); got != 8 {
		t.Fatalf("NV_ENC_SEQUENCE_PARAM_PAYLOAD alignment=%d want=8", got)
	}
}

func TestNVENCEncodeConstants(t *testing.T) {
	if nvencPicStructFrame != 1 {
		t.Fatalf("picture struct frame=%d", nvencPicStructFrame)
	}
	if nvencPicFlagForceIDR != 0x2 || nvencPicFlagOutputSPSPPS != 0x4 {
		t.Fatalf("picture flags forceIDR=%#x SPSPPS=%#x", nvencPicFlagForceIDR, nvencPicFlagOutputSPSPPS)
	}
	if nvencVersionWithReservedBit(7) != 0xf107000d {
		t.Fatalf("pic params v7=%#x", nvencVersionWithReservedBit(7))
	}
	if nvencVersionWithReservedBit(2) != 0xf102000d {
		t.Fatalf("lock bitstream v2=%#x", nvencVersionWithReservedBit(2))
	}
}

func TestNVENCH265KeyFrameDetection(t *testing.T) {
	if !nvencH265KeyFrame(nil, nvencPicTypeIDR) {
		t.Fatal("IDR picture type was not treated as key frame")
	}
	idr := []byte{0, 0, 0, 1, byte(19 << 1), 1, 2, 3}
	if !nvencH265KeyFrame(idr, 0) {
		t.Fatal("HEVC IRAP NAL was not treated as key frame")
	}
	if nvencH265KeyFrame([]byte{0, 0, 0, 1, byte(1 << 1), 1, 2}, 0) {
		t.Fatal("ordinary HEVC slice was treated as key frame")
	}
}

func TestNVENCH265ParameterSets(t *testing.T) {
	data := []byte{
		0, 0, 0, 1, byte(32 << 1), 1, 0xaa,
		0, 0, 0, 1, byte(33 << 1), 1, 0xbb,
		0, 0, 0, 1, byte(34 << 1), 1, 0xcc,
		0, 0, 0, 1, byte(19 << 1), 1, 0xdd,
	}
	got := nvencH265ParameterSets(data)
	if !H265HasParameterSets(got) {
		t.Fatalf("parameter set extraction failed: %x", got)
	}
	if len(got) >= len(data) {
		t.Fatalf("parameter set extraction retained frame payload: %x", got)
	}
}

func TestNVENCPicParamsFieldOffsets(t *testing.T) {
	var pic nvencPicParamsBlob
	binary.LittleEndian.PutUint32(pic.Data[nvencPicInputWidthOffset:nvencPicInputWidthOffset+4], 1920)
	binary.LittleEndian.PutUint32(pic.Data[nvencPicInputHeightOffset:nvencPicInputHeightOffset+4], 1080)
	binary.LittleEndian.PutUint64(pic.Data[nvencPicInputBufferOffset:nvencPicInputBufferOffset+8], 0x1111222233334444)
	binary.LittleEndian.PutUint64(pic.Data[nvencPicOutputBitstreamOffset:nvencPicOutputBitstreamOffset+8], 0x5555666677778888)
	if got := binary.LittleEndian.Uint32(pic.Data[4:8]); got != 1920 {
		t.Fatalf("inputWidth offset mismatch: %d", got)
	}
	if got := binary.LittleEndian.Uint32(pic.Data[8:12]); got != 1080 {
		t.Fatalf("inputHeight offset mismatch: %d", got)
	}
	if got := binary.LittleEndian.Uint64(pic.Data[40:48]); got != 0x1111222233334444 {
		t.Fatalf("inputBuffer offset mismatch: %#x", got)
	}
	if got := binary.LittleEndian.Uint64(pic.Data[48:56]); got != 0x5555666677778888 {
		t.Fatalf("outputBitstream offset mismatch: %#x", got)
	}
}

func TestNVENCH265EncoderControlDefaults(t *testing.T) {
	encoder := &nvencH265Encoder{
		cfg: VideoConfig{FPS: 60},
		stats: EncoderStats{
			Hardware: true,
			Backend:  "nvenc-hevc444-d3d11",
		},
	}
	if err := encoder.ForceIDR(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !encoder.forceIDR {
		t.Fatal("ForceIDR did not set pending IDR")
	}
	if err := encoder.Reconfigure(context.Background(), VideoConfig{}); err != ErrEncoderControlUnsupported {
		t.Fatalf("Reconfigure error=%v", err)
	}
	stats := encoder.Stats()
	if !stats.Hardware || stats.Backend != "nvenc-hevc444-d3d11" {
		t.Fatalf("stats=%+v", stats)
	}
	_ = time.Second
}
